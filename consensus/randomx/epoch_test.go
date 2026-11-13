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

func TestEpochBoundaryForkAndReorgSeedIsolation(t *testing.T) {
	chain := NewFakeChainReader()

	// Build shared ancestor chain up to block 1983 (just before Epoch 1 seed block 1984 = 2048 - 64)
	genesis := &types.Header{Number: big.NewInt(0), Time: 1000}
	chain.AddHeader(genesis)
	prev := genesis
	for i := uint64(1); i < EpochLength-SeedLag; i++ {
		h := &types.Header{
			ParentHash: prev.Hash(),
			Number:     big.NewInt(int64(i)),
			Time:       prev.Time + 30,
		}
		chain.AddHeader(h)
		prev = h
	}

	// Fork A and Fork B diverge at SeedBlockNumber(1) = 1984 and cross Epoch 1 boundary (2048 -> 2050)
	buildBranch := func(tag string, start *types.Header) (*types.Header, *types.Header) {
		var seedHdr, tipHdr *types.Header
		curr := start
		for i := EpochLength - SeedLag; i <= EpochLength+2; i++ {
			h := &types.Header{
				ParentHash: curr.Hash(),
				Number:     big.NewInt(int64(i)),
				Time:       curr.Time + 30,
				Extra:      []byte(tag),
			}
			chain.AddHeader(h)
			if i == EpochLength-SeedLag {
				seedHdr = h
			}
			tipHdr = h
			curr = h
		}
		return seedHdr, tipHdr
	}

	seedA, tipA := buildBranch("fork-A", prev)
	seedB, tipB := buildBranch("fork-B", prev)

	if seedA.Hash() == seedB.Hash() {
		t.Fatal("Fork A and Fork B seed hashes must differ")
	}

	em := NewEpochManager(false)
	defer em.Close()

	// Resolve seeds for both competing tips in Epoch 1
	resolvedA, err := em.ResolveSeedForHeader(tipA, chain)
	if err != nil {
		t.Fatalf("ResolveSeedForHeader(tipA) failed: %v", err)
	}
	if resolvedA != seedA.Hash() {
		t.Fatalf("Fork A seed mismatch: have %s, want %s", resolvedA.Hex(), seedA.Hash().Hex())
	}

	resolvedB, err := em.ResolveSeedForHeader(tipB, chain)
	if err != nil {
		t.Fatalf("ResolveSeedForHeader(tipB) failed: %v", err)
	}
	if resolvedB != seedB.Hash() {
		t.Fatalf("Fork B seed mismatch: have %s, want %s", resolvedB.Hex(), seedB.Hash().Hex())
	}

	// Verify both branches obtain distinct RxContext instances with matching seeds
	ctxA, err := em.GetContextForHeader(tipA, chain)
	if err != nil {
		t.Fatalf("GetContextForHeader(tipA) failed: %v", err)
	}
	defer ctxA.DecRef()

	ctxB, err := em.GetContextForHeader(tipB, chain)
	if err != nil {
		t.Fatalf("GetContextForHeader(tipB) failed: %v", err)
	}
	defer ctxB.DecRef()

	if ctxA == ctxB {
		t.Fatal("Competing forks with different epoch seeds must not share RxContext")
	}
}

func TestResolvedSeedCacheCapacity(t *testing.T) {
	if MaxResolvedSeedCache < 2048 {
		t.Fatalf("expected MaxResolvedSeedCache >= 2048 for batch sync, got %d", MaxResolvedSeedCache)
	}
	if MaxCachedContextsFast != 2 {
		t.Fatalf("expected MaxCachedContextsFast == 2 to prevent OOM in Fast Mode, got %d", MaxCachedContextsFast)
	}

	em := NewEpochManager(false)
	defer em.Close()

	// Insert 300 entries (> old limit of 128) and verify none are prematurely evicted.
	for i := 0; i < 300; i++ {
		h := common.BigToHash(big.NewInt(int64(i + 1)))
		s := common.BigToHash(big.NewInt(int64(i + 10000)))
		em.cacheResolvedSeed(h, s)
	}

	em.mu.RLock()
	gotLen := len(em.resolvedSeeds)
	em.mu.RUnlock()
	if gotLen != 300 {
		t.Fatalf("expected 300 cached resolved seeds, got %d", gotLen)
	}
}

