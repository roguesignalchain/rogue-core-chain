package randomx

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/params/types/ctypes"
	"github.com/ethereum/go-ethereum/triedb"
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

type mockGamificationStateChain struct {
	state *state.StateDB
}

func (m *mockGamificationStateChain) Config() ctypes.ChainConfigurator { return nil }
func (m *mockGamificationStateChain) CurrentHeader() *types.Header      { return nil }
func (m *mockGamificationStateChain) GetHeader(hash common.Hash, number uint64) *types.Header {
	return nil
}
func (m *mockGamificationStateChain) GetHeaderByNumber(number uint64) *types.Header { return nil }
func (m *mockGamificationStateChain) GetHeaderByHash(hash common.Hash) *types.Header { return nil }
func (m *mockGamificationStateChain) GetTd(hash common.Hash, number uint64) *big.Int  { return nil }
func (m *mockGamificationStateChain) State() (*state.StateDB, error)                  { return m.state, nil }

func TestGetMinerStatsFromState(t *testing.T) {
	db := rawdb.NewMemoryDatabase()
	sdb := state.NewDatabase(db)
	st, err := state.New(common.Hash{}, sdb, nil)
	if err != nil {
		t.Fatalf("failed to create state: %v", err)
	}

	miner := common.HexToAddress("0x0544802665bc7c22124348e745a7abaa8420e197")
	for i := 0; i < 5; i++ {
		IncrementMinerBlockCount(st, miner)
	}

	chain := &mockGamificationStateChain{state: st}
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

func TestGamificationSurvivesEIP158FinaliseAndCommit(t *testing.T) {
	db := rawdb.NewMemoryDatabase()
	trieDB := state.NewDatabase(db)
	sdb, err := state.New(types.EmptyRootHash, trieDB, nil)
	if err != nil {
		t.Fatalf("failed to create state db: %v", err)
	}

	miner := common.HexToAddress("0xdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
	count := IncrementMinerBlockCount(sdb, miner)
	if count != 1 {
		t.Fatalf("expected count 1, got %d", count)
	}

	// Simulate EIP-158 empty account deletion during block finalisation.
	sdb.Finalise(true)

	if !sdb.Exist(GamificationSystemAddress) {
		t.Fatalf("gamification system address %s was deleted by Finalise(true)", GamificationSystemAddress.Hex())
	}
	if nonce := sdb.GetNonce(GamificationSystemAddress); nonce != 1 {
		t.Fatalf("expected nonce 1 on %s, got %d", GamificationSystemAddress.Hex(), nonce)
	}

	root, err := sdb.Commit(1, true)
	if err != nil {
		t.Fatalf("failed to commit state: %v", err)
	}
	if err := trieDB.TrieDB().Commit(root, false); err != nil {
		t.Fatalf("failed to commit trie db: %v", err)
	}

	reopened, err := state.New(root, trieDB, nil)
	if err != nil {
		t.Fatalf("failed to reopen state from committed root %s: %v", root.Hex(), err)
	}
	if !reopened.Exist(GamificationSystemAddress) {
		t.Fatalf("gamification system address missing after reopen from root %s", root.Hex())
	}
	if nonce := reopened.GetNonce(GamificationSystemAddress); nonce != 1 {
		t.Fatalf("expected persisted nonce 1, got %d", nonce)
	}
	if got := GetMinerBlockCount(reopened, miner); got != 1 {
		t.Fatalf("expected persisted miner block count 1, got %d", got)
	}
}

func TestGamificationBlockStateRootRegression(t *testing.T) {
	db := rawdb.NewMemoryDatabase()
	tdb := triedb.NewDatabase(db, nil)
	genesisSpec := params.DefaultRogueGenesisBlock()
	genesisBlock := core.MustCommitGenesis(db, tdb, genesisSpec)

	rx := New(Config{
		FastMode: false,
		Threads:  1,
		Treasury: params.RogueTreasuryAddress,
	})
	defer rx.Close()

	miner := common.HexToAddress("0x0544802665bc7c22124348e745a7abaa8420e197")
	blocks, _ := core.GenerateChain(genesisSpec.Config, genesisBlock, rx, db, 3, func(i int, b *core.BlockGen) {
		b.SetCoinbase(miner)
	})
	if len(blocks) != 3 {
		t.Fatalf("expected 3 generated blocks, got %d", len(blocks))
	}

	expectedRoots := []common.Hash{
		common.HexToHash("0x6dcc3b4c04e419301d146c1b88a6fa98d2018611daf13adfc4b3ba60b4dbee80"),
		common.HexToHash("0x7dfb77bbe418c671053018503907a79e4fce3a1d77714fc0dcad0a5fc4f57fa0"),
		common.HexToHash("0x8e2435966006e522a5a3fe0570e7bc90a88d5be0bf71bf7e62d13582ec0b2574"),
	}
	for i, blk := range blocks {
		if blk.Root() != expectedRoots[i] {
			t.Fatalf("block %d stateRoot regression: got %s, want %s", i+1, blk.Root().Hex(), expectedRoots[i].Hex())
		}
	}
}
