package randomx

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/core/types"
)

func TestEpochCalculations(t *testing.T) {
	tests := []struct {
		block    uint64
		expected uint64
	}{
		{0, 0},
		{1, 0},
		{2047, 0},
		{2048, 1},
		{2049, 1},
		{4095, 1},
		{4096, 2},
	}

	for _, tc := range tests {
		if ep := EpochNumber(tc.block); ep != tc.expected {
			t.Errorf("EpochNumber(%d) = %d, expected %d", tc.block, ep, tc.expected)
		}
	}
}

func TestSeedBlockCalculations(t *testing.T) {
	tests := []struct {
		epoch    uint64
		expected uint64
	}{
		{0, 0},
		{1, 1984}, // 2048 - 64
		{2, 4032}, // 4096 - 64
		{3, 6080}, // 6144 - 64
	}

	for _, tc := range tests {
		if sb := SeedBlockNumber(tc.epoch); sb != tc.expected {
			t.Errorf("SeedBlockNumber(%d) = %d, expected %d", tc.epoch, sb, tc.expected)
		}
	}
}

func TestEpochContextResolution(t *testing.T) {
	chain := NewFakeChainReader()

	// Add genesis block
	genesis := &types.Header{
		Number: big.NewInt(0),
		Time:   1000,
	}
	chain.AddHeader(genesis)

	em := NewEpochManager(false) // Light mode
	defer em.Close()

	ctx, err := em.GetContext(0, chain)
	if err != nil {
		t.Fatalf("Failed to get context for block 0: %v", err)
	}
	if ctx == nil {
		t.Fatal("Context for block 0 is nil")
	}
	defer ctx.DecRef()

	// Hash calculation with epoch 0 context
	hash, err := ctx.CalculateHash([]byte("Rogue Signal Test"))
	if err != nil {
		t.Fatalf("Hash calculation failed: %v", err)
	}
	if len(hash) != 32 {
		t.Fatalf("Expected 32-byte hash, got %d", len(hash))
	}
}

func TestEpochSeedReorgInvalidation(t *testing.T) {
	chain := NewFakeChainReader()

	// Seed block for epoch 1 is block 1984 (2048 - 64)
	seedBlockA := &types.Header{
		Number: big.NewInt(1984),
		Time:   10000,
		Extra:  []byte("branch-A"),
	}
	chain.AddHeader(seedBlockA)

	em := NewEpochManager(false)
	defer em.Close()

	seed1, err := em.GetSeed(1, chain)
	if err != nil {
		t.Fatalf("GetSeed(1) failed on branch A: %v", err)
	}
	if seed1 != seedBlockA.Hash() {
		t.Fatalf("Expected seed %s, got %s", seedBlockA.Hash().Hex(), seed1.Hex())
	}

	// Simulate a legitimate chain reorganization replacing block 1984 with branch B
	chainReorg := NewFakeChainReader()
	seedBlockB := &types.Header{
		Number: big.NewInt(1984),
		Time:   10005,
		Extra:  []byte("branch-B"),
	}
	chainReorg.AddHeader(seedBlockB)

	seed2, err := em.GetSeed(1, chainReorg)
	if err != nil {
		t.Fatalf("GetSeed(1) failed after reorg to branch B: %v", err)
	}
	if seed2 != seedBlockB.Hash() {
		t.Fatalf("EpochManager did not invalidate stale seed after reorg: got %s, want %s", seed2.Hex(), seedBlockB.Hash().Hex())
	}
}

