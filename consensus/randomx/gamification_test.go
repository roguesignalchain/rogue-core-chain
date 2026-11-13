package randomx

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/triedb"
	"github.com/holiman/uint256"
)

func TestIncrementMinerBlockCountAndStorageSlot(t *testing.T) {
	db := rawdb.NewMemoryDatabase()
	tdb := triedb.NewDatabase(db, nil)
	genesisBlock := core.MustCommitGenesis(db, tdb, params.DefaultRogueGenesisBlock())
	sdb, err := state.New(genesisBlock.Root(), state.NewDatabaseWithNodeDB(db, tdb), nil)
	if err != nil {
		t.Fatalf("failed to create state db: %v", err)
	}

	miner := common.HexToAddress("0xdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef")

	// Verify Solidity mapping slot 1 key derivation: keccak256(abi.encode(miner, uint256(1)))
	expectedSlot := crypto.Keccak256Hash(
		common.LeftPadBytes(miner.Bytes(), 32),
		common.LeftPadBytes([]byte{1}, 32),
	)
	if got := MinerStorageKey(miner); got != expectedSlot {
		t.Fatalf("MinerStorageKey mismatch: got %s, want %s", got.Hex(), expectedSlot.Hex())
	}

	if count := GetMinerBlockCount(sdb, miner); count != 0 {
		t.Fatalf("expected initial count 0, got %d", count)
	}

	IncrementMinerBlockCount(sdb, miner)
	IncrementMinerBlockCount(sdb, miner)
	IncrementMinerBlockCount(sdb, miner)
	sdb.IntermediateRoot(true)

	if count := GetMinerBlockCount(sdb, miner); count != 3 {
		t.Fatalf("expected count 3, got %d", count)
	}
}

func TestGamificationSurvivesEIP158FinaliseAndCommit(t *testing.T) {
	db := rawdb.NewMemoryDatabase()
	tdb := triedb.NewDatabase(db, nil)
	genesisSpec := params.DefaultRogueGenesisBlock()
	genesisBlock := core.MustCommitGenesis(db, tdb, genesisSpec)

	sdb, err := state.New(genesisBlock.Root(), state.NewDatabaseWithNodeDB(db, tdb), nil)
	if err != nil {
		t.Fatalf("failed to open genesis state db: %v", err)
	}

	miner := common.HexToAddress("0xdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
	if count := IncrementMinerBlockCount(sdb, miner); count != 1 {
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
	if codeSize := sdb.GetCodeSize(GamificationSystemAddress); codeSize == 0 {
		t.Fatalf("expected non-zero system contract code on %s", GamificationSystemAddress.Hex())
	}

	root, err := sdb.Commit(1, true)
	if err != nil {
		t.Fatalf("failed to commit state: %v", err)
	}
	if err := tdb.Commit(root, false); err != nil {
		t.Fatalf("failed to commit trie db: %v", err)
	}

	reopened, err := state.New(root, state.NewDatabaseWithNodeDB(db, tdb), nil)
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

func TestSystemContractGamificationEVM(t *testing.T) {
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

	// Open state at block 3
	tip := blocks[len(blocks)-1]
	sdb, err := state.New(tip.Root(), state.NewDatabaseWithNodeDB(db, tdb), nil)
	if err != nil {
		t.Fatalf("failed to open tip state: %v", err)
	}

	blockCtx := vm.BlockContext{
		CanTransfer: core.CanTransfer,
		Transfer:    core.Transfer,
		GetHash:     func(n uint64) common.Hash { return common.Hash{} },
		Coinbase:    tip.Coinbase(),
		BlockNumber: tip.Number(),
		Time:        tip.Time(),
		Difficulty:  tip.Difficulty(),
		GasLimit:    tip.GasLimit(),
		BaseFee:     big.NewInt(0),
	}
	txCtx := vm.TxContext{
		Origin:   miner,
		GasPrice: big.NewInt(1),
	}
	evm := vm.NewEVM(blockCtx, txCtx, sdb, genesisSpec.Config, vm.Config{})

	staticCall := func(sig string, args ...[]byte) []byte {
		selector := crypto.Keccak256([]byte(sig))[:4]
		input := append([]byte{}, selector...)
		for _, a := range args {
			input = append(input, common.LeftPadBytes(a, 32)...)
		}
		ret, _, callErr := evm.StaticCall(vm.AccountRef(miner), GamificationSystemAddress, input, 1_000_000)
		if callErr != nil {
			t.Fatalf("StaticCall(%s) failed: %v", sig, callErr)
		}
		return ret
	}

	// 1. owner() == RogueTreasuryAddress
	ownerRet := staticCall("owner()")
	if gotOwner := common.BytesToAddress(ownerRet); gotOwner != params.RogueTreasuryAddress {
		t.Fatalf("owner() mismatch: got %s, want %s", gotOwner.Hex(), params.RogueTreasuryAddress.Hex())
	}

	// 2. blocksMined(miner) == 3
	blocksRet := staticCall("blocksMined(address)", miner.Bytes())
	if gotBlocks := new(big.Int).SetBytes(blocksRet).Uint64(); gotBlocks != 3 {
		t.Fatalf("blocksMined(miner) mismatch: got %d, want 3", gotBlocks)
	}

	// 3. getMinerLevel(miner) == 1 (1..24 blocks)
	levelRet := staticCall("getMinerLevel(address)", miner.Bytes())
	if gotLevel := new(big.Int).SetBytes(levelRet).Uint64(); gotLevel != 1 {
		t.Fatalf("getMinerLevel(miner) mismatch: got %d, want 1", gotLevel)
	}

	// 4. meetsLevel(miner, 1) == true, meetsLevel(miner, 2) == false
	meets1 := staticCall("meetsLevel(address,uint32)", miner.Bytes(), []byte{1})
	if new(big.Int).SetBytes(meets1).Uint64() != 1 {
		t.Fatal("expected meetsLevel(miner, 1) to be true")
	}
	meets2 := staticCall("meetsLevel(address,uint32)", miner.Bytes(), []byte{2})
	if new(big.Int).SetBytes(meets2).Uint64() != 0 {
		t.Fatal("expected meetsLevel(miner, 2) to be false")
	}

	// 5. Sending native value > 0 must revert (non-payable)
	sdb.AddBalance(miner, uint256.NewInt(1_000_000))
	sel := crypto.Keccak256([]byte("getMinerLevel(address)"))[:4]
	input := append(sel, common.LeftPadBytes(miner.Bytes(), 32)...)
	_, _, valErr := evm.Call(vm.AccountRef(miner), GamificationSystemAddress, input, 1_000_000, uint256.NewInt(1))
	if valErr == nil {
		t.Fatal("expected non-payable Call with value > 0 to revert")
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
		common.HexToHash("0x017cd5ec3a8eefcd838b847dd7b099b4ab1246acded66ae075dc71a2776d7d0d"),
		common.HexToHash("0xc30b9d99268d52ecccf80344c0c50c1147297fd2b6737cd5784f8bb940eea8f4"),
		common.HexToHash("0x6b452ec0ae3c90a7899b897b79c93829e0fb7ce93aee4b19fa106209f470d071"),
	}
	for i, blk := range blocks {
		if blk.Root() != expectedRoots[i] {
			t.Fatalf("block %d stateRoot regression: got %s, want %s", i+1, blk.Root().Hex(), expectedRoots[i].Hex())
		}
	}
}
