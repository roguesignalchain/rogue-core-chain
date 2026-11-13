package randomx

import (
	"errors"
	"math"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
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
		GasLimit:   60000000,
	}
	chain.AddHeader(genesis)

	rx := New(Config{FastMode: false, Threads: 1})
	defer rx.Close()

	// Create a chain of 3 headers where each parent is the previous header in the batch
	h1 := &types.Header{
		ParentHash: genesis.Hash(),
		UncleHash:  types.EmptyUncleHash,
		Number:     big.NewInt(1),
		Time:       genesis.Time + 30,
		GasLimit:   60000000,
		Extra:      []byte("Rogue Node v0.1.0"),
	}
	d1, err := CalculateDifficulty(chain, h1.Time, genesis)
	if err != nil {
		t.Fatalf("CalculateDifficulty h1 failed: %v", err)
	}
	h1.Difficulty = d1

	tempChain := NewFakeChainReader()
	tempChain.AddHeader(genesis)
	tempChain.AddHeader(h1)

	h2 := &types.Header{
		ParentHash: h1.Hash(),
		UncleHash:  types.EmptyUncleHash,
		Number:     big.NewInt(2),
		Time:       h1.Time + 30,
		GasLimit:   60000000,
	}
	d2, err := CalculateDifficulty(tempChain, h2.Time, h1)
	if err != nil {
		t.Fatalf("CalculateDifficulty h2 failed: %v", err)
	}
	h2.Difficulty = d2
	tempChain.AddHeader(h2)

	h3 := &types.Header{
		ParentHash: h2.Hash(),
		UncleHash:  types.EmptyUncleHash,
		Number:     big.NewInt(3),
		Time:       h2.Time + 30,
		GasLimit:   60000000,
	}
	d3, err := CalculateDifficulty(tempChain, h3.Time, h2)
	if err != nil {
		t.Fatalf("CalculateDifficulty h3 failed: %v", err)
	}
	h3.Difficulty = d3

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
		GasLimit:   60000000,
	}
	chain.AddHeader(genesis)

	rx := New(Config{FastMode: false, Threads: 1})
	defer rx.Close()

	// Exactly 64 bytes of ExtraData must be accepted
	validExtra := make([]byte, MaxExtraDataSize)
	hValid := &types.Header{
		ParentHash: genesis.Hash(),
		UncleHash:  types.EmptyUncleHash,
		Number:     big.NewInt(1),
		Time:       now,
		GasLimit:   60000000,
		Extra:      validExtra,
	}
	dValid, err := CalculateDifficulty(chain, hValid.Time, genesis)
	if err != nil {
		t.Fatalf("CalculateDifficulty hValid failed: %v", err)
	}
	hValid.Difficulty = dValid
	if err := rx.VerifyHeader(chain, hValid, false); err != nil {
		t.Fatalf("Expected 64-byte ExtraData to be valid, got: %v", err)
	}

	// 65 bytes of ExtraData must be rejected with ErrExtraDataTooLong
	oversizedExtra := make([]byte, MaxExtraDataSize+1)
	hInvalid := &types.Header{
		ParentHash: genesis.Hash(),
		UncleHash:  types.EmptyUncleHash,
		Number:     big.NewInt(1),
		Time:       now,
		GasLimit:   60000000,
		Extra:      oversizedExtra,
	}
	dInvalid, err := CalculateDifficulty(chain, hInvalid.Time, genesis)
	if err != nil {
		t.Fatalf("CalculateDifficulty hInvalid failed: %v", err)
	}
	hInvalid.Difficulty = dInvalid
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
	if status.FinalityMode != "ECBP-1100 (MESS)" {
		t.Errorf("Expected FinalityMode ECBP-1100 (MESS), got %s", status.FinalityMode)
	}
	if status.VMPool.Mode != "light" {
		t.Errorf("Expected VMPool.Mode light, got %s", status.VMPool.Mode)
	}
	if status.MaxExtraDataSize != 64 {
		t.Errorf("Expected MaxExtraDataSize 64, got %d", status.MaxExtraDataSize)
	}
	if status.HalvingInterval != 2000000 {
		t.Errorf("Expected HalvingInterval 2000000, got %d", status.HalvingInterval)
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
	if len(apis) < 2 {
		t.Fatal("Expected both eth and randomx APIs")
	}

	ethAPI, ok := apis[0].Service.(*EthAPI)
	if !ok {
		t.Fatalf("Expected EthAPI on eth namespace, got %T", apis[0].Service)
	}

	if _, err := ethAPI.GetWork(); err != ErrRemoteMiningDisabled {
		t.Errorf("Expected ErrRemoteMiningDisabled from GetWork, got %v", err)
	}

	if ethAPI.SubmitWork(types.EncodeNonce(1), common.Hash{}, common.Hash{}) {
		t.Error("Expected SubmitWork to return false when remote mining is disabled")
	}

	if ethAPI.SubmitHashrate(1000, common.Hash{}) {
		t.Error("Expected SubmitHashrate to return false when remote mining is disabled")
	}

	rxAPI, ok := apis[1].Service.(*API)
	if !ok {
		t.Fatalf("Expected API on randomx namespace, got %T", apis[1].Service)
	}
	if _, err := rxAPI.GetWork(); err != ErrRemoteMiningDisabled {
		t.Errorf("Expected ErrRemoteMiningDisabled from randomx GetWork, got %v", err)
	}
}

