package main

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"sync/atomic"
)

// StratumJob represents an active mining work template distributed to XMRig clients.
type StratumJob struct {
	JobID       string `json:"job_id"`
	Blob        string `json:"blob"`
	Target      string `json:"target"`
	SeedHash    string `json:"seed_hash"`
	Height      uint64 `json:"height"`
	Algo        string `json:"algo"`

	// Internal metadata
	SealHash    [32]byte
	BlockTarget *big.Int
	ExtraNonce  uint32
	CleanJobs   bool
}

// JobManager tracks pending jobs, constructs RandomX blobs, and validates share submissions.
type JobManager struct {
	mu          sync.RWMutex
	jobs        map[string]*StratumJob
	currentJob  *StratumJob
	jobCounter  uint64
	shareDiff   uint64
	shareTarget uint32
	targetHex   string
}

// NewJobManager initializes job coordinator with the configured share difficulty.
func NewJobManager(shareDiff uint64) *JobManager {
	target32, targetHex := ShareDiffTarget(shareDiff)
	return &JobManager{
		jobs:        make(map[string]*StratumJob),
		shareDiff:   shareDiff,
		shareTarget: target32,
		targetHex:   targetHex,
	}
}

// CleanHex strips "0x" prefix and returns clean lowercase hex string.
func CleanHex(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X") {
		return strings.ToLower(s[2:])
	}
	return strings.ToLower(s)
}

// HashToBig converts a 32-byte RandomX little-endian hash into a big-endian big.Int.
func HashToBig(hash [32]byte) *big.Int {
	reversed := make([]byte, 32)
	for i := 0; i < 32; i++ {
		reversed[i] = hash[31-i]
	}
	return new(big.Int).SetBytes(reversed)
}

// ParseHexToBytes decodes a hex string into a fixed 32-byte array.
func ParseHexToBytes(hexStr string) ([32]byte, error) {
	clean := CleanHex(hexStr)
	var out [32]byte
	bytes, err := hex.DecodeString(clean)
	if err != nil {
		return out, err
	}
	if len(bytes) != 32 {
		return out, fmt.Errorf("invalid byte length: got %d, expected 32", len(bytes))
	}
	copy(out[:], bytes)
	return out, nil
}

// CreateJob builds a 76-byte RandomX mining blob from a node WorkPackage.
// Blob Layout (76 bytes = 152 hex chars):
//   Offset 0..31: SealHash (32 bytes)
//   Offset 32..38: ExtraNonce (7 bytes)
//   Offset 39..42: Nonce zero placeholder (4 bytes - where XMRig injects uint32 nonce)
//   Offset 43..46: ExtraNonce high (4 bytes)
//   Offset 47..75: Zero padding (29 bytes)
func (jm *JobManager) CreateJob(wp *WorkPackage, cleanJobs bool) (*StratumJob, error) {
	sealHashBytes, err := ParseHexToBytes(wp.SealHash)
	if err != nil {
		return nil, fmt.Errorf("invalid seal hash: %w", err)
	}

	cleanSeed := CleanHex(wp.SeedHash)
	if len(cleanSeed) != 64 {
		return nil, fmt.Errorf("invalid seed hash length: %s", wp.SeedHash)
	}

	// Parse block height
	var height uint64
	cleanHeight := CleanHex(wp.BlockHeight)
	if _, err := fmt.Sscanf(cleanHeight, "%x", &height); err != nil {
		height = 0
	}

	// Parse block 256-bit target
	cleanTarget := CleanHex(wp.BlockTarget)
	targetBytes, err := hex.DecodeString(cleanTarget)
	if err != nil {
		return nil, fmt.Errorf("invalid block target: %w", err)
	}
	blockTarget := new(big.Int).SetBytes(targetBytes)

	// Increment job ID
	jobIDNum := atomic.AddUint64(&jm.jobCounter, 1)
	jobID := fmt.Sprintf("%x", jobIDNum)

	// Assemble 76-byte blob
	blob := make([]byte, 76)
	copy(blob[:32], sealHashBytes[:])

	// Bytes 32..38: zeros
	// Offset 39..42: explicitly zero for XMRig 32-bit nonce injection
	// Offset 43..46: upper 32-bit extraNonce (part of 64-bit Ethereum BlockNonce)
	extraNonce := uint32(jobIDNum & 0xFFFFFFFF)
	binary.LittleEndian.PutUint32(blob[43:47], extraNonce)
	// Offset 47..75: zeros padding

	job := &StratumJob{
		JobID:       jobID,
		Blob:        hex.EncodeToString(blob),
		Target:      jm.targetHex,
		SeedHash:    cleanSeed,
		Height:      height,
		Algo:        "rx/0",
		SealHash:    sealHashBytes,
		BlockTarget: blockTarget,
		ExtraNonce:  extraNonce,
		CleanJobs:   cleanJobs,
	}

	jm.mu.Lock()
	jm.jobs[jobID] = job
	jm.currentJob = job

	// Keep cache size bounded to last 64 jobs
	if len(jm.jobs) > 64 {
		for id := range jm.jobs {
			if id != jobID {
				delete(jm.jobs, id)
				if len(jm.jobs) <= 64 {
					break
				}
			}
		}
	}
	jm.mu.Unlock()

	return job, nil
}

