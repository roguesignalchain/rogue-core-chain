package randomx

import (
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/state"
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

func TestVerifyHeadersBatch(t *testing.T) {
	chain := NewFakeChainReader()
	now := uint64(time.Now().Unix())

	// Genesis header
	genesis := &types.Header{
		Number:     big.NewInt(0),
		Time:       now - 120,
		Difficulty: big.NewInt(0x10000),
	}
	chain.AddHeader(genesis)

	rx := New(Config{FastMode: false, Threads: 1})
	defer rx.Close()

	// Create a chain of 3 headers where each parent is the previous header in the batch
	h1 := &types.Header{
		ParentHash: genesis.Hash(),
		Number:     big.NewInt(1),
		Time:       genesis.Time + 30,
		GasLimit:   60000000,
		Extra:      []byte("Rogue Node v0.1.0"),
	}
	h1.Difficulty = CalculateDifficulty(chain, h1.Time, genesis)

	tempChain := NewFakeChainReader()
	tempChain.AddHeader(genesis)
	tempChain.AddHeader(h1)

	h2 := &types.Header{
		ParentHash: h1.Hash(),
		Number:     big.NewInt(2),
		Time:       h1.Time + 30,
		GasLimit:   60000000,
	}
	h2.Difficulty = CalculateDifficulty(tempChain, h2.Time, h1)
	tempChain.AddHeader(h2)

	h3 := &types.Header{
		ParentHash: h2.Hash(),
		Number:     big.NewInt(3),
		Time:       h2.Time + 30,
		GasLimit:   60000000,
	}
	h3.Difficulty = CalculateDifficulty(tempChain, h3.Time, h2)

	headers := []*types.Header{h1, h2, h3}
	seals := []bool{false, false, false}

	// Verify batch where ancestors h1 and h2 are only in the batch and not yet in chain
	abort, results := rx.VerifyHeaders(chain, headers, seals)
	defer close(abort)

	for _, header := range headers {
		err := <-results
		if err != nil {
			t.Fatalf("Header #%d (%s) failed verification: %v", header.Number.Uint64(), header.Hash().Hex(), err)
		}
	}
}

func TestExtraDataLimitValidation(t *testing.T) {
	chain := NewFakeChainReader()
	now := uint64(time.Now().Unix())

	genesis := &types.Header{
		Number:     big.NewInt(0),
		Time:       now - 60,
		Difficulty: big.NewInt(0x10000),
	}
	chain.AddHeader(genesis)

	rx := New(Config{FastMode: false, Threads: 1})
	defer rx.Close()

	// Exactly 64 bytes of ExtraData must be accepted
	validExtra := make([]byte, MaxExtraDataSize)
	hValid := &types.Header{
		ParentHash: genesis.Hash(),
		Number:     big.NewInt(1),
		Time:       now,
		GasLimit:   60000000,
		Extra:      validExtra,
	}
	hValid.Difficulty = CalculateDifficulty(chain, hValid.Time, genesis)
	if err := rx.VerifyHeader(chain, hValid, false); err != nil {
		t.Fatalf("Expected 64-byte ExtraData to be valid, got: %v", err)
	}

	// 65 bytes of ExtraData must be rejected with ErrExtraDataTooLong
	oversizedExtra := make([]byte, MaxExtraDataSize+1)
	hInvalid := &types.Header{
		ParentHash: genesis.Hash(),
		Number:     big.NewInt(1),
		Time:       now,
		GasLimit:   60000000,
		Extra:      oversizedExtra,
	}
	hInvalid.Difficulty = CalculateDifficulty(chain, hInvalid.Time, genesis)
	if err := rx.VerifyHeader(chain, hInvalid, false); err == nil {
		t.Fatal("Expected oversized 65-byte ExtraData to be rejected, but got nil")
	}
}

func TestPrepareMTP11Adjustment(t *testing.T) {
	chain := NewFakeChainReader()
	rx := New(Config{FastMode: false, Threads: 1})
	defer rx.Close()

	// Construct 11 headers where earlier blocks have higher timestamps than the last parent
	// (valid under MTP-11 when mined, resulting in MTP11(parent) > parent.Time)
	times := []uint64{1000, 1030, 1060, 1090, 1120, 1150, 1180, 1210, 1240, 1270, 1100}
	var parent *types.Header
	for i, ts := range times {
		h := &types.Header{
			Number:     big.NewInt(int64(i)),
			Time:       ts,
			Difficulty: big.NewInt(0x10000),
		}
		if parent != nil {
			h.ParentHash = parent.Hash()
		}
		chain.AddHeader(h)
		parent = h
	}

	mtp := CalculateMTP11(chain, parent)
	if mtp <= parent.Time {
		t.Fatalf("Test setup expected mtp (%d) > parent.Time (%d)", mtp, parent.Time)
	}

	// Simulate worker.go setting candidate.Time = parent.Time + 1 (1101 <= mtp 1150)
	candidate := &types.Header{
		ParentHash: parent.Hash(),
		Number:     big.NewInt(11),
		Time:       parent.Time + 1,
		GasLimit:   60000000,
	}

	if err := rx.Prepare(chain, candidate); err != nil {
		t.Fatalf("Prepare failed: %v", err)
	}

	if candidate.Time <= mtp {
		t.Fatalf("Prepare did not advance candidate.Time past MTP-11: got %d, mtp %d", candidate.Time, mtp)
	}
}

func TestConsensusStatusAndEpochInfoAPI(t *testing.T) {
	chain := NewFakeChainReader()
	genesis := &types.Header{
		Number:     big.NewInt(0),
		Time:       1000,
		Difficulty: big.NewInt(0x10000),
	}
	chain.AddHeader(genesis)

	rx := New(Config{FastMode: false, Threads: 1})
	defer rx.Close()

	api := &API{rx: rx, chain: chain}
	status := api.GetConsensusStatus()

	if status.TargetBlockTime != 30 {
		t.Errorf("Expected TargetBlockTime 30, got %d", status.TargetBlockTime)
	}
	if status.MaxReorgDepth != 90 {
		t.Errorf("Expected MaxReorgDepth 90, got %d", status.MaxReorgDepth)
	}
	if status.MaxExtraDataSize != 64 {
		t.Errorf("Expected MaxExtraDataSize 64, got %d", status.MaxExtraDataSize)
	}
	if status.HalvingInterval != 2100000 {
		t.Errorf("Expected HalvingInterval 2100000, got %d", status.HalvingInterval)
	}
	if !status.MESSActive {
		t.Error("Expected MESSActive to be true")
	}
}

func TestFinalizeAndAssembleWithTransactions(t *testing.T) {
	chain := NewFakeChainReader()
	rx := New(Config{FastMode: false, Threads: 1})
	defer rx.Close()

	header := &types.Header{
		Number:     big.NewInt(1),
		Time:       1010,
		Coinbase:   common.HexToAddress("0x1111111111111111111111111111111111111111"),
		Difficulty: big.NewInt(1000),
	}

	statedb, err := state.New(common.Hash{}, state.NewDatabase(rawdb.NewMemoryDatabase()), nil)
	if err != nil {
		t.Fatalf("Failed to create statedb: %v", err)
	}

	tx := types.NewTx(&types.LegacyTx{
		Nonce:    0,
		To:       &common.Address{},
		Value:    big.NewInt(1000),
		Gas:      21000,
		GasPrice: big.NewInt(1),
	})
	receipt := &types.Receipt{
		Status:            types.ReceiptStatusSuccessful,
		CumulativeGasUsed: 21000,
	}

	block, err := rx.FinalizeAndAssemble(chain, header, statedb, []*types.Transaction{tx}, nil, []*types.Receipt{receipt}, nil)
	if err != nil {
		t.Fatalf("FinalizeAndAssemble failed: %v", err)
	}

	if len(block.Transactions()) != 1 {
		t.Fatalf("Expected 1 transaction in block, got %d", len(block.Transactions()))
	}
	if block.Header().TxHash == types.EmptyTxsHash {
		t.Errorf("TxHash must not be empty when transactions are present")
	}
}

func TestRemoteMiningDisabled(t *testing.T) {
	chain := NewFakeChainReader()
	rx := New(Config{FastMode: false, Threads: 1})
	defer rx.Close()

	apis := rx.APIs(chain)
	if len(apis) == 0 {
		t.Fatal("Expected non-empty APIs")
	}

	api := apis[0].Service.(*API)

	if _, err := api.GetWork(); err != ErrRemoteMiningDisabled {
		t.Errorf("Expected ErrRemoteMiningDisabled from GetWork, got %v", err)
	}

	if api.SubmitWork(types.EncodeNonce(1), common.Hash{}, common.Hash{}) {
		t.Error("Expected SubmitWork to return false when remote mining is disabled")
	}

	if api.SubmitHashrate(1000, common.Hash{}) {
		t.Error("Expected SubmitHashrate to return false when remote mining is disabled")
	}
}
