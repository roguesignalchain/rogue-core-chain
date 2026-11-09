package randomx

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/core/types"
)

func TestEpochCalculations(t *testing.T) {
	tests := []struct {
		block    uint64
		expected uint64
	}{
		{0, 0},
		{1, 0},
		{2047, 0},
		{2048, 1},
		{2049, 1},
		{4095, 1},
		{4096, 2},
	}

	for _, tc := range tests {
		if ep := EpochNumber(tc.block); ep != tc.expected {
			t.Errorf("EpochNumber(%d) = %d, expected %d", tc.block, ep, tc.expected)
		}
	}
}

func TestSeedBlockCalculations(t *testing.T) {
	tests := []struct {
		epoch    uint64
		expected uint64
	}{
		{0, 0},
		{1, 1984}, // 2048 - 64
		{2, 4032}, // 4096 - 64
		{3, 6080}, // 6144 - 64
	}

	for _, tc := range tests {
		if sb := SeedBlockNumber(tc.epoch); sb != tc.expected {
			t.Errorf("SeedBlockNumber(%d) = %d, expected %d", tc.epoch, sb, tc.expected)
		}
	}
}

func TestEpochContextResolution(t *testing.T) {
	chain := NewFakeChainReader()

	// Add genesis block
	genesis := &types.Header{
		Number: big.NewInt(0),
		Time:   1000,
	}
	chain.AddHeader(genesis)

	em := NewEpochManager(false) // Light mode
	defer em.Close()

	ctx, err := em.GetContext(0, chain)
	if err != nil {
		t.Fatalf("Failed to get context for block 0: %v", err)
	}
	if ctx == nil {
		t.Fatal("Context for block 0 is nil")
	}

	// Hash calculation with epoch 0 context
	hash, err := ctx.CalculateHash([]byte("Rogue Signal Test"))
	if err != nil {
		t.Fatalf("Hash calculation failed: %v", err)
	}
	if len(hash) != 32 {
		t.Fatalf("Expected 32-byte hash, got %d", len(hash))
	}
}

func TestEpochSeedReorgInvalidation(t *testing.T) {
	chain := NewFakeChainReader()

	// Seed block for epoch 1 is block 1984 (2048 - 64)
	seedBlockA := &types.Header{
		Number: big.NewInt(1984),
		Time:   10000,
		Extra:  []byte("branch-A"),
	}
	chain.AddHeader(seedBlockA)

	em := NewEpochManager(false)
	defer em.Close()

	seed1, err := em.GetSeed(1, chain)
	if err != nil {
		t.Fatalf("GetSeed(1) failed on branch A: %v", err)
	}
	if seed1 != seedBlockA.Hash() {
		t.Fatalf("Expected seed %s, got %s", seedBlockA.Hash().Hex(), seed1.Hex())
	}

	// Simulate a legitimate chain reorganization replacing block 1984 with branch B
	chainReorg := NewFakeChainReader()
	seedBlockB := &types.Header{
		Number: big.NewInt(1984),
		Time:   10005,
		Extra:  []byte("branch-B"),
	}
	chainReorg.AddHeader(seedBlockB)

	seed2, err := em.GetSeed(1, chainReorg)
	if err != nil {
		t.Fatalf("GetSeed(1) failed after reorg to branch B: %v", err)
	}
	if seed2 != seedBlockB.Hash() {
		t.Fatalf("EpochManager did not invalidate stale seed after reorg: got %s, want %s", seed2.Hex(), seedBlockB.Hash().Hex())
	}
}
