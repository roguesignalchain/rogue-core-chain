package randomx

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
)

func TestCalculateMinerStats(t *testing.T) {
	testAddr := common.HexToAddress("0x1234567890123456789012345678901234567890")

	tests := []struct {
		blocks        uint64
		expectedLevel uint32
		expectedTitle string
		expectedNext  uint64
	}{
		{0, 0, "Unranked Runner", 1},
		{1, 1, "Recon Runner", 25},
		{13, 1, "Recon Runner", 25},
		{24, 1, "Recon Runner", 25},
		{25, 2, "Signal Scraper", 100},
		{99, 2, "Signal Scraper", 100},
		{100, 3, "Hash Warden", 500},
		{499, 3, "Hash Warden", 500},
		{500, 4, "Cipher Architect", 2500},
		{2499, 4, "Cipher Architect", 2500},
		{2500, 5, "Apex Operator", 10000},
		{9999, 5, "Apex Operator", 10000},
		{10000, 6, "Rogue Singularity", 10000},
		{50000, 6, "Rogue Singularity", 10000},
	}

	for _, tt := range tests {
		stats := CalculateMinerStats(testAddr, tt.blocks)
		if stats.Level != tt.expectedLevel {
			t.Errorf("Blocks %d: expected level %d, got %d", tt.blocks, tt.expectedLevel, stats.Level)
		}
		if stats.Title != tt.expectedTitle {
			t.Errorf("Blocks %d: expected title %q, got %q", tt.blocks, tt.expectedTitle, stats.Title)
		}
		if stats.NextLevelXP != tt.expectedNext {
			t.Errorf("Blocks %d: expected nextLevelXP %d, got %d", tt.blocks, tt.expectedNext, stats.NextLevelXP)
		}
		if stats.ProgressPercent < 0.0 || stats.ProgressPercent > 100.0 {
			t.Errorf("Blocks %d: progressPercent %f out of bounds [0, 100]", tt.blocks, stats.ProgressPercent)
		}
	}
}

func TestIncrementMinerBlockCount(t *testing.T) {
	db := rawdb.NewMemoryDatabase()
	sdb, err := state.New(types.EmptyRootHash, state.NewDatabase(db), nil)
	if err != nil {
		t.Fatalf("failed to create state db: %v", err)
	}

	miner := common.HexToAddress("0xdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef")

	// Initially zero
	count := GetMinerBlockCount(sdb, miner)
	if count != 0 {
		t.Fatalf("expected initial count 0, got %d", count)
	}

	// Increment 3 times
	IncrementMinerBlockCount(sdb, miner)
	IncrementMinerBlockCount(sdb, miner)
	IncrementMinerBlockCount(sdb, miner)

	count = GetMinerBlockCount(sdb, miner)
	if count != 3 {
		t.Fatalf("expected count 3, got %d", count)
	}

	stats := CalculateMinerStats(miner, count)
	if stats.Level != 1 {
		t.Fatalf("expected level 1, got %d", stats.Level)
	}
	if stats.Title != "Recon Runner" {
		t.Fatalf("expected title 'Recon Runner', got %q", stats.Title)
	}
}