func TestGasLimitValidation(t *testing.T) {
	chain := NewFakeChainReader()
	now := uint64(time.Now().Unix())

	genesis := &types.Header{
		Number:     big.NewInt(0),
		Time:       now - 60,
		Difficulty: big.NewInt(0x10000),
		GasLimit:   60000000,
	}
	chain.AddHeader(genesis)

	rx := New(Config{FastMode: false, Threads: 1})
	defer rx.Close()

	// 1. Jumping gasLimit by 2x parent must be rejected by misc.VerifyGaslimit
	hJump := &types.Header{
		ParentHash: genesis.Hash(),
		UncleHash:  types.EmptyUncleHash,
		Number:     big.NewInt(1),
		Time:       now,
		GasLimit:   120000000,
	}
	dJump, err := CalculateDifficulty(chain, hJump.Time, genesis)
	if err != nil {
		t.Fatalf("CalculateDifficulty hJump failed: %v", err)
	}
	hJump.Difficulty = dJump
	if err := rx.VerifyHeader(chain, hJump, false); err == nil {
		t.Fatal("Expected header with 2x parent GasLimit jump to be rejected")
	}

	// 2. GasLimit within ±1/1024 delta must be accepted
	hValid := &types.Header{
		ParentHash: genesis.Hash(),
		UncleHash:  types.EmptyUncleHash,
		Number:     big.NewInt(1),
		Time:       now,
		GasLimit:   60000000 + (60000000/1024 - 1),
	}
	dValid2, err := CalculateDifficulty(chain, hValid.Time, genesis)
	if err != nil {
		t.Fatalf("CalculateDifficulty hValid2 failed: %v", err)
	}
	hValid.Difficulty = dValid2
	if err := rx.VerifyHeader(chain, hValid, false); err != nil {
		t.Fatalf("Expected valid GasLimit adjustment to pass, got: %v", err)
	}
}

func TestDisallowedOptionalHeaderFields(t *testing.T) {
	chain := NewFakeChainReader()
	now := uint64(time.Now().Unix())

	genesis := &types.Header{
		Number:     big.NewInt(0),
		Time:       now - 60,
		Difficulty: big.NewInt(0x10000),
		GasLimit:   60000000,
	}
	chain.AddHeader(genesis)

	rx := New(Config{FastMode: false, Threads: 1})
	defer rx.Close()

	makeBase := func() *types.Header {
		h := &types.Header{
			ParentHash: genesis.Hash(),
			UncleHash:  types.EmptyUncleHash,
			Number:     big.NewInt(1),
			Time:       now,
			GasLimit:   60000000,
		}
		diff, err := CalculateDifficulty(chain, h.Time, genesis)
		if err != nil {
			t.Fatalf("CalculateDifficulty base failed: %v", err)
		}
		h.Difficulty = diff
		return h
	}

	// Verify BaseFee rejection
	hBaseFee := makeBase()
	hBaseFee.BaseFee = big.NewInt(1000000000)
	if err := rx.VerifyHeader(chain, hBaseFee, false); err == nil {
		t.Fatal("Expected non-nil BaseFee to be rejected")
	}

	// Verify WithdrawalsHash rejection and SealHash commitment
	hWithdrawals := makeBase()
	emptyRoot := types.EmptyRootHash
	hWithdrawals.WithdrawalsHash = &emptyRoot
	if err := rx.VerifyHeader(chain, hWithdrawals, false); err == nil {
		t.Fatal("Expected non-nil WithdrawalsHash to be rejected")
	}
	if rx.SealHash(makeBase()) == rx.SealHash(hWithdrawals) {
		t.Fatal("Expected SealHash to commit to WithdrawalsHash")
	}

	// Verify ExcessBlobGas and BlobGasUsed rejection
	zero := uint64(0)
	hBlob := makeBase()
	hBlob.ExcessBlobGas = &zero
	if err := rx.VerifyHeader(chain, hBlob, false); err == nil {
		t.Fatal("Expected non-nil ExcessBlobGas to be rejected")
	}

	// Verify ParentBeaconRoot rejection
	hBeacon := makeBase()
	hBeacon.ParentBeaconRoot = &emptyRoot
	if err := rx.VerifyHeader(chain, hBeacon, false); err == nil {
		t.Fatal("Expected non-nil ParentBeaconRoot to be rejected")
	}
}

