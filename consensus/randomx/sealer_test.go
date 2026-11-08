package randomx

import (
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

func TestRemoteSealerGetAndSubmitWork(t *testing.T) {
	chain := NewFakeChainReader()
	genesis := &types.Header{
		Number: big.NewInt(0),
		Time:   1000,
	}
	chain.AddHeader(genesis)

	rx := New(Config{FastMode: false, Threads: 0})
	defer rx.Close()

	apis := rx.APIs(chain)
	if len(apis) == 0 {
		t.Fatal("Expected non-empty APIs")
	}

	api := apis[0].Service.(*API)

	// Before any work is dispatched, GetWork must return an error
	if _, err := api.GetWork(); err == nil {
		t.Error("Expected error from GetWork when no work is pending")
	}

	// Dispatch block with low difficulty
	diff := big.NewInt(4)
	header := &types.Header{
		ParentHash: genesis.Hash(),
		Number:     big.NewInt(1),
		Difficulty: diff,
		Time:       1010,
		GasLimit:   60000000,
	}
	block := types.NewBlock(header, nil, nil, nil, nil)
	results := make(chan *types.Block, 1)
	stop := make(chan struct{})
	defer close(stop)

	go rx.Seal(chain, block, results, stop)

	// Allow sealer loop to process sealTask
	time.Sleep(50 * time.Millisecond)

	work, err := api.GetWork()
	if err != nil {
		t.Fatalf("GetWork failed: %v", err)
	}

	sealHash := rx.SealHash(block.Header())
	if work[0] != sealHash.Hex() {
		t.Errorf("SealHash mismatch: got %s, want %s", work[0], sealHash.Hex())
	}

	target := DifficultyToTarget(diff)
	if work[2] != common.BytesToHash(target.Bytes()).Hex() {
		t.Errorf("Target mismatch: got %s, want %s", work[2], common.BytesToHash(target.Bytes()).Hex())
	}

	// Mine a solution for this block
	ctx, err := rx.epochs.GetContext(1, chain)
	if err != nil {
		t.Fatalf("Failed to get RandomX context: %v", err)
	}

	var (
		winningNonce  uint64
		winningDigest common.Hash
		found         bool
	)
	for nonce := uint64(0); nonce < 100000; nonce++ {
		input := MiningInput(sealHash, nonce)
		rxHash, err := ctx.CalculateHash(input)
		if err != nil {
			t.Fatalf("CalculateHash failed: %v", err)
		}
		if HashToBig(rxHash).Cmp(target) <= 0 {
			winningNonce = nonce
			winningDigest = common.BytesToHash(rxHash[:])
			found = true
			break
		}
	}
	if !found {
		t.Fatal("Failed to find winning nonce within 100000 iterations")
	}

	// Submit invalid solution
	badDigest := common.HexToHash("0xdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
	if api.SubmitWork(types.EncodeNonce(999999), sealHash, badDigest) {
		t.Error("SubmitWork should fail for invalid solution")
	}

	// Submit valid solution
	if !api.SubmitWork(types.EncodeNonce(winningNonce), sealHash, winningDigest) {
		t.Fatal("SubmitWork failed for valid solution")
	}

	// Verify sealed block arrives in results channel
	select {
	case sealedBlock := <-results:
		if sealedBlock.NumberU64() != 1 {
			t.Errorf("Sealed block number mismatch: got %d, want 1", sealedBlock.NumberU64())
		}
		if sealedBlock.Nonce() != winningNonce {
			t.Errorf("Nonce mismatch: got %v, want %v", sealedBlock.Nonce(), winningNonce)
		}
		if sealedBlock.MixDigest() != winningDigest {
			t.Errorf("MixDigest mismatch: got %s, want %s", sealedBlock.MixDigest().Hex(), winningDigest.Hex())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Timed out waiting for sealed block from results channel")
	}
}
