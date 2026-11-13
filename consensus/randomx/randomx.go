package randomx

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/big"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/consensus/misc"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/metrics"
	"github.com/ethereum/go-ethereum/params/vars"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/ethereum/go-ethereum/trie"
	"golang.org/x/crypto/sha3"
)

const (
	// MaxExtraDataSize is the maximum size in bytes allowed for header.Extra on Rogue Signal (64 bytes).
	MaxExtraDataSize = 64
)

var (
	// ErrInvalidPoW is returned when the RandomX proof of work does not meet the required target.
	ErrInvalidPoW = errors.New("invalid RandomX proof of work")

	// ErrInvalidMixDigest is returned when header.MixDigest does not match the computed RandomX hash.
	ErrInvalidMixDigest = errors.New("invalid RandomX mix digest")

	// ErrUnclesNotAllowed is returned when a block contains uncle headers (disabled on Rogue Signal).
	ErrUnclesNotAllowed = errors.New("uncles not allowed on Rogue Signal")

	// ErrExtraDataTooLong is returned when header.Extra exceeds MaxExtraDataSize (64 bytes).
	ErrExtraDataTooLong = errors.New("header extra-data exceeds 64 bytes")
)

type hashrateTracker struct {
	mu          sync.Mutex
	lastTick    time.Time
	lastHashes  int64
	currentRate float64
	totalHashes atomic.Int64
}

func (ht *hashrateTracker) Mark(attempts int64) {
	if attempts <= 0 {
		return
	}
	ht.totalHashes.Add(attempts)
}

func (ht *hashrateTracker) Reset() {
	ht.mu.Lock()
	defer ht.mu.Unlock()
	ht.lastTick = time.Time{}
	ht.lastHashes = 0
	ht.currentRate = 0
	ht.totalHashes.Store(0)
}

func (ht *hashrateTracker) Rate() float64 {
	ht.mu.Lock()
	defer ht.mu.Unlock()

	now := time.Now()
	if ht.lastTick.IsZero() {
		ht.lastTick = now
		ht.lastHashes = ht.totalHashes.Load()
		return ht.currentRate
	}

	elapsed := now.Sub(ht.lastTick).Seconds()
	if elapsed >= 1.0 {
		currHashes := ht.totalHashes.Load()
		delta := currHashes - ht.lastHashes
		if delta > 0 {
			instant := float64(delta) / elapsed
			if ht.currentRate == 0 {
				ht.currentRate = instant
			} else {
				ht.currentRate = 0.7*instant + 0.3*ht.currentRate
			}
		} else if elapsed > 4.0 {
			ht.currentRate = 0
		}
		ht.lastHashes = currHashes
		ht.lastTick = now
	} else if now.Sub(ht.lastTick).Seconds() > 4.0 {
		ht.currentRate = 0
	}

	return ht.currentRate
}

// RandomX implements the consensus.Engine and consensus.PoW interfaces for Rogue Signal using RandomX (rx/0).
type RandomX struct {
	fastMode bool
	threads  int
	treasury common.Address

	epochs *EpochManager

	tracker  hashrateTracker
	hashrate metrics.Meter

	// Optional test hooks; nil falls back to time.Now().Unix() and crypto/rand.Reader.
	nowFunc    func() uint64
	randReader io.Reader

	sealMu         sync.Mutex
	activeSealDone chan struct{}

	lock sync.RWMutex
}

func (rx *RandomX) currentUnixTime() uint64 {
	if rx.nowFunc != nil {
		return rx.nowFunc()
	}
	return uint64(time.Now().Unix())
}

func (rx *RandomX) randSource() io.Reader {
	if rx.randReader != nil {
		return rx.randReader
	}
	return rand.Reader
}

// Config defines the configuration options for RandomX consensus engine.
type Config struct {
	FastMode bool
	Threads  int
	Treasury common.Address
}

// New creates a full RandomX consensus engine.
func New(cfg Config) *RandomX {
	treasury := cfg.Treasury
	if treasury == (common.Address{}) {
		treasury = DefaultTreasuryAddress
	}

	return &RandomX{
		fastMode: cfg.FastMode,
		threads:  cfg.Threads,
		treasury: treasury,
		epochs:   NewEpochManager(cfg.FastMode),
		hashrate: metrics.NewMeter(),
	}
}

