package randomx

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/core/types"
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

	// Build a chain of 65 blocks solved in 5 seconds (target is 10 seconds)
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
			Time:       parent.Time + 5, // Fast: 5 seconds
			Difficulty: initDiff,
			ParentHash: parent.Hash(),
		}
		chain.AddHeader(header)
		parent = header
	}

	nextDiff := CalculateDifficulty(chain, parent.Time+5, parent)
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

	// Build a chain of 65 blocks solved in 25 seconds (target is 10 seconds)
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
			Time:       parent.Time + 25, // Slow: 25 seconds
			Difficulty: initDiff,
			ParentHash: parent.Hash(),
		}
		chain.AddHeader(header)
		parent = header
	}

	nextDiff := CalculateDifficulty(chain, parent.Time+25, parent)
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

	// Too far in future (> 15 seconds)
	futureHeader := &types.Header{
		Number:     big.NewInt(16),
		Time:       now + 20,
		ParentHash: parent.Hash(),
	}
	if err := VerifyHeaderTime(chain, futureHeader, parent, now); err != consensus.ErrFutureBlock {
		t.Errorf("Expected ErrFutureBlock, got %v", err)
	}
}