func TestStrictEmptyUncleHash(t *testing.T) {
	chain := NewFakeChainReader()
	now := uint64(time.Now().Unix())

	genesis := &types.Header{
		Number:     big.NewInt(0),
		Time:       now - 60,
		Difficulty: big.NewInt(0x10000),
		GasLimit:   60000000,
	}
	chain.AddHeader(genesis)

	rx := New(Config{FastMode: false, Threads: 1})
	defer rx.Close()

	h := &types.Header{
		ParentHash: genesis.Hash(),
		Number:     big.NewInt(1),
		Time:       now,
		GasLimit:   60000000,
		UncleHash:  common.HexToHash("0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef"),
	}
	diff, err := CalculateDifficulty(chain, h.Time, genesis)
	if err != nil {
		t.Fatalf("CalculateDifficulty failed: %v", err)
	}
	h.Difficulty = diff

	if err := rx.VerifyHeader(chain, h, false); err == nil {
		t.Fatal("Expected header with non-empty UncleHash to be rejected")
	} else if !errors.Is(err, ErrUnclesNotAllowed) {
		t.Fatalf("Expected ErrUnclesNotAllowed, got %v", err)
	}
}

func TestVerifySealMixDigestPrecheck(t *testing.T) {
	rx := New(Config{FastMode: false, Threads: 1})
	defer rx.Close()

	// Even with nil chain, MixDigest > Target must reject immediately before touching EpochManager/CGo
	h := &types.Header{
		Number:     big.NewInt(1),
		Difficulty: big.NewInt(1000000),
		MixDigest:  common.HexToHash("0xffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"),
	}
	if err := rx.VerifySeal(nil, h); err == nil {
		t.Fatal("Expected VerifySeal to reject MixDigest exceeding target before CGo hashing")
	}
}

