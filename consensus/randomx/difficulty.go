package randomx

import (
	"errors"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params/types/ctypes"
)

const (
	// DefaultLWMAWindow is the number of historical blocks used for LWMA-1 difficulty adjustment (60 blocks = ~30 minutes at 30s/block).
	DefaultLWMAWindow = 60

	// DefaultTargetBlockTime is the target block interval in seconds for Rogue Signal (30 seconds).
	DefaultTargetBlockTime = 30

	// MTPWindow is the number of ancestor blocks used to compute the Past Median Time (MTP-11).
	MTPWindow = 11

	// MaxFutureDrift is the maximum allowed future timestamp drift in seconds (15 seconds).
	MaxFutureDrift = 15

	// MaxClampUpFactor is the maximum multiplier for difficulty increase per block (133%).
	MaxClampUpFactor = 133

	// MaxClampDownFactor is the minimum multiplier for difficulty decrease per block (67%).
	MaxClampDownFactor = 67

	// ClampDenominator is the base for clamp percentages.
	ClampDenominator = 100
)

var (
	// Max256 is 2^256 - 1, the maximum 256-bit unsigned integer.
	Max256 = new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1))

	// MinDifficulty is the absolute minimum difficulty threshold for Rogue Signal.
	MinDifficulty = big.NewInt(0x10000)

	// ErrOlderBlockTime is returned when a block's timestamp is <= Past Median Time (MTP-11).
	ErrOlderBlockTime = errors.New("timestamp older than or equal to past median time")
)

// DifficultyToTarget converts a difficulty value into a 256-bit mining target.
// Target = (2^256 - 1) / Difficulty
func DifficultyToTarget(diff *big.Int) *big.Int {
	if diff == nil || diff.Sign() <= 0 {
		return new(big.Int).Set(Max256)
	}
	target := new(big.Int).Div(Max256, diff)
	if target.Sign() <= 0 {
		return big.NewInt(1)
	}
	return target
}

// TargetToDifficulty converts a 256-bit target into difficulty.
// Difficulty = (2^256 - 1) / Target
func TargetToDifficulty(target *big.Int) *big.Int {
	if target == nil || target.Sign() <= 0 {
		return new(big.Int).Set(Max256)
	}
	diff := new(big.Int).Div(Max256, target)
	if diff.Cmp(MinDifficulty) < 0 {
		return new(big.Int).Set(MinDifficulty)
	}
	return diff
}

// HashToBig converts a 32-byte hash (little-endian byte array as produced by RandomX)
// into a big.Int for target comparison.
func HashToBig(hash [32]byte) *big.Int {
	reversed := make([]byte, 32)
	for i := 0; i < 32; i++ {
		reversed[i] = hash[31-i]
	}
	return new(big.Int).SetBytes(reversed)
}