// SetThreads updates the number of mining threads dynamically (-1 to disable local mining).
// When mining is stopped (threads <= 0), all idle pooled RandomX VMs are purged to reclaim RAM.
func (rx *RandomX) SetThreads(threads int) {
	rx.lock.Lock()
	rx.threads = threads
	rx.lock.Unlock()
	if threads <= 0 {
		rx.tracker.Reset()
		rx.epochs.PurgeAllVMs()
	}
}

// SetFastMode dynamically switches between RandomX Light Mode (~256MB) and Fast Mode (~2.08GB).
func (rx *RandomX) SetFastMode(fastMode bool) {
	rx.lock.Lock()
	if rx.fastMode == fastMode {
		rx.lock.Unlock()
		return
	}
	rx.fastMode = fastMode
	rx.lock.Unlock()
	rx.epochs.SetFastMode(fastMode)
}

// IsFastMode returns true if RandomX Fast Mode (~2.08GB) is currently active.
func (rx *RandomX) IsFastMode() bool {
	return rx.epochs.IsFastMode()
}

// headerChainReader wraps a ChainHeaderReader with an in-memory batch of headers
// so ancestors within the batch can be resolved before they are committed to the chain database.
type headerChainReader struct {
	consensus.ChainHeaderReader
	byHash   map[common.Hash]*types.Header
	byNumber map[uint64]*types.Header
}

func (r *headerChainReader) GetHeader(hash common.Hash, number uint64) *types.Header {
	if h, ok := r.byHash[hash]; ok {
		return h
	}
	return r.ChainHeaderReader.GetHeader(hash, number)
}

func (r *headerChainReader) GetHeaderByHash(hash common.Hash) *types.Header {
	if h, ok := r.byHash[hash]; ok {
		return h
	}
	return r.ChainHeaderReader.GetHeaderByHash(hash)
}

func (r *headerChainReader) GetHeaderByNumber(number uint64) *types.Header {
	if h, ok := r.byNumber[number]; ok {
		return h
	}
	return r.ChainHeaderReader.GetHeaderByNumber(number)
}

// Author implements consensus.Engine, returning the block coinbase.
func (rx *RandomX) Author(header *types.Header) (common.Address, error) {
	return header.Coinbase, nil
}

// VerifyHeader checks whether a header conforms to the Rogue Signal consensus rules.
func (rx *RandomX) VerifyHeader(chain consensus.ChainHeaderReader, header *types.Header, seal bool) error {
	if header == nil || header.Number == nil || !header.Number.IsUint64() {
		return consensus.ErrInvalidNumber
	}
	// Genesis block is always valid
	number := header.Number.Uint64()
	if number == 0 {
		return nil
	}

	parent := chain.GetHeader(header.ParentHash, number-1)
	if parent == nil {
		return consensus.ErrUnknownAncestor
	}

	return rx.verifyHeader(chain, header, parent, seal)
}

