package miner

import (
	"bytes"
	"math/big"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/randomx"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/txpool"
	"github.com/ethereum/go-ethereum/core/txpool/legacypool"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/event"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/params/types/genesisT"
)

func TestRogueWorkerStopAndEngineReset(t *testing.T) {
	t.Parallel()

	db := rawdb.NewMemoryDatabase()
	engine := randomx.New(randomx.Config{FastMode: false, Threads: 1})
	defer engine.Close()

	gspec := &genesisT.Genesis{
		Config:   params.RogueChainConfig,
		GasLimit: 50000000,
		Alloc:    genesisT.GenesisAlloc{testBankAddress: {Balance: testBankFunds}},
	}
	chain, err := core.NewBlockChain(db, &core.CacheConfig{TrieDirtyDisabled: true}, gspec, nil, engine, vm.Config{}, nil, nil)
	if err != nil {
		t.Fatalf("core.NewBlockChain failed: %v", err)
	}
	defer chain.Stop()

	pool := legacypool.New(testTxPoolConfig, chain)
	tp, err := txpool.New(testTxPoolConfig.PriceLimit, chain, []txpool.SubPool{pool})
	if err != nil {
		t.Fatalf("txpool.New failed: %v", err)
	}
	defer tp.Close()

	backend := &testWorkerBackend{
		chain:   chain,
		txPool:  tp,
		genesis: gspec,
	}
	mux := new(event.TypeMux)
	defer mux.Stop()

	cfg := DefaultConfig
	cfg.Etherbase = testBankAddress
	w := newWorker(&cfg, params.RogueChainConfig, engine, backend, mux, nil, false)
	var closeOnce sync.Once
	defer closeOnce.Do(w.close)

	// Seed a non-zero lastSealHash and start the worker
	w.pendingMu.Lock()
	w.lastSealHash = common.HexToHash("0x1234")
	w.pendingMu.Unlock()

	w.start()
	time.Sleep(100 * time.Millisecond)

	// Stopping the worker must clear running state and reset lastSealHash
	w.stop()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		w.pendingMu.RLock()
		cleared := w.lastSealHash == (common.Hash{})
		w.pendingMu.RUnlock()
		if cleared && !w.isRunning() {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	w.pendingMu.RLock()
	last := w.lastSealHash
	w.pendingMu.RUnlock()
	if last != (common.Hash{}) {
		t.Fatalf("expected lastSealHash to be cleared on stop, got %s", last.Hex())
	}

	// Disabling RandomX threads must reset hashrate to 0
	engine.SetThreads(-1)
	if rate := engine.Hashrate(); rate != 0 {
		t.Fatalf("expected RandomX hashrate 0 after SetThreads(-1), got %f", rate)
	}
}

func TestRogueWorkerRequiresExplicitEtherbase(t *testing.T) {
	t.Parallel()

	db := rawdb.NewMemoryDatabase()
	engine := randomx.New(randomx.Config{FastMode: false, Threads: 1})
	defer engine.Close()

	gspec := &genesisT.Genesis{
		Config:     params.RogueChainConfig,
		GasLimit:   50000000,
		Difficulty: big.NewInt(0x10000),
		Alloc:      genesisT.GenesisAlloc{testBankAddress: {Balance: testBankFunds}},
	}
	chain, err := core.NewBlockChain(db, &core.CacheConfig{TrieDirtyDisabled: true}, gspec, nil, engine, vm.Config{}, nil, nil)
	if err != nil {
		t.Fatalf("core.NewBlockChain failed: %v", err)
	}
	defer chain.Stop()

	pool := legacypool.New(testTxPoolConfig, chain)
	tp, err := txpool.New(testTxPoolConfig.PriceLimit, chain, []txpool.SubPool{pool})
	if err != nil {
		t.Fatalf("txpool.New failed: %v", err)
	}
	defer tp.Close()

	backend := &testWorkerBackend{
		chain:   chain,
		txPool:  tp,
		genesis: gspec,
	}
	mux := new(event.TypeMux)
	defer mux.Stop()

	w := newWorker(&DefaultConfig, params.RogueChainConfig, engine, backend, mux, nil, false)
	defer w.close()

	taskCh := make(chan *task, 1)
	w.newTaskHook = func(tsk *task) {
		taskCh <- tsk
	}
	w.running.Store(true)
	w.commitWork(nil, true, time.Now().Unix())

	select {
	case tsk := <-taskCh:
		t.Fatalf("expected commitWork to refuse mining without etherbase, got task with coinbase %s", tsk.block.Coinbase().Hex())
	case <-time.After(150 * time.Millisecond):
	}
}

func TestRogueWorkerIgnoresSideBlockUncles(t *testing.T) {
	t.Parallel()

	db := rawdb.NewMemoryDatabase()
	engine := randomx.New(randomx.Config{FastMode: false, Threads: 1})
	defer engine.Close()

	gspec := &genesisT.Genesis{
		Config:     params.RogueChainConfig,
		GasLimit:   50000000,
		Difficulty: big.NewInt(0x10000),
		Alloc:      genesisT.GenesisAlloc{testBankAddress: {Balance: testBankFunds}},
	}
	chain, err := core.NewBlockChain(db, &core.CacheConfig{TrieDirtyDisabled: true}, gspec, nil, engine, vm.Config{}, nil, nil)
	if err != nil {
		t.Fatalf("core.NewBlockChain failed: %v", err)
	}
	defer chain.Stop()

	pool := legacypool.New(testTxPoolConfig, chain)
	tp, err := txpool.New(testTxPoolConfig.PriceLimit, chain, []txpool.SubPool{pool})
	if err != nil {
		t.Fatalf("txpool.New failed: %v", err)
	}
	defer tp.Close()

	backend := &testWorkerBackend{
		chain:   chain,
		txPool:  tp,
		genesis: gspec,
	}
	mux := new(event.TypeMux)
	defer mux.Stop()

	cfg := DefaultConfig
	cfg.Etherbase = testBankAddress
	w := newWorker(&cfg, params.RogueChainConfig, engine, backend, mux, nil, false)
	var closeOnce sync.Once
	defer closeOnce.Do(w.close)

	taskCh := make(chan *task, 4)
	w.newTaskHook = func(tsk *task) {
		taskCh <- tsk
	}
	w.skipSealHook = func(*task) bool {
		return true
	}

	w.start()
	var tsk *task
	select {
	case tsk = <-taskCh:
		if len(tsk.block.Uncles()) != 0 {
			t.Fatalf("expected 0 uncles in initial RandomX task, got %d", len(tsk.block.Uncles()))
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for initial RandomX sealing task")
	}

	// Simulate a competing side-block arriving via ChainSideEvent.
	genesis := chain.Genesis()
	sideBlock := types.NewBlockWithHeader(&types.Header{
		ParentHash: genesis.Hash(),
		Number:     big.NewInt(1),
		Time:       genesis.Time() + 30,
		Difficulty: big.NewInt(0x10000),
		GasLimit:   genesis.GasLimit(),
	})
	w.chainSideCh <- core.ChainSideEvent{Block: sideBlock}

	// Trigger another work cycle with a distinct timestamp and verify uncles are
	// still suppressed and commit succeeds (not stalled by ErrUnclesNotAllowed).
	nextTS := tsk.block.Time() + 1
	w.newWorkCh <- &newWorkReq{timestamp: int64(nextTS)}
	select {
	case nextTsk := <-taskCh:
		if len(nextTsk.block.Uncles()) != 0 {
			t.Fatalf("expected 0 uncles after side-block event, got %d", len(nextTsk.block.Uncles()))
		}
	case <-time.After(3 * time.Second):
		t.Fatal("worker stalled after side-block event instead of committing new RandomX work")
	}

	closeOnce.Do(w.close)
	if len(w.localUncles) != 0 || len(w.remoteUncles) != 0 {
		t.Fatalf("expected RandomX worker not to cache uncles, got local=%d remote=%d", len(w.localUncles), len(w.remoteUncles))
	}
}

func TestRogueMinerExtraData64Bytes(t *testing.T) {
	t.Parallel()

	db := rawdb.NewMemoryDatabase()
	engine := randomx.New(randomx.Config{FastMode: false, Threads: 1})
	defer engine.Close()

	gspec := &genesisT.Genesis{
		Config:   params.RogueChainConfig,
		GasLimit: 50000000,
		Alloc:    genesisT.GenesisAlloc{testBankAddress: {Balance: testBankFunds}},
	}
	chain, err := core.NewBlockChain(db, &core.CacheConfig{TrieDirtyDisabled: true}, gspec, nil, engine, vm.Config{}, nil, nil)
	if err != nil {
		t.Fatalf("core.NewBlockChain failed: %v", err)
	}
	defer chain.Stop()

	pool := legacypool.New(testTxPoolConfig, chain)
	tp, err := txpool.New(testTxPoolConfig.PriceLimit, chain, []txpool.SubPool{pool})
	if err != nil {
		t.Fatalf("txpool.New failed: %v", err)
	}
	defer tp.Close()

	backend := &testWorkerBackend{
		chain:   chain,
		txPool:  tp,
		genesis: gspec,
	}
	mux := new(event.TypeMux)
	defer mux.Stop()

	m := New(backend, &DefaultConfig, params.RogueChainConfig, mux, engine, nil)
	defer m.Close()

	extra64 := bytes.Repeat([]byte("R"), 64)
	if err := m.SetExtra(extra64); err != nil {
		t.Fatalf("expected 64-byte ExtraData to succeed on Rogue RandomX, got: %v", err)
	}
	extra65 := bytes.Repeat([]byte("R"), 65)
	if err := m.SetExtra(extra65); err == nil {
		t.Fatal("expected 65-byte ExtraData to be rejected on Rogue RandomX")
	}
}