// CalculateDifficulty implements the LWMA-1 (Linear Weighted Moving Average)
// difficulty adjustment algorithm for Rogue Signal.
//
// Formula:
//
//	NextTarget = (AvgTarget * SolvetimeWeightSum) / (k * T * SolvetimeSum)
//	where SolvetimeWeightSum = sum_{i=1}^{k} (i * solvetime_i)
//	k = min(window, blockNumber)
func CalculateDifficulty(chain consensus.ChainHeaderReader, time uint64, parent *types.Header) *big.Int {
	if parent == nil {
		return new(big.Int).Set(MinDifficulty)
	}

	parentNum := parent.Number.Uint64()
	if parentNum == 0 {
		return new(big.Int).Set(parent.Difficulty)
	}

	targetTime := uint64(DefaultTargetBlockTime)
	window := uint64(DefaultLWMAWindow)

	// Determine effective history window k (number of solve intervals available back to Genesis)
	k := window
	if parentNum < window {
		k = parentNum
	}

	// Fetch headers from parent back k blocks
	headers := make([]*types.Header, k+1)
	curr := parent
	headers[k] = curr

	for i := int(k) - 1; i >= 0; i-- {
		p := chain.GetHeader(curr.ParentHash, curr.Number.Uint64()-1)
		if p == nil {
			// If not enough ancestors are found, fall back to parent difficulty
			return new(big.Int).Set(parent.Difficulty)
		}
		headers[i] = p
		curr = p
	}

	// Calculate weighted solve times and average target over the window using monotonic timestamps
	// to prevent out-of-order timestamp oscillation attacks under MTP-11.
	var (
		sumTarget     = new(big.Int)
		weightedTimes = int64(0)
		sumSolveTimes = int64(0)
		prevTime      = int64(headers[0].Time)
	)

	for i := uint64(1); i <= k; i++ {
		hCurr := headers[i]

		currTime := int64(hCurr.Time)
		if currTime <= prevTime {
			currTime = prevTime + 1
		}
		solvetime := currTime - prevTime
		prevTime = currTime

		// Cap extreme solvetimes to prevent malicious manipulation (max 6 * targetTime)
		if solvetime > int64(6*targetTime) {
			solvetime = int64(6 * targetTime)
		}

		sumSolveTimes += solvetime
		weightedTimes += int64(i) * solvetime

		target := DifficultyToTarget(hCurr.Difficulty)
		sumTarget.Add(sumTarget, target)
	}

	// avgTarget = sumTarget / k
	avgTarget := new(big.Int).Div(sumTarget, new(big.Int).SetUint64(k))

	// denominator = k * (k + 1) * targetTime / 2
	denominator := (k * (k + 1) * targetTime) / 2
	if denominator == 0 {
		return new(big.Int).Set(parent.Difficulty)
	}

	// nextTarget = (avgTarget * weightedTimes) / denominator
	nextTarget := new(big.Int).Mul(avgTarget, big.NewInt(weightedTimes))
	nextTarget.Div(nextTarget, big.NewInt(int64(denominator)))

	if nextTarget.Sign() <= 0 {
		nextTarget = big.NewInt(1)
	}

	// Convert nextTarget back to difficulty
	nextDiff := TargetToDifficulty(nextTarget)

	// Apply target clamping: limit adjustment to ±33% per block compared to parent
	clampUp := new(big.Int).Mul(parent.Difficulty, big.NewInt(MaxClampUpFactor))
	clampUp.Div(clampUp, big.NewInt(ClampDenominator))

	clampDown := new(big.Int).Mul(parent.Difficulty, big.NewInt(MaxClampDownFactor))
	clampDown.Div(clampDown, big.NewInt(ClampDenominator))

	if nextDiff.Cmp(clampUp) > 0 {
		nextDiff.Set(clampUp)
	} else if nextDiff.Cmp(clampDown) < 0 {
		nextDiff.Set(clampDown)
	}

	if nextDiff.Cmp(MinDifficulty) < 0 {
		nextDiff.Set(MinDifficulty)
	}

	return nextDiff
}

// CalculateMTP11 calculates the Past Median Time (MTP-11) for strict header validation.
// A valid block timestamp must be strictly greater than the median of the past 11 blocks.
func CalculateMTP11(chain consensus.ChainHeaderReader, parent *types.Header) uint64 {
	if parent == nil {
		return 0
	}

	count := MTPWindow
	times := make([]uint64, 0, count)

	curr := parent
	for curr != nil && len(times) < count {
		times = append(times, curr.Time)
		if curr.Number.Uint64() == 0 {
			break
		}
		curr = chain.GetHeader(curr.ParentHash, curr.Number.Uint64()-1)
	}

	if len(times) == 0 {
		return parent.Time
	}

	// Simple insertion sort to find median
	for i := 1; i < len(times); i++ {
		key := times[i]
		j := i - 1
		for j >= 0 && times[j] > key {
			times[j+1] = times[j]
			j--
		}
		times[j+1] = key
	}

	// Median index
	return times[len(times)/2]
}

// VerifyHeaderTime checks that:
// 1. Header.Time > MTP11(parent)
// 2. Header.Time <= now + MaxFutureDrift (15 seconds)
func VerifyHeaderTime(chain consensus.ChainHeaderReader, header *types.Header, parent *types.Header, now uint64) error {
	mtp := CalculateMTP11(chain, parent)
	if header.Time <= mtp {
		return ErrOlderBlockTime
	}

	if header.Time > now+MaxFutureDrift {
		return consensus.ErrFutureBlock
	}

	return nil
}

// FakeChainReader is a mock helper for unit tests that provides headers by hash/number.
type FakeChainReader struct {
	headers map[common.Hash]*types.Header
}

func NewFakeChainReader() *FakeChainReader {
	return &FakeChainReader{headers: make(map[common.Hash]*types.Header)}
}

func (f *FakeChainReader) AddHeader(h *types.Header) {
	f.headers[h.Hash()] = h
}

func (f *FakeChainReader) Config() ctypes.ChainConfigurator {
	return nil
}

func (f *FakeChainReader) CurrentHeader() *types.Header {
	return nil
}

func (f *FakeChainReader) GetHeader(hash common.Hash, number uint64) *types.Header {
	return f.headers[hash]
}

func (f *FakeChainReader) GetHeaderByNumber(number uint64) *types.Header {
	for _, h := range f.headers {
		if h.Number.Uint64() == number {
			return h
		}
	}
	return nil
}

func (f *FakeChainReader) GetHeaderByHash(hash common.Hash) *types.Header {
	return f.headers[hash]
}

func (f *FakeChainReader) GetTd(hash common.Hash, number uint64) *big.Int {
	return big.NewInt(1)
}
