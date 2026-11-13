package miner

import (
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/randomx"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/txpool"
	"github.com/ethereum/go-ethereum/core/txpool/legacypool"
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

	w := newWorker(&DefaultConfig, params.RogueChainConfig, engine, backend, mux, nil, false)
	defer w.close()

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