func TestGetLightContextForHeader(t *testing.T) {
	chain := NewFakeChainReader()
	genesis := &types.Header{
		Number:     big.NewInt(0),
		Time:       1700000000,
		Difficulty: big.NewInt(0x10000),
	}
	chain.AddHeader(genesis)

	em := NewEpochManager(false)
	defer em.Close()

	// Seed a synthetic FastMode-tagged context in cache for epoch 0 to verify
	// GetLightContextForHeader reuses an existing cached context when available.
	seed0 := genesis.Hash()
	em.mu.Lock()
	em.epochSeeds[0] = seed0
	em.mu.Unlock()

	h1 := &types.Header{
		ParentHash: genesis.Hash(),
		Number:     big.NewInt(1),
		Time:       genesis.Time + 30,
		Difficulty: big.NewInt(0x10000),
	}
	chain.AddHeader(h1)

	ctxLight, err := em.GetLightContextForHeader(h1, chain)
	if err != nil {
		t.Fatalf("GetLightContextForHeader failed: %v", err)
	}
	defer ctxLight.DecRef()

	if ctxLight.fastMode {
		t.Fatal("expected GetLightContextForHeader to initialize a Light Mode context when none is cached")
	}

	// Simulate FastMode flag enabled on EpochManager; GetLightContextForHeader must still
	// return a Light Mode context for uncached seeds rather than allocating a 2.08GB dataset.
	em.SetFastMode(true)
	if !em.IsFastMode() {
		t.Fatal("expected EpochManager FastMode to be true")
	}

	ctxReuse, err := em.GetLightContextForHeader(h1, chain)
	if err != nil {
		t.Fatalf("GetLightContextForHeader reuse failed: %v", err)
	}
	defer ctxReuse.DecRef()

	if ctxReuse != ctxLight {
		t.Fatal("expected GetLightContextForHeader to reuse already-cached Light context")
	}

	// Verify historical Light isolation: loading historical Light contexts (e.g. from peer side-chains
	// or historical epoch verification) must stay bounded in historicalLight (<= MaxCachedHistoricalLight)
	// and MUST NEVER evict active or precomputed FastMode contexts (seedA and seedB).
	seedA := common.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111")
	seedB := common.HexToHash("0x2222222222222222222222222222222222222222222222222222222222222222")
	seedPast1 := common.HexToHash("0x3333333333333333333333333333333333333333333333333333333333333333")
	seedPast2 := common.HexToHash("0x4444444444444444444444444444444444444444444444444444444444444444")
	seedPast3 := common.HexToHash("0x5555555555555555555555555555555555555555555555555555555555555555")
	em.mu.Lock()
	em.contexts[seedA] = &RxContext{fastMode: true, vmPool: make(chan *VM, 1)}
	em.contextEpochs[seedA] = 10
	em.contexts[seedB] = &RxContext{fastMode: true, vmPool: make(chan *VM, 1)}
	em.contextEpochs[seedB] = 11
	em.mu.Unlock()

	for _, pastSeed := range []common.Hash{seedPast1, seedPast2, seedPast3} {
		ctxPast, err := em.getContextBySeed(5, pastSeed, false)
		if err != nil {
			t.Fatalf("getContextBySeed(%x) failed: %v", pastSeed[:4], err)
		}
		ctxPast.DecRef()
	}

	em.mu.RLock()
	cachedFastCount := len(em.contexts)
	_, hasPrevFast := em.contexts[seedA]
	_, hasCurrentFast := em.contexts[seedB]
	histCount := len(em.historicalLight)
	_, hasOldestHist := em.historicalLight[seedPast1]
	_, hasNewestHist := em.historicalLight[seedPast3]
	em.mu.RUnlock()

	if cachedFastCount != 2 || !hasPrevFast || !hasCurrentFast {
		t.Fatalf("historical Light verification evicted active FastMode contexts: count=%d hasA=%v hasB=%v", cachedFastCount, hasPrevFast, hasCurrentFast)
	}
	if histCount != MaxCachedHistoricalLight {
		t.Fatalf("expected historicalLight cache size=%d, got %d", MaxCachedHistoricalLight, histCount)
	}
	if hasOldestHist || !hasNewestHist {
		t.Fatalf("expected FIFO eviction on historicalLight (hasOldest=%v hasNewest=%v)", hasOldestHist, hasNewestHist)
	}
}