func TestMalformedAndNilHeaders(t *testing.T) {
	rx := New(Config{FastMode: false, Threads: 1})
	defer rx.Close()

	chain := NewFakeChainReader()
	now := uint64(time.Now().Unix())
	genesis := &types.Header{
		Number:     big.NewInt(0),
		Time:       now - 60,
		Difficulty: big.NewInt(0x10000),
		GasLimit:   60000000,
	}
	chain.AddHeader(genesis)

	hugeNum := new(big.Int).Lsh(big.NewInt(1), 65)

	malformedCases := []struct {
		name   string
		header *types.Header
	}{
		{"nil header", nil},
		{"nil Number", &types.Header{ParentHash: genesis.Hash(), Time: now}},
		{"non-uint64 Number", &types.Header{ParentHash: genesis.Hash(), Number: hugeNum, Time: now}},
	}

	for _, tc := range malformedCases {
		if err := rx.VerifyHeader(chain, tc.header, false); !errors.Is(err, consensus.ErrInvalidNumber) {
			t.Errorf("%s: VerifyHeader expected ErrInvalidNumber, got %v", tc.name, err)
		}
		if err := rx.VerifySeal(chain, tc.header); !errors.Is(err, consensus.ErrInvalidNumber) {
			t.Errorf("%s: VerifySeal expected ErrInvalidNumber, got %v", tc.name, err)
		}
		if _, err := rx.epochs.ResolveSeedForHeader(tc.header, chain); !errors.Is(err, consensus.ErrInvalidNumber) {
			t.Errorf("%s: ResolveSeedForHeader expected ErrInvalidNumber, got %v", tc.name, err)
		}
		if _, err := rx.epochs.GetContextForHeader(tc.header, chain); !errors.Is(err, consensus.ErrInvalidNumber) {
			t.Errorf("%s: GetContextForHeader expected ErrInvalidNumber, got %v", tc.name, err)
		}
		if _, err := CalculateDifficulty(chain, now, tc.header); !errors.Is(err, consensus.ErrUnknownAncestor) {
			t.Errorf("%s: CalculateDifficulty expected ErrUnknownAncestor, got %v", tc.name, err)
		}
	}

	// Nil, zero, and negative Difficulty checks
	diffCases := []*big.Int{nil, big.NewInt(0), big.NewInt(-100)}
	for _, d := range diffCases {
		h := &types.Header{
			ParentHash: genesis.Hash(),
			Number:     big.NewInt(1),
			Time:       now,
			GasLimit:   60000000,
			UncleHash:  types.EmptyUncleHash,
			Difficulty: d,
		}
		if err := rx.VerifyHeader(chain, h, false); err == nil {
			t.Errorf("VerifyHeader should reject difficulty=%v", d)
		}
		if err := rx.VerifySeal(chain, h); !errors.Is(err, ErrInvalidPoW) {
			t.Errorf("VerifySeal expected ErrInvalidPoW for difficulty=%v, got %v", d, err)
		}
	}

	// Batch VerifyHeaders with nil and malformed headers must not panic
	batch := []*types.Header{
		nil,
		{Number: nil},
		{Number: hugeNum},
	}
	abort, results := rx.VerifyHeaders(chain, batch, []bool{false, false, false})
	defer close(abort)
	for i := range batch {
		if err := <-results; !errors.Is(err, consensus.ErrInvalidNumber) {
			t.Errorf("VerifyHeaders[%d] expected ErrInvalidNumber, got %v", i, err)
		}
	}
}

func TestVerifyHeaderTimeOverflowAndDrift(t *testing.T) {
	chain := NewFakeChainReader()
	parent := &types.Header{Number: big.NewInt(0), Time: 1000}
	chain.AddHeader(parent)

	// Exact MaxFutureDrift is allowed
	if err := VerifyHeaderTime(chain, &types.Header{Number: big.NewInt(1), Time: 2000 + MaxFutureDrift}, parent, 2000); err != nil {
		t.Fatalf("Expected exact MaxFutureDrift to pass, got %v", err)
	}

	// Exceeding MaxFutureDrift by 1 second must fail
	if err := VerifyHeaderTime(chain, &types.Header{Number: big.NewInt(1), Time: 2000 + MaxFutureDrift + 1}, parent, 2000); !errors.Is(err, consensus.ErrFutureBlock) {
		t.Fatalf("Expected ErrFutureBlock, got %v", err)
	}

	// Overflow-safe check when now is near math.MaxUint64
	nearMax := uint64(math.MaxUint64) - 5
	if err := VerifyHeaderTime(chain, &types.Header{Number: big.NewInt(1), Time: nearMax + 5}, parent, nearMax); err != nil {
		t.Fatalf("Expected overflow-safe drift check near MaxUint64 to pass, got %v", err)
	}
}

func TestHashMeetsTargetZeroAlloc(t *testing.T) {
	target := DifficultyToTarget(big.NewInt(0x10000))
	targetBE := TargetToBytes32(target)

	var zeroHash [32]byte
	if !HashMeetsTarget(zeroHash, targetBE) {
		t.Fatal("Zero hash must always meet any positive target")
	}

	// Exact equality (little-endian hash matching big-endian targetBE)
	var eqHash [32]byte
	for i := 0; i < 32; i++ {
		eqHash[31-i] = targetBE[i]
	}
	if !HashMeetsTarget(eqHash, targetBE) {
		t.Fatal("Hash equal to target must meet target")
	}

	// One byte higher in MSB must fail
	overHash := eqHash
	overHash[31] = 0xff
	if HashMeetsTarget(overHash, targetBE) {
		t.Fatal("Hash above target must not meet target")
	}

	// Nil/zero target rejects even zero hash? Wait: TargetToBytes32(0) is 0x00..00, so zeroHash == 0x00..00
	// Confirm >256-bit target saturates to 0xff..ff
	maxTargetBE := TargetToBytes32(new(big.Int).Lsh(big.NewInt(1), 256))
	var maxHash [32]byte
	for i := range maxHash {
		maxHash[i] = 0xff
	}
	if !HashMeetsTarget(maxHash, maxTargetBE) {
		t.Fatal("Saturated 256-bit target must accept max hash")
	}
}