// verifyHeader performs full header validation given an already resolved parent header.
// Phase 1 executes lightweight consensus rules in microseconds (block number continuity, ExtraData size,
// MTP-11 + 30s drift, LWMA-1 difficulty, GasLimit bounds, and disallowed optional header fields)
// before Phase 2 invokes the CPU-intensive RandomX CGo seal check.
func (rx *RandomX) verifyHeader(chain consensus.ChainHeaderReader, header, parent *types.Header, seal bool) error {
	if header == nil || parent == nil || header.Number == nil || parent.Number == nil || !header.Number.IsUint64() {
		return consensus.ErrInvalidNumber
	}
	// 1. Verify that the block number is parent's +1
	if diff := new(big.Int).Sub(header.Number, parent.Number); diff.Cmp(big.NewInt(1)) != 0 {
		return consensus.ErrInvalidNumber
	}

	// 2. Verify that UncleHash is EmptyUncleHash (uncles are strictly forbidden on Rogue Signal)
	if header.UncleHash != types.EmptyUncleHash {
		return ErrUnclesNotAllowed
	}

	// 3. Verify ExtraData size limit (<= 64 bytes) to prevent header bloat attacks
	if len(header.Extra) > MaxExtraDataSize {
		return fmt.Errorf("%w: have %d bytes, max %d bytes", ErrExtraDataTooLong, len(header.Extra), MaxExtraDataSize)
	}

	// 4 & 5. Compute LWMA-1 difficulty and MTP-11 in a single ancestor traversal,
	// then verify strict timestamp rules (MTP-11 + max 30s future drift) and difficulty.
	if header.Difficulty == nil || header.Difficulty.Sign() <= 0 {
		return fmt.Errorf("invalid difficulty: %v", header.Difficulty)
	}
	expectedDifficulty, mtp, err := CalculateDifficultyAndMTP11(chain, parent)
	if err != nil {
		return err
	}
	if header.Time <= mtp {
		return ErrOlderBlockTime
	}
	now := rx.currentUnixTime()
	if header.Time > now && header.Time-now > MaxFutureDrift {
		return consensus.ErrFutureBlock
	}
	if header.Difficulty.Cmp(expectedDifficulty) != 0 {
		return fmt.Errorf("invalid difficulty: have %v, want %v", header.Difficulty, expectedDifficulty)
	}

	// 6. Verify gas limit bounds and ±1/1024 adjustment delta relative to parent
	if header.GasLimit > vars.MaxGasLimit {
		return fmt.Errorf("invalid gasLimit: have %v, max %v", header.GasLimit, vars.MaxGasLimit)
	}
	if header.GasUsed > header.GasLimit {
		return fmt.Errorf("gas used %d exceeds gas limit %d", header.GasUsed, header.GasLimit)
	}
	if err := misc.VerifyGaslimit(parent.GasLimit, header.GasLimit); err != nil {
		return err
	}

	// 7. Reject optional header fields from inactive forks to prevent block-hash malleability
	if header.BaseFee != nil {
		return fmt.Errorf("invalid baseFee: have %v, expected nil", header.BaseFee)
	}
	if header.WithdrawalsHash != nil {
		return fmt.Errorf("invalid withdrawalsHash: have %x, expected nil", *header.WithdrawalsHash)
	}
	if header.ExcessBlobGas != nil {
		return fmt.Errorf("invalid excessBlobGas: have %d, expected nil", *header.ExcessBlobGas)
	}
	if header.BlobGasUsed != nil {
		return fmt.Errorf("invalid blobGasUsed: have %d, expected nil", *header.BlobGasUsed)
	}
	if header.ParentBeaconRoot != nil {
		return fmt.Errorf("invalid parentBeaconRoot: have %x, expected nil", *header.ParentBeaconRoot)
	}

	// 8. Verify seal if requested (Phase 2)
	if seal {
		if err := rx.VerifySeal(chain, header); err != nil {
			return err
		}
	}

	return nil
}

