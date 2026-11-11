package randomx

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params/types/ctypes"
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

	sdb.IntermediateRoot(true)

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

type mockGamificationChain struct {
	headersByNum map[uint64]*types.Header
	current       *types.Header
}

func (m *mockGamificationChain) Config() ctypes.ChainConfigurator { return nil }
func (m *mockGamificationChain) CurrentHeader() *types.Header      { return m.current }
func (m *mockGamificationChain) GetHeader(hash common.Hash, number uint64) *types.Header {
	return m.headersByNum[number]
}
func (m *mockGamificationChain) GetHeaderByNumber(number uint64) *types.Header {
	return m.headersByNum[number]
}
func (m *mockGamificationChain) GetHeaderByHash(hash common.Hash) *types.Header { return nil }
func (m *mockGamificationChain) GetTd(hash common.Hash, number uint64) *big.Int  { return nil }

func TestGetMinerStatsFallback(t *testing.T) {
	miner := common.HexToAddress("0x0544802665bc7c22124348e745a7abaa8420e197")
	other := common.HexToAddress("0x70b2656c4dd9556a6243f9f5de1b07d000000000")

	chain := &mockGamificationChain{
		headersByNum: make(map[uint64]*types.Header),
	}

	// Blocks: #1-#4 other, #5 miner, #6-#7 other, #8-#11 miner (5 blocks total)
	for i := uint64(1); i <= 11; i++ {
		coinbase := other
		if i == 5 || i >= 8 {
			coinbase = miner
		}
		h := &types.Header{
			Number:   big.NewInt(int64(i)),
			Coinbase: coinbase,
		}
		chain.headersByNum[i] = h
		chain.current = h
	}

	api := &RogueAPI{chain: chain}
	stats, err := api.GetMinerStats(miner)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if stats.BlocksMined != 5 {
		t.Errorf("expected 5 blocks mined, got %d", stats.BlocksMined)
	}
	if stats.Level != 1 {
		t.Errorf("expected level 1, got %d", stats.Level)
	}
	if stats.Title != "Recon Runner" {
		t.Errorf("expected 'Recon Runner', got %q", stats.Title)
	}
	if stats.NextLevelXP != 25 {
		t.Errorf("expected next level XP 25, got %d", stats.NextLevelXP)
	}
}