// GetJob retrieves an active job by its string ID.
func (jm *JobManager) GetJob(jobID string) *StratumJob {
	jm.mu.RLock()
	defer jm.mu.RUnlock()
	return jm.jobs[jobID]
}

// CurrentJob returns the most recent work template.
func (jm *JobManager) CurrentJob() *StratumJob {
	jm.mu.RLock()
	defer jm.mu.RUnlock()
	return jm.currentJob
}

// ParseMinerNonce parses 4-byte or 8-byte nonce submitted by XMRig.
func ParseMinerNonce(nonceHex string) (uint64, error) {
	clean := CleanHex(nonceHex)
	bytes, err := hex.DecodeString(clean)
	if err != nil {
		return 0, err
	}

	if len(bytes) == 4 {
		return uint64(binary.LittleEndian.Uint32(bytes)), nil
	} else if len(bytes) == 8 {
		return binary.LittleEndian.Uint64(bytes), nil
	}
	return 0, fmt.Errorf("unexpected nonce length: %d bytes (expected 4 or 8)", len(bytes))
}

// CalculateFullNonce reconstructs the 64-bit Ethereum BlockNonce from upper extraNonce and miner nonce.
func CalculateFullNonce(extraNonce uint32, minerNonce32 uint32) uint64 {
	return (uint64(extraNonce) << 32) | uint64(minerNonce32)
}

// VerifyShare checks whether a submitted hash meets share difficulty and/or full block difficulty.
func (jm *JobManager) VerifyShare(job *StratumJob, nonceHex, resultHex string) (isShareValid bool, isBlockCandidate bool, err error) {
	resultClean := CleanHex(resultHex)
	resultBytes, err := hex.DecodeString(resultClean)
	if err != nil || len(resultBytes) != 32 {
		return false, false, fmt.Errorf("invalid result hash: must be 32 bytes hex")
	}

	var rxHash [32]byte
	copy(rxHash[:], resultBytes)

	// In Monero/RandomX, hash is little-endian; high 32 bits are in bytes 28..31
	hashVal32 := binary.LittleEndian.Uint32(rxHash[28:32])
	if hashVal32 > jm.shareTarget {
		return false, false, fmt.Errorf("share does not meet difficulty target (%08x > %08x)", hashVal32, jm.shareTarget)
	}
	isShareValid = true

	// Check if share meets full network block target (256-bit comparison)
	hashNum := HashToBig(rxHash)
	if job.BlockTarget != nil && hashNum.Cmp(job.BlockTarget) <= 0 {
		isBlockCandidate = true
	}

	return isShareValid, isBlockCandidate, nil
}