// VerifyHeaders verifies a batch of headers concurrently using a two-phase fail-fast pipeline:
//   - Phase 1 validates lightweight header fields (ExtraData, MTP-11, LWMA-1 difficulty, GasUsed).
//   - Phase 2 validates RandomX PoW seals in parallel across runtime.NumCPU() worker goroutines.
func (rx *RandomX) VerifyHeaders(chain consensus.ChainHeaderReader, headers []*types.Header, seals []bool) (chan<- struct{}, <-chan error) {
	abort := make(chan struct{})
	results := make(chan error, len(headers))

	if len(headers) == 0 {
		return abort, results
	}

	byHash := make(map[common.Hash]*types.Header, len(headers))
	byNumber := make(map[uint64]*types.Header, len(headers))
	for _, h := range headers {
		if h == nil || h.Number == nil || !h.Number.IsUint64() {
			continue
		}
		byHash[h.Hash()] = h
		byNumber[h.Number.Uint64()] = h
	}
	batchReader := &headerChainReader{
		ChainHeaderReader: chain,
		byHash:            byHash,
		byNumber:          byNumber,
	}

	go func() {
		n := len(headers)
		errs := make([]error, n)
		done := make([]chan struct{}, n)
		for i := range done {
			done[i] = make(chan struct{})
		}

		workers := runtime.NumCPU()
		if workers < 1 {
			workers = 1
		}
		if workers > n {
			workers = n
		}
		sem := make(chan struct{}, workers)

		// Producer: run Phase 1 fail-fast checks and dispatch Phase 2 seal checks to worker pool
		go func() {
			for i, header := range headers {
				select {
				case <-abort:
					close(done[i])
					return
				default:
				}

				if header == nil || header.Number == nil || !header.Number.IsUint64() {
					errs[i] = consensus.ErrInvalidNumber
					close(done[i])
					continue
				}

				number := header.Number.Uint64()
				if number == 0 {
					close(done[i])
					continue
				}

				var parent *types.Header
				if i > 0 && headers[i-1].Hash() == header.ParentHash {
					parent = headers[i-1]
				} else {
					parent = batchReader.GetHeader(header.ParentHash, number-1)
				}

				if parent == nil {
					errs[i] = consensus.ErrUnknownAncestor
					close(done[i])
					continue
				}

				// Phase 1: Fast lightweight validation (microseconds)
				if err := rx.verifyHeader(batchReader, header, parent, false); err != nil {
					errs[i] = err
					close(done[i])
					continue
				}

				// If seal verification is not requested for this header, we are done
				if i >= len(seals) || !seals[i] {
					close(done[i])
					continue
				}

				// Phase 2: Parallel RandomX PoW seal verification
				select {
				case <-abort:
					close(done[i])
					return
				case sem <- struct{}{}:
				}

				go func(idx int, h *types.Header) {
					defer func() {
						<-sem
						close(done[idx])
					}()
					select {
					case <-abort:
						return
					default:
					}
					errs[idx] = rx.VerifySeal(batchReader, h)
				}(i, header)
			}
		}()

		// Consumer: emit verification results in strict header order
		for i := 0; i < n; i++ {
			select {
			case <-abort:
				return
			case <-done[i]:
			}
			select {
			case <-abort:
				return
			case results <- errs[i]:
			}
		}
	}()

	return abort, results
}

// VerifyUncles enforces that Rogue Signal strictly forbids uncle blocks (Bitcoin model).
func (rx *RandomX) VerifyUncles(chain consensus.ChainReader, block *types.Block) error {
	if len(block.Uncles()) > 0 {
		return ErrUnclesNotAllowed
	}
	return nil
}

// Prepare initializes consensus fields on the header before execution.
func (rx *RandomX) Prepare(chain consensus.ChainHeaderReader, header *types.Header) error {
	parent := chain.GetHeader(header.ParentHash, header.Number.Uint64()-1)
	if parent == nil {
		return consensus.ErrUnknownAncestor
	}

	// Ensure header.Time strictly exceeds MTP-11(parent) even if recent blocks had out-of-order timestamps
	if mtp := CalculateMTP11(chain, parent); header.Time <= mtp {
		header.Time = mtp + 1
	}
	if now := rx.currentUnixTime(); header.Time > now+MaxFutureDrift {
		log.Warn("RandomX Prepare: MTP-11 forced block timestamp beyond MaxFutureDrift; block may be rejected until clock catches up",
			"number", header.Number,
			"headerTime", header.Time,
			"now", now,
			"maxFutureDrift", MaxFutureDrift,
		)
	}

	diff, err := CalculateDifficulty(chain, header.Time, parent)
	if err != nil {
		return err
	}
	header.Difficulty = diff
	header.UncleHash = types.EmptyUncleHash

	// Trigger background precomputation of next epoch cache if inside seed lag window
	rx.epochs.PrecomputeNextEpoch(header.Number.Uint64(), chain)
	return nil
}

// Finalize credits mining rewards to the miner coinbase (95%) and treasury (5%).
func (rx *RandomX) Finalize(chain consensus.ChainHeaderReader, header *types.Header, state *state.StateDB, txs []*types.Transaction,
	uncles []*types.Header, withdrawals []*types.Withdrawal) {
	AccumulateRewards(state, header, rx.treasury)
}

