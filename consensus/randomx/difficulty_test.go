package randomx

import (
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params/types/ctypes"
)

func TestDifficultyTargetConversion(t *testing.T) {
	diff := big.NewInt(0x10000)
	target := DifficultyToTarget(diff)
	if target.Sign() <= 0 {
		t.Fatalf("Target should be positive, got %v", target)
	}

	recovDiff := TargetToDifficulty(target)
	diffDelta := new(big.Int).Abs(new(big.Int).Sub(diff, recovDiff))
	if diffDelta.Cmp(big.NewInt(10)) > 0 {
		t.Errorf("Difficulty roundtrip delta too high: diff=%v, recov=%v", diff, recovDiff)
	}
}

func TestLWMAAdjustsUpWhenFast(t *testing.T) {
	chain := NewFakeChainReader()

	// Build a chain of 65 blocks solved in 10 seconds (target is 30 seconds)
	initDiff := big.NewInt(0x20000)
	parent := &types.Header{
		Number:     big.NewInt(0),
		Time:       1000,
		Difficulty: initDiff,
	}
	chain.AddHeader(parent)

	for i := int64(1); i <= 65; i++ {
		header := &types.Header{
			Number:     big.NewInt(i),
			Time:       parent.Time + 10, // Fast: 10 seconds (target 30s)
			Difficulty: initDiff,
			ParentHash: parent.Hash(),
		}
		chain.AddHeader(header)
		parent = header
	}

	nextDiff, err := CalculateDifficulty(chain, parent.Time+10, parent)
	if err != nil {
		t.Fatalf("CalculateDifficulty failed: %v", err)
	}
	if nextDiff.Cmp(initDiff) <= 0 {
		t.Errorf("Expected difficulty to increase when blocks are fast, got %v <= %v", nextDiff, initDiff)
	}

	// Verify clamping: max 133%
	maxAllowed := new(big.Int).Mul(initDiff, big.NewInt(MaxClampUpFactor))
	maxAllowed.Div(maxAllowed, big.NewInt(ClampDenominator))
	if nextDiff.Cmp(maxAllowed) > 0 {
		t.Errorf("Difficulty exceeded upper clamp limit: got %v, max %v", nextDiff, maxAllowed)
	}
}

func TestLWMAAdjustsDownWhenSlow(t *testing.T) {
	chain := NewFakeChainReader()

	// Build a chain of 65 blocks solved in 75 seconds (target is 30 seconds)
	initDiff := big.NewInt(0x40000)
	parent := &types.Header{
		Number:     big.NewInt(0),
		Time:       1000,
		Difficulty: initDiff,
	}
	chain.AddHeader(parent)

	for i := int64(1); i <= 65; i++ {
		header := &types.Header{
			Number:     big.NewInt(i),
			Time:       parent.Time + 75, // Slow: 75 seconds (target 30s)
			Difficulty: initDiff,
			ParentHash: parent.Hash(),
		}
		chain.AddHeader(header)
		parent = header
	}

	nextDiff, err := CalculateDifficulty(chain, parent.Time+75, parent)
	if err != nil {
		t.Fatalf("CalculateDifficulty failed: %v", err)
	}
	if nextDiff.Cmp(initDiff) >= 0 {
		t.Errorf("Expected difficulty to decrease when blocks are slow, got %v >= %v", nextDiff, initDiff)
	}

	// Verify clamping: min 67%
	minAllowed := new(big.Int).Mul(initDiff, big.NewInt(MaxClampDownFactor))
	minAllowed.Div(minAllowed, big.NewInt(ClampDenominator))
	if nextDiff.Cmp(minAllowed) < 0 {
		t.Errorf("Difficulty dropped below lower clamp limit: got %v, min %v", nextDiff, minAllowed)
	}
}

func TestLWMACandidateTimestampIndependence(t *testing.T) {
	chain := NewFakeChainReader()

	initDiff := big.NewInt(0x40000)
	parent := &types.Header{
		Number:     big.NewInt(0),
		Time:       1000,
		Difficulty: initDiff,
	}
	chain.AddHeader(parent)

	for i := int64(1); i <= 30; i++ {
		header := &types.Header{
			Number:     big.NewInt(i),
			Time:       parent.Time + 30,
			Difficulty: initDiff,
			ParentHash: parent.Hash(),
		}
		chain.AddHeader(header)
		parent = header
	}

	// A dishonest miner trying to manipulate their candidate header timestamp
	// must get the exact same difficulty as an honest miner.
	diffHonest, err := CalculateDifficulty(chain, parent.Time+30, parent)
	if err != nil {
		t.Fatalf("CalculateDifficulty failed: %v", err)
	}
	diffManipulated, err := CalculateDifficulty(chain, parent.Time+100000, parent)
	if err != nil {
		t.Fatalf("CalculateDifficulty failed: %v", err)
	}
	if diffHonest.Cmp(diffManipulated) != 0 {
		t.Fatalf("LWMA-1 must be independent of candidate header timestamp: got %v vs %v", diffHonest, diffManipulated)
	}
}

