package randomx

import (
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

func TestAuthor(t *testing.T) {
	rx := New(Config{FastMode: false, Threads: 1})
	defer rx.Close()

	coinbase := common.HexToAddress("0x1234567890123456789012345678901234567890")
	header := &types.Header{Coinbase: coinbase}

	author, err := rx.Author(header)
	if err != nil {
		t.Fatalf("Author returned error: %v", err)
	}
	if author != coinbase {
		t.Errorf("Author mismatch: got %v, want %v", author, coinbase)
	}
}

func TestVerifyUnclesRejection(t *testing.T) {
	rx := New(Config{FastMode: false, Threads: 1})
	defer rx.Close()

	// Block with uncles must fail
	uncle := &types.Header{Number: big.NewInt(1)}
	blockWithUncles := types.NewBlock(&types.Header{Number: big.NewInt(2)}, nil, []*types.Header{uncle}, nil, nil)

	if err := rx.VerifyUncles(nil, blockWithUncles); err != ErrUnclesNotAllowed {
		t.Errorf("Expected ErrUnclesNotAllowed, got %v", err)
	}

	// Block without uncles must pass
	blockNoUncles := types.NewBlock(&types.Header{Number: big.NewInt(2)}, nil, nil, nil, nil)
	if err := rx.VerifyUncles(nil, blockNoUncles); err != nil {
		t.Errorf("Block without uncles failed: %v", err)
	}
}

func TestSealHashDeterminism(t *testing.T) {
	rx := New(Config{FastMode: false, Threads: 1})
	defer rx.Close()

	h1 := &types.Header{
		Number:     big.NewInt(10),
		Difficulty: big.NewInt(0x10000),
		Time:       1000,
		Nonce:      types.EncodeNonce(12345),
		MixDigest:  common.HexToHash("0xabcdef"),
	}

	h2 := &types.Header{
		Number:     big.NewInt(10),
		Difficulty: big.NewInt(0x10000),
		Time:       1000,
		Nonce:      types.EncodeNonce(99999),                 // Different nonce
		MixDigest:  common.HexToHash("0x111111111111111111"), // Different MixDigest
	}

	// SealHash must ignore Nonce and MixDigest
	hash1 := rx.SealHash(h1)
	hash2 := rx.SealHash(h2)

	if hash1 != hash2 {
		t.Errorf("SealHash must be invariant to Nonce and MixDigest, got %s != %s", hash1.Hex(), hash2.Hex())
	}
}

func TestSealAndVerifySealIntegration(t *testing.T) {
	chain := NewFakeChainReader()

	// Genesis header
	genesis := &types.Header{
		Number: big.NewInt(0),
		Time:   1000,
	}
	chain.AddHeader(genesis)

	rx := New(Config{FastMode: false, Threads: 2})
	defer rx.Close()

	// Low difficulty for fast mining test (averages ~4 hashes)
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

	// Mine the block
	err := rx.Seal(chain, block, results, stop)
	if err != nil {
		t.Fatalf("Seal failed: %v", err)
	}

	var sealedBlock *types.Block
	select {
	case sealedBlock = <-results:
	case <-time.After(15 * time.Second):
		close(stop)
		t.Fatal("Mining timed out after 15 seconds")
	}

	sealedHeader := sealedBlock.Header()
	t.Logf("Mined block with Nonce: %d, MixDigest: %s", sealedHeader.Nonce.Uint64(), sealedHeader.MixDigest.Hex())

	// Verify seal with correct block
	if err := rx.VerifySeal(chain, sealedHeader); err != nil {
		t.Fatalf("VerifySeal failed on valid mined block: %v", err)
	}

	// Tampered nonce should fail verification
	tamperedHeader := types.CopyHeader(sealedHeader)
	tamperedHeader.Nonce = types.EncodeNonce(sealedHeader.Nonce.Uint64() + 1)
	if err := rx.VerifySeal(chain, tamperedHeader); err == nil {
		t.Error("VerifySeal accepted tampered nonce")
	}
}