// FinalizeAndAssemble credits rewards and constructs the final block.
func (rx *RandomX) FinalizeAndAssemble(chain consensus.ChainHeaderReader, header *types.Header, state *state.StateDB, txs []*types.Transaction,
	uncles []*types.Header, receipts []*types.Receipt, withdrawals []*types.Withdrawal) (*types.Block, error) {

	if len(uncles) > 0 {
		return nil, ErrUnclesNotAllowed
	}

	rx.Finalize(chain, header, state, txs, uncles, withdrawals)
	header.Root = state.IntermediateRoot(true)

	// Assemble block without uncles using StackTrie hasher for tx and receipt trees
	return types.NewBlock(header, txs, nil, receipts, trie.NewStackTrie(nil)), nil
}

// SealHash calculates the pre-seal hash of the block header using Ethereum RLP encoding.
func (rx *RandomX) SealHash(header *types.Header) common.Hash {
	hasher := sha3.NewLegacyKeccak256()

	enc := []interface{}{
		header.ParentHash,
		header.UncleHash,
		header.Coinbase,
		header.Root,
		header.TxHash,
		header.ReceiptHash,
		header.Bloom,
		header.Difficulty,
		header.Number,
		header.GasLimit,
		header.GasUsed,
		header.Time,
		header.Extra,
	}
	if header.BaseFee != nil {
		enc = append(enc, header.BaseFee)
	}
	if header.WithdrawalsHash != nil {
		enc = append(enc, header.WithdrawalsHash)
	}
	if header.BlobGasUsed != nil {
		enc = append(enc, header.BlobGasUsed)
	}
	if header.ExcessBlobGas != nil {
		enc = append(enc, header.ExcessBlobGas)
	}
	if header.ParentBeaconRoot != nil {
		enc = append(enc, header.ParentBeaconRoot)
	}

	rlp.Encode(hasher, enc)
	var hash common.Hash
	hasher.Sum(hash[:0])
	return hash
}

// MiningBlobSize defines the canonical 76-byte RandomX mining blob (Monero/rx0 standard).
const MiningBlobSize = 76

// MiningInput serializes SealHash(header) and the 64-bit Nonce into a standard 76-byte RandomX mining blob.
// Offset 0..31: SealHash (32 bytes)
// Offset 32..38: Extra nonce / reserved (7 bytes)
// Offset 39..46: Nonce uint64 (8 bytes, little-endian; bytes 39..42 match standard XMRig 32-bit nonce)
// Offset 47..75: Padding (29 bytes)
func MiningInput(sealHash common.Hash, nonce uint64) []byte {
	input := make([]byte, MiningBlobSize)
	copy(input[:32], sealHash.Bytes())
	binary.LittleEndian.PutUint64(input[39:], nonce)
	return input
}

// VerifySeal checks if the header contains a valid RandomX proof of work.
func (rx *RandomX) VerifySeal(chain consensus.ChainHeaderReader, header *types.Header) error {
	if header == nil || header.Number == nil || !header.Number.IsUint64() {
		return consensus.ErrInvalidNumber
	}
	number := header.Number.Uint64()
	if number == 0 {
		return nil // Genesis
	}
	if header.Difficulty == nil || header.Difficulty.Sign() <= 0 {
		return ErrInvalidPoW
	}

	// Fast pre-check: verify MixDigest meets the difficulty target before spending
	// heavy CPU cycles computing the CGo RandomX hash. If MixDigest > Target, it is impossible
	// for this seal to be valid regardless of the hash calculation.
	targetBE := TargetToBytes32(DifficultyToTarget(header.Difficulty))
	if !HashMeetsTarget(header.MixDigest, targetBE) {
		return ErrInvalidPoW
	}

	// Use Light Mode (256MB) when verifying historical-epoch headers on a Fast Mode node
	// to avoid allocating a 2.08GB dataset or evicting active mining epochs.
	var (
		ctx *RxContext
		err error
	)
	useLightForPastEpoch := false
	if rx.IsFastMode() && chain != nil {
		if head := chain.CurrentHeader(); head != nil && head.Number != nil && head.Number.IsUint64() {
			if EpochNumber(number) < EpochNumber(head.Number.Uint64()) {
				useLightForPastEpoch = true
			}
		}
	}
	if useLightForPastEpoch {
		ctx, err = rx.epochs.GetLightContextForHeader(header, chain)
	} else {
		ctx, err = rx.epochs.GetContextForHeader(header, chain)
	}
	if err != nil {
		return fmt.Errorf("failed to get RandomX context for block %d: %w", number, err)
	}
	defer ctx.DecRef()

	sealHash := rx.SealHash(header)
	input := MiningInput(sealHash, header.Nonce.Uint64())

	rxHash, err := ctx.CalculateHash(input)
	if err != nil {
		return fmt.Errorf("RandomX hash calculation failed: %w", err)
	}

	// Verify MixDigest matches the RandomX hash output (target check already passed above)
	if !bytes.Equal(header.MixDigest.Bytes(), rxHash[:]) {
		return ErrInvalidMixDigest
	}

	return nil
}