func FuzzVerifyHeader(f *testing.F) {
	f.Add(uint64(1), uint64(1030), uint64(0x10000), uint64(60000000), uint64(0), []byte("rogue"), byte(0xff))
	f.Add(uint64(0), uint64(0), uint64(0), uint64(0), uint64(0), []byte{}, byte(0x00))
	f.Add(uint64(2), uint64(math.MaxUint64), uint64(1), uint64(50000000), uint64(50000001), make([]byte, 100), byte(0x7f))

	rx := New(Config{FastMode: false, Threads: 1})
	defer rx.Close()

	chain := NewFakeChainReader()
	now := uint64(time.Now().Unix())
	genesis := &types.Header{
		Number:     big.NewInt(0),
		Time:       now - 60,
		Difficulty: big.NewInt(0x10000),
		GasLimit:   60000000,
	}
	chain.AddHeader(genesis)

	f.Fuzz(func(t *testing.T, num, ts, diff, gasLimit, gasUsed uint64, extra []byte, mixByte byte) {
		var mix common.Hash
		for i := range mix {
			mix[i] = mixByte
		}
		// Keep MSB non-zero when Difficulty >= MinDifficulty so VerifySeal exercises
		// header validation & fast target pre-check without allocating 256MB CGo caches per fuzz worker.
		if diff >= 0x10000 {
			mix[31] = 0xff
		}
		h := &types.Header{
			ParentHash: genesis.Hash(),
			Number:     new(big.Int).SetUint64(num),
			Time:       ts,
			Difficulty: new(big.Int).SetUint64(diff),
			GasLimit:   gasLimit,
			GasUsed:    gasUsed,
			Extra:      extra,
			UncleHash:  types.EmptyUncleHash,
			MixDigest:  mix,
		}
		// Must never panic on arbitrary fuzzed headers
		_ = rx.VerifyHeader(chain, h, false)
		if diff == 0 || diff >= 0x10000 || num == 0 {
			_ = rx.VerifySeal(chain, h)
		}
		_, _ = CalculateDifficulty(chain, ts, h)
	})
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) {
	return 0, errors.New("entropy source unavailable")
}

func TestSealRandFailureFallback(t *testing.T) {
	chain := NewFakeChainReader()
	genesis := &types.Header{
		Number: big.NewInt(0),
		Time:   1000,
	}
	chain.AddHeader(genesis)

	rx := New(Config{FastMode: false, Threads: 1})
	defer rx.Close()
	rx.randReader = failingReader{}

	header := &types.Header{
		ParentHash: genesis.Hash(),
		Number:     big.NewInt(1),
		Difficulty: big.NewInt(4),
		Time:       1010,
		GasLimit:   60000000,
	}
	block := types.NewBlock(header, nil, nil, nil, nil)

	results := make(chan *types.Block, 1)
	stop := make(chan struct{})
	if err := rx.Seal(chain, block, results, stop); err != nil {
		t.Fatalf("Seal failed with fallback rng: %v", err)
	}

	select {
	case sealed := <-results:
		if err := rx.VerifySeal(chain, sealed.Header()); err != nil {
			t.Fatalf("VerifySeal failed for block sealed with fallback nonce: %v", err)
		}
	case <-time.After(15 * time.Second):
		close(stop)
		t.Fatal("Mining timed out after 15 seconds")
	}
}

func TestCalcDifficultyErrorFallback(t *testing.T) {
	rx := New(Config{FastMode: false, Threads: 1})
	defer rx.Close()

	chain := NewFakeChainReader()

	// nil parent must fall back to MinDifficulty without panicking.
	if got := rx.CalcDifficulty(chain, 1000, nil); got.Cmp(MinDifficulty) != 0 {
		t.Fatalf("expected MinDifficulty %v for nil parent, got %v", MinDifficulty, got)
	}

	// Missing ancestor in chain causes CalculateDifficulty to fail; CalcDifficulty must fall back to parent.Difficulty.
	parentDiff := big.NewInt(123456)
	orphanParent := &types.Header{
		Number:     big.NewInt(10),
		Time:       2000,
		Difficulty: parentDiff,
	}
	if got := rx.CalcDifficulty(chain, 2030, orphanParent); got.Cmp(parentDiff) != 0 {
		t.Fatalf("expected parent.Difficulty fallback %v, got %v", parentDiff, got)
	}
}