func TestMTP11StrictValidation(t *testing.T) {
	chain := NewFakeChainReader()

	// Build 15 blocks with timestamps spaced by 10s
	var parent *types.Header
	for i := int64(0); i <= 15; i++ {
		h := &types.Header{
			Number: big.NewInt(i),
			Time:   1000 + uint64(i*10),
		}
		if parent != nil {
			h.ParentHash = parent.Hash()
		}
		chain.AddHeader(h)
		parent = h
	}

	mtp := CalculateMTP11(chain, parent)
	// Last 11 blocks are numbers 5..15: median should be number 10 (time = 1100)
	if mtp != 1100 {
		t.Errorf("Expected MTP-11 to be 1100, got %d", mtp)
	}

	now := parent.Time + 10
	// Valid header
	validHeader := &types.Header{
		Number:     big.NewInt(16),
		Time:       parent.Time + 10,
		ParentHash: parent.Hash(),
	}
	if err := VerifyHeaderTime(chain, validHeader, parent, now); err != nil {
		t.Errorf("Valid header failed verification: %v", err)
	}

	// Older than MTP
	oldHeader := &types.Header{
		Number:     big.NewInt(16),
		Time:       1090, // <= 1100
		ParentHash: parent.Hash(),
	}
	if err := VerifyHeaderTime(chain, oldHeader, parent, now); err != ErrOlderBlockTime {
		t.Errorf("Expected ErrOlderBlockTime, got %v", err)
	}

	// Too far in future (> 30 seconds MaxFutureDrift)
	futureHeader := &types.Header{
		Number:     big.NewInt(16),
		Time:       now + 35,
		ParentHash: parent.Hash(),
	}
	if err := VerifyHeaderTime(chain, futureHeader, parent, now); err != consensus.ErrFutureBlock {
		t.Errorf("Expected ErrFutureBlock, got %v", err)
	}
}

func TestLWMAMonotonicTimestampClamp(t *testing.T) {
	chain := NewFakeChainReader()
	initDiff := big.NewInt(0x40000)

	parent := &types.Header{
		Number:     big.NewInt(0),
		Time:       1000,
		Difficulty: initDiff,
	}
	chain.AddHeader(parent)

	// Simulate timestamp oscillation within MTP-11 bounds (alternating +15s and -14s steps)
	baseTime := uint64(1000)
	for i := int64(1); i <= 20; i++ {
		var ts uint64
		if i%2 == 1 {
			ts = baseTime + uint64(i)*2 + 15
		} else {
			ts = baseTime + uint64(i)*2
		}
		h := &types.Header{
			Number:     big.NewInt(i),
			Time:       ts,
			Difficulty: initDiff,
			ParentHash: parent.Hash(),
		}
		chain.AddHeader(h)
		parent = h
	}

	// Because overall elapsed time across 20 blocks is ~40s (<< 20 * 30s = 600s),
	// difficulty must still increase rather than being tricked downward by negative solvetime resets.
	nextDiff, err := CalculateDifficulty(chain, parent.Time+30, parent)
	if err != nil {
		t.Fatalf("CalculateDifficulty failed: %v", err)
	}
	if nextDiff.Cmp(initDiff) <= 0 {
		t.Fatalf("Expected difficulty to increase despite oscillating timestamps, got %v <= %v", nextDiff, initDiff)
	}
}

func TestLWMAMissingAncestorDeterministicError(t *testing.T) {
	chain := NewFakeChainReader()

	initDiff := big.NewInt(0x40000)
	parent := &types.Header{
		Number:     big.NewInt(10),
		Time:       1000 + 10*30,
		Difficulty: initDiff,
	}
	chain.AddHeader(parent)
	// Do not add ancestors 0..9 to chain

	diff, err := CalculateDifficulty(chain, parent.Time+30, parent)
	if err == nil {
		t.Fatalf("Expected CalculateDifficulty to fail when ancestors are missing, got diff %v", diff)
	}
	if !errors.Is(err, consensus.ErrUnknownAncestor) {
		t.Fatalf("Expected consensus.ErrUnknownAncestor, got %v", err)
	}
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