// Seal implements the consensus.Engine interface, coordinating internal node CPU miner threads.
func (rx *RandomX) Seal(chain consensus.ChainHeaderReader, block *types.Block, results chan<- *types.Block, stop <-chan struct{}) error {
	header := block.Header()
	number := header.Number.Uint64()

	rx.lock.RLock()
	threads := rx.threads
	rx.lock.RUnlock()

	// If local CPU mining is disabled, return immediately
	if threads <= 0 {
		return nil
	}

	targetBE := TargetToBytes32(DifficultyToTarget(header.Difficulty))
	sealHash := rx.SealHash(header)

	var (
		baseNonceBuf [8]byte
		baseNonce    uint64
	)
	if _, err := io.ReadFull(rx.randSource(), baseNonceBuf[:]); err != nil {
		baseNonce = uint64(time.Now().UnixNano()) ^ header.Number.Uint64()
		log.Warn("RandomX Seal: crypto/rand failed, falling back to time-based nonce",
			"err", err,
			"block", header.Number,
			"fallbackNonce", baseNonce,
		)
	} else {
		baseNonce = binary.LittleEndian.Uint64(baseNonceBuf[:])
	}

	var (
		foundNonce uint64
		foundHash  [32]byte
		found      = make(chan struct{})
		abortOnce  sync.Once
	)

	rx.sealMu.Lock()
	prevDone := rx.activeSealDone
	currDone := make(chan struct{})
	rx.activeSealDone = currDone
	rx.sealMu.Unlock()

	// Mining (including epoch context/dataset initialization and VM prewarming) must run asynchronously
	// to prevent blocking the worker's taskLoop, allowing immediate cancellation on stop or new heads.
	go func() {
		defer close(currDone)

		// Wait for any previous sealing session to release its pooled VMs before starting
		// a new session, preventing transient VM pool over-allocation on rapid recommits.
		if prevDone != nil {
			select {
			case <-prevDone:
			case <-stop:
				return
			}
		}

		// If MTP-11 forced header.Time beyond MaxFutureDrift, wait until the clock enters
		// the valid drift window instead of wasting CPU mining a block that peers will reject.
		if now := rx.currentUnixTime(); header.Time > now+MaxFutureDrift {
			waitDur := time.Duration(header.Time-(now+MaxFutureDrift)) * time.Second
			timer := time.NewTimer(waitDur)
			select {
			case <-stop:
				timer.Stop()
				return
			case <-timer.C:
			}
		}

		select {
		case <-stop:
			return
		default:
		}

		ctx, err := rx.epochs.GetContextForHeader(header, chain)
		if err != nil {
			log.Error("RandomX Seal: failed to get RandomX context for mining", "number", number, "err", err)
			return
		}
		defer ctx.DecRef()

		select {
		case <-stop:
			return
		default:
		}

		// Pre-warm pooled VMs in parallel using GOMAXPROCS before spawning mining threads
		ctx.PrewarmVMs(threads)

		select {
		case <-stop:
			return
		default:
		}

		var wg sync.WaitGroup
		for threadID := 0; threadID < threads; threadID++ {
			wg.Add(1)
			go func(tid int) {
				defer wg.Done()

				// Acquire a thread-local VM for the entire mining session (zero pool/lock contention per hash)
				vm, err := ctx.AcquireVM()
				if err != nil {
					log.Debug("RandomX seal worker failed to acquire VM", "tid", tid, "number", number, "err", err)
					return
				}
				defer ctx.ReleaseVM(vm)

				// Pre-allocate the 76-byte RandomX mining blob once per worker goroutine
				var input [MiningBlobSize]byte
				copy(input[:32], sealHash.Bytes())

				// Each thread starts searching from an independent cryptographically seeded nonce offset
				nonce := baseNonce + uint64(tid)*0x100000000
				var attempts int64

				for {
					select {
					case <-stop:
						rx.markAttempts(attempts)
						return
					case <-found:
						rx.markAttempts(attempts)
						return
					default:
					}

					attempts++
					if (attempts % 64) == 0 {
						rx.markAttempts(attempts)
						attempts = 0
					}

					binary.LittleEndian.PutUint64(input[39:], nonce)
					rxHash, err := ctx.CalculateHashWithVM(vm, input[:])
					if err != nil {
						rx.markAttempts(attempts)
						log.Debug("RandomX seal worker stopped due to context error", "tid", tid, "number", number, "err", err)
						return
					}

					if HashMeetsTarget(rxHash, targetBE) {
						rx.markAttempts(attempts)
						abortOnce.Do(func() {
							foundNonce = nonce
							foundHash = rxHash
							close(found)
						})
						return
					}

					nonce++
				}
			}(threadID)
		}

		select {
		case <-stop:
			wg.Wait()
		case <-found:
			wg.Wait()
			// Assemble winning header
			header.Nonce = types.EncodeNonce(foundNonce)
			header.MixDigest = common.BytesToHash(foundHash[:])

			log.Info("Successfully sealed block with RandomX",
				"number", header.Number,
				"difficulty", header.Difficulty,
				"nonce", foundNonce,
				"hash", header.MixDigest.Hex(),
			)

			select {
			case results <- block.WithSeal(header):
			case <-stop:
			}
		}
	}()

	return nil
}