func TestVerifyHeaderDeterministicClock(t *testing.T) {
	const fixedNow uint64 = 1_700_000_000

	rx := New(Config{FastMode: false, Threads: 1})
	defer rx.Close()
	rx.nowFunc = func() uint64 { return fixedNow }

	chain := NewFakeChainReader()
	var parent *types.Header
	// Build a 12-block chain (0..11) with increasing timestamps ending at fixedNow - 100.
	baseTime := fixedNow - 100 - 11*30
	for i := uint64(0); i <= 11; i++ {
		h := &types.Header{
			Number:     new(big.Int).SetUint64(i),
			Time:       baseTime + i*30,
			Difficulty: new(big.Int).Set(MinDifficulty),
			GasLimit:   60000000,
			UncleHash:  types.EmptyUncleHash,
		}
		if parent != nil {
			h.ParentHash = parent.Hash()
		}
		chain.AddHeader(h)
		parent = h
	}

	// Calculate MTP-11 for parent (blocks 1..11, median is block 6).
	mtp := CalculateMTP11(chain, parent)

	makeCandidate := func(ts uint64) *types.Header {
		diff, err := CalculateDifficulty(chain, ts, parent)
		if err != nil {
			t.Fatalf("CalculateDifficulty failed: %v", err)
		}
		return &types.Header{
			ParentHash: parent.Hash(),
			Number:     big.NewInt(12),
			Time:       ts,
			Difficulty: diff,
			GasLimit:   parent.GasLimit,
			UncleHash:  types.EmptyUncleHash,
		}
	}

	// 1. Exact MaxFutureDrift (+30s) must be accepted.
	if err := rx.verifyHeader(chain, makeCandidate(fixedNow+MaxFutureDrift), parent, false); err != nil {
		t.Fatalf("expected +30s drift to pass, got: %v", err)
	}

	// 2. +31s drift must be rejected with consensus.ErrFutureBlock.
	if err := rx.verifyHeader(chain, makeCandidate(fixedNow+MaxFutureDrift+1), parent, false); !errors.Is(err, consensus.ErrFutureBlock) {
		t.Fatalf("expected consensus.ErrFutureBlock for +31s drift, got: %v", err)
	}

	// 3. Exact MTP-11 timestamp must be rejected with ErrOlderBlockTime.
	if err := rx.verifyHeader(chain, makeCandidate(mtp), parent, false); !errors.Is(err, ErrOlderBlockTime) {
		t.Fatalf("expected ErrOlderBlockTime for timestamp == MTP-11, got: %v", err)
	}

	// 4. MTP-11 + 1s must be accepted.
	if err := rx.verifyHeader(chain, makeCandidate(mtp+1), parent, false); err != nil {
		t.Fatalf("expected MTP-11 + 1s to pass, got: %v", err)
	}

	// 5. timestamp < MTP-11 must also be rejected with ErrOlderBlockTime.
	if err := rx.verifyHeader(chain, makeCandidate(mtp-1), parent, false); !errors.Is(err, ErrOlderBlockTime) {
		t.Fatalf("expected ErrOlderBlockTime for timestamp < MTP-11, got: %v", err)
	}
}

func TestSealNonBlockingOnPreclosedStop(t *testing.T) {
	rx := New(Config{Threads: 1})
	defer rx.Close()

	// Nil chain without seed block: if Seal() ran GetContextForHeader synchronously,
	// it would return an error instead of returning nil immediately on pre-closed stop.
	hdr := &types.Header{
		Number:     big.NewInt(1),
		Difficulty: new(big.Int).Set(MinDifficulty),
		GasLimit:   50000000,
		UncleHash:  types.EmptyUncleHash,
	}
	block := types.NewBlockWithHeader(hdr)

	stop := make(chan struct{})
	close(stop)
	results := make(chan *types.Block, 1)

	done := make(chan error, 1)
	go func() {
		done <- rx.Seal(nil, block, results, stop)
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("expected Seal with pre-closed stop to return nil asynchronously, got: %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Seal blocked on pre-closed stop")
	}
}