func TestSideForkEpochSeedResolution(t *testing.T) {
	chain := NewFakeChainReader()

	// Canonical branch A from block 1983 -> 2048
	commonAncestor := &types.Header{
		Number: big.NewInt(1983),
		Time:   10000,
	}
	chain.AddHeader(commonAncestor)

	currA := commonAncestor
	var seedA *types.Header
	for n := int64(1984); n <= 2048; n++ {
		h := &types.Header{
			ParentHash: currA.Hash(),
			Number:     big.NewInt(n),
			Time:       currA.Time + 30,
			Extra:      []byte("canonical-branch-A"),
		}
		chain.AddHeader(h)
		if n == 1984 {
			seedA = h
		}
		currA = h
	}

	// Side fork B diverges at block 1984 (the epoch 1 seed block) and extends to block 2048.
	// We store fork B headers in a map by hash while GetHeaderByNumber still returns canonical branch A.
	sideHeaders := make(map[uint64]*types.Header)
	currB := commonAncestor
	var seedB *types.Header
	for n := int64(1984); n <= 2048; n++ {
		h := &types.Header{
			ParentHash: currB.Hash(),
			Number:     big.NewInt(n),
			Time:       currB.Time + 29,
			Extra:      []byte("competing-fork-B"),
		}
		chain.headers[h.Hash()] = h
		sideHeaders[uint64(n)] = h
		if n == 1984 {
			seedB = h
		}
		currB = h
	}

	// Ensure GetHeaderByNumber(1984) returns canonical seedA:
	reader := &headerChainReader{
		ChainHeaderReader: chain,
		byHash:            chain.headers,
		byNumber: map[uint64]*types.Header{
			1983: commonAncestor,
			1984: seedA,
		},
	}

	em := NewEpochManager(false)
	defer em.Close()

	// Canonical block 2048 must resolve seedA
	resolvedA, err := em.ResolveSeedForHeader(currA, reader)
	if err != nil {
		t.Fatalf("ResolveSeedForHeader failed for canonical branch A: %v", err)
	}
	if resolvedA != seedA.Hash() {
		t.Fatalf("Expected canonical seed %s, got %s", seedA.Hash().Hex(), resolvedA.Hex())
	}

	// Side-fork block 2048 must resolve seedB by walking its own parent chain back to 1984
	resolvedB, err := em.ResolveSeedForHeader(sideHeaders[2048], reader)
	if err != nil {
		t.Fatalf("ResolveSeedForHeader failed for side fork B: %v", err)
	}
	if resolvedB != seedB.Hash() {
		t.Fatalf("Expected side-fork seed %s, got %s", seedB.Hash().Hex(), resolvedB.Hex())
	}

	// Subsequent call for the same header must hit the O(1) resolvedSeeds cache
	if len(em.resolvedSeeds) == 0 {
		t.Fatal("Expected resolvedSeeds cache to be populated")
	}
	resolvedBCached, err := em.ResolveSeedForHeader(sideHeaders[2048], nil)
	if err != nil {
		t.Fatalf("Cached ResolveSeedForHeader failed: %v", err)
	}
	if resolvedBCached != seedB.Hash() {
		t.Fatalf("Expected cached side-fork seed %s, got %s", seedB.Hash().Hex(), resolvedBCached.Hex())
	}
}

func TestRxContextRefCountAndVMPool(t *testing.T) {
	chain := NewFakeChainReader()
	genesis := &types.Header{
		Number: big.NewInt(0),
		Time:   1000,
	}
	chain.AddHeader(genesis)

	em := NewEpochManager(false)
	defer em.Close()

	ctx, err := em.GetContext(0, chain)
	if err != nil {
		t.Fatalf("GetContext failed: %v", err)
	}

	// Prewarm 2 VMs and verify pool telemetry
	ctx.PrewarmVMs(2)
	totalVMs, activeVMs, allocatedMB := em.VMPoolStats()
	if totalVMs != 2 {
		t.Fatalf("Expected 2 prewarmed VMs, got %d", totalVMs)
	}
	if activeVMs != 0 {
		t.Fatalf("Expected 0 active VMs when idle, got %d", activeVMs)
	}
	if allocatedMB < 256 {
		t.Fatalf("Expected at least 256 MB allocated in light mode, got %d", allocatedMB)
	}

	// Acquire a VM and compute hash
	vm, err := ctx.AcquireVM()
	if err != nil {
		t.Fatalf("AcquireVM failed: %v", err)
	}
	_, activeDuring, _ := em.VMPoolStats()
	if activeDuring != 1 {
		t.Fatalf("Expected 1 active VM while acquired, got %d", activeDuring)
	}
	h, err := ctx.CalculateHashWithVM(vm, []byte("vm-pool-test"))
	if err != nil {
		t.Fatalf("CalculateHashWithVM failed: %v", err)
	}
	if len(h) != 32 {
		t.Fatalf("Expected 32-byte hash, got %d", len(h))
	}
	ctx.ReleaseVM(vm)

	// Close context while still holding a reference; memory must stay alive until DecRef
	ctx.Close()
	if ctx.refs.Load() <= 0 {
		t.Fatal("Expected refcount > 0 before DecRef")
	}

	// Final DecRef triggers lazy release and purges VMs
	ctx.DecRef()
	if ctx.refs.Load() != 0 {
		t.Fatalf("Expected refcount 0 after DecRef, got %d", ctx.refs.Load())
	}
}

func TestResolveSeedForHeaderMissingAncestor(t *testing.T) {
	chain := NewFakeChainReader()
	// Provide canonical seed at 1984 so GetSeed would succeed if mistakenly used as fallback
	canonicalSeed := &types.Header{
		Number: big.NewInt(1984),
		Time:   1000,
	}
	chain.AddHeader(canonicalSeed)

	// Header at 2048 whose parent chain is missing
	orphan := &types.Header{
		ParentHash: common.HexToHash("0xdeadbeef"),
		Number:     big.NewInt(2048),
		Time:       2000,
	}

	em := NewEpochManager(false)
	defer em.Close()

	_, err := em.ResolveSeedForHeader(orphan, chain)
	if err != consensus.ErrUnknownAncestor {
		t.Fatalf("Expected ErrUnknownAncestor for orphan branch missing seed ancestor, got %v", err)
	}
}