func (rx *RandomX) markAttempts(attempts int64) {
	if attempts <= 0 {
		return
	}
	rx.tracker.Mark(attempts)
	rx.hashrate.Mark(attempts)
}

// CalcDifficulty returns the difficulty of the next block.
func (rx *RandomX) CalcDifficulty(chain consensus.ChainHeaderReader, time uint64, parent *types.Header) *big.Int {
	diff, err := CalculateDifficulty(chain, time, parent)
	if err != nil {
		var (
			parentNum  uint64
			parentHash common.Hash
			fallback   *big.Int
		)
		if parent != nil {
			if parent.Number != nil {
				parentNum = parent.Number.Uint64()
			}
			parentHash = parent.Hash()
			if parent.Difficulty != nil {
				fallback = new(big.Int).Set(parent.Difficulty)
			}
		}
		if fallback == nil {
			fallback = new(big.Int).Set(MinDifficulty)
		}
		log.Warn("RandomX CalcDifficulty: failed to compute LWMA-1 difficulty, using fallback",
			"err", err,
			"parentNumber", parentNum,
			"parentHash", parentHash,
			"time", time,
			"fallback", fallback,
		)
		return fallback
	}
	return diff
}

// Hashrate implements consensus.PoW, returning the current mining hashrate of the internal CPU miner.
func (rx *RandomX) Hashrate() float64 {
	rx.lock.RLock()
	threads := rx.threads
	rx.lock.RUnlock()
	if threads <= 0 {
		return 0
	}

	localRate := rx.tracker.Rate()
	if localRate == 0 {
		ms := rx.hashrate.Snapshot()
		localRate = ms.Rate1()
	}
	return localRate
}

// APIs returns consensus RPC APIs for eth (read-only/stubs) and randomx (diagnostics/controls) namespaces.
func (rx *RandomX) APIs(chain consensus.ChainHeaderReader) []rpc.API {
	return []rpc.API{
		{
			Namespace: "eth",
			Service:   &EthAPI{rx: rx},
		},
		{
			Namespace: "randomx",
			Service:   &API{rx: rx, chain: chain},
		},
	}
}

// Close releases all RandomX caches and memory.
func (rx *RandomX) Close() error {
	rx.epochs.Close()
	return nil
}
