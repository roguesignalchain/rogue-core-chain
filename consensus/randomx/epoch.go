package randomx

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/core/types"
)

const (
	// EpochLength is the number of blocks per RandomX epoch (2048 blocks, ~17.07 hours at 30s/block).
	EpochLength = uint64(2048)

	// SeedLag is the number of blocks prior to epoch transition when the new seed is locked (64 blocks, ~32 minutes at 30s/block).
	SeedLag = uint64(64)

	// CacheRetentionBlocks is the number of blocks to retain the previous epoch cache to handle chain reorgs.
	CacheRetentionBlocks = uint64(128)

	// MaxCachedContexts is the maximum number of RandomX contexts retained in memory simultaneously in Light Mode.
	MaxCachedContexts = 3

	// MaxCachedContextsFast is the maximum number of RandomX contexts retained in Fast Mode (~2.08GB each) to prevent OOM.
	MaxCachedContextsFast = 2

	// MaxCachedHistoricalLight is the maximum number of historical Light Mode (~256MB each) contexts
	// cached separately so old-epoch sync/peer headers never evict active 2.08GB FastDataset contexts.
	MaxCachedHistoricalLight = 4

	// MaxResolvedSeedCache is the capacity of the O(1) header-to-seed LRU cache (~160KB RAM, covers 2 full epochs).
	MaxResolvedSeedCache = 4096
)

type inflightKey struct {
	seed     common.Hash
	fastMode bool
}

type seedInit struct {
	done chan struct{}
	ctx  *RxContext
	err  error
}

// EpochManager coordinates RandomX cache/dataset lifecycle across epochs with precomputation and reorg/fork support.
type EpochManager struct {
	fastMode     bool
	minVMPoolCap int
	mu           sync.RWMutex

	// contexts caches RxContext by 32-byte seed hash so competing forks at epoch boundaries do not thrash each other.
	contexts map[common.Hash]*RxContext

	// contextEpochs tracks the highest epoch number associated with each cached seed for eviction.
	contextEpochs map[common.Hash]uint64

	// historicalLight caches Light Mode (~256MB) contexts for out-of-window historical headers separately
	// from contexts so verifying old epochs can never evict active/precomputed FastDatasets.
	historicalLight map[common.Hash]*RxContext
	historicalOrder []common.Hash

	// epochSeeds maps epoch number to the most recently resolved canonical 32-byte seed.
	epochSeeds map[uint64]common.Hash

	// resolvedSeeds provides O(1) cached header-hash -> seed-hash lookup for side-forks and recent headers.
	resolvedSeeds map[common.Hash]common.Hash
	resolvedOrder []common.Hash

	// inflight deduplicates concurrent context allocations without holding the global lock during CGo allocation.
	inflight map[inflightKey]*seedInit

	// allocSem limits concurrent CGo dataset/cache initializations to prevent memory exhaustion DoS.
	allocSem chan struct{}

	// fastAllocSem serializes ~2.34GB FastDataset builds so overlapping epoch transitions do not spike RAM.
	fastAllocSem chan struct{}

	closed atomic.Bool
}

// NewEpochManager creates a new epoch coordinator.
func NewEpochManager(fastMode bool) *EpochManager {
	return &EpochManager{
		fastMode:        fastMode,
		contexts:        make(map[common.Hash]*RxContext),
		contextEpochs:   make(map[common.Hash]uint64),
		historicalLight: make(map[common.Hash]*RxContext, MaxCachedHistoricalLight),
		historicalOrder: make([]common.Hash, 0, MaxCachedHistoricalLight),
		epochSeeds:      make(map[uint64]common.Hash),
		resolvedSeeds:   make(map[common.Hash]common.Hash, MaxResolvedSeedCache),
		resolvedOrder:   make([]common.Hash, 0, MaxResolvedSeedCache),
		inflight:        make(map[inflightKey]*seedInit),
		allocSem:        make(chan struct{}, 2),
		fastAllocSem:    make(chan struct{}, 1),
	}
}

// SetFastMode updates the memory mode (Light Mode ~256MB vs Fast Mode ~2.08GB) dynamically.
func (em *EpochManager) SetFastMode(fastMode bool) {
	em.mu.Lock()
	defer em.mu.Unlock()
	if em.fastMode == fastMode {
		return
	}
	em.fastMode = fastMode
}

// SetMinVMPoolCap updates the minimum VM pool capacity for newly allocated contexts.
func (em *EpochManager) SetMinVMPoolCap(poolCap int) {
	em.mu.Lock()
	defer em.mu.Unlock()
	if poolCap > 0 {
		em.minVMPoolCap = poolCap
	}
}

// IsFastMode returns true if Fast Mode (~2.08GB dataset) is enabled.
func (em *EpochManager) IsFastMode() bool {
	em.mu.RLock()
	defer em.mu.RUnlock()
	return em.fastMode
}

// EpochNumber calculates the epoch index for a given block number.
func EpochNumber(blockNum uint64) uint64 {
	return blockNum / EpochLength
}

// SeedBlockNumber calculates which block's hash serves as the seed for a given epoch.
// For Epoch 0: Block 0 (genesis).
// For Epoch E (E > 0): Block (E * EpochLength - SeedLag).
func SeedBlockNumber(epoch uint64) uint64 {
	if epoch == 0 {
		return 0
	}
	target := epoch * EpochLength
	if target < SeedLag {
		return 0
	}
	return target - SeedLag
}

// GetSeed retrieves or resolves the canonical seed hash for the given epoch.
// It cross-checks against the canonical header at SeedBlockNumber(epoch) so that if a
// chain reorganization modifies the seed block, any stale cached seed mapping is updated.
func (em *EpochManager) GetSeed(epoch uint64, chain consensus.ChainHeaderReader) (common.Hash, error) {
	seedNum := SeedBlockNumber(epoch)
	var headerHash common.Hash
	if chain != nil {
		if header := chain.GetHeaderByNumber(seedNum); header != nil {
			headerHash = header.Hash()
		}
	}

	em.mu.RLock()
	cachedSeed, exists := em.epochSeeds[epoch]
	em.mu.RUnlock()

	if exists && (headerHash == (common.Hash{}) || cachedSeed == headerHash) {
		return cachedSeed, nil
	}

	if headerHash == (common.Hash{}) {
		return common.Hash{}, fmt.Errorf("seed block %d not found for epoch %d", seedNum, epoch)
	}

	em.mu.Lock()
	em.epochSeeds[epoch] = headerHash
	em.mu.Unlock()
	return headerHash, nil
}

func (em *EpochManager) cacheResolvedSeedLocked(headerHash, seed common.Hash) {
	if _, exists := em.resolvedSeeds[headerHash]; exists {
		return
	}
	if len(em.resolvedOrder) >= MaxResolvedSeedCache {
		oldest := em.resolvedOrder[0]
		em.resolvedOrder = em.resolvedOrder[1:]
		delete(em.resolvedSeeds, oldest)
	}
	em.resolvedSeeds[headerHash] = seed
	em.resolvedOrder = append(em.resolvedOrder, headerHash)
}

func (em *EpochManager) cacheResolvedSeed(headerHash, seed common.Hash) {
	em.mu.Lock()
	em.cacheResolvedSeedLocked(headerHash, seed)
	em.mu.Unlock()
}

func (em *EpochManager) cacheResolvedSeeds(seed common.Hash, hashes ...common.Hash) {
	em.mu.Lock()
	for _, h := range hashes {
		em.cacheResolvedSeedLocked(h, seed)
	}
	em.mu.Unlock()
}

// ResolveSeedForHeader resolves the exact RandomX seed hash for a specific header by following
// its ancestor chain back to SeedBlockNumber(epoch). Uses an O(1) bounded cache (~160KB RAM)
// to avoid repeated parent walks on consecutive or side-fork blocks.
func (em *EpochManager) ResolveSeedForHeader(header *types.Header, chain consensus.ChainHeaderReader) (common.Hash, error) {
	if header == nil || header.Number == nil || !header.Number.IsUint64() {
		return common.Hash{}, consensus.ErrInvalidNumber
	}
	blockNum := header.Number.Uint64()
	epoch := EpochNumber(blockNum)
	seedNum := SeedBlockNumber(epoch)

	if seedNum == 0 {
		return em.GetSeed(epoch, chain)
	}

	headerHash := header.Hash()
	if blockNum == seedNum {
		em.cacheResolvedSeed(headerHash, headerHash)
		return headerHash, nil
	}

	// O(1) fast path: check if this header or its parent in the same epoch is already cached
	em.mu.RLock()
	if cached, ok := em.resolvedSeeds[headerHash]; ok {
		em.mu.RUnlock()
		return cached, nil
	}
	if blockNum > 0 && blockNum-1 > seedNum && EpochNumber(blockNum-1) == epoch {
		if parentSeed, ok := em.resolvedSeeds[header.ParentHash]; ok {
			em.mu.RUnlock()
			em.cacheResolvedSeed(headerHash, parentSeed)
			return parentSeed, nil
		}
	}
	em.mu.RUnlock()

	if chain == nil {
		return em.GetSeed(epoch, chain)
	}

	// Walk parent hashes back to seedNum, recording same-epoch ancestors for batch caching
	// and periodically checking resolvedSeeds to short-circuit without per-step lock contention.
	if blockNum > seedNum {
		currHash := header.ParentHash
		currNum := blockNum - 1
		walked := make([]common.Hash, 0, 16)
		walked = append(walked, headerHash)

		for currNum >= seedNum {
			if currNum == seedNum {
				curr := chain.GetHeader(currHash, currNum)
				if curr == nil {
					break
				}
				seed := curr.Hash()
				em.cacheResolvedSeeds(seed, walked...)
				return seed, nil
			}
			if EpochNumber(currNum) == epoch && len(walked)%16 == 0 {
				em.mu.RLock()
				cached, ok := em.resolvedSeeds[currHash]
				em.mu.RUnlock()
				if ok {
					em.cacheResolvedSeeds(cached, walked...)
					return cached, nil
				}
			}

			curr := chain.GetHeader(currHash, currNum)
			if curr == nil {
				break
			}
			if EpochNumber(currNum) == epoch {
				walked = append(walked, currHash)
			}
			if currNum == 0 {
				break
			}
			currHash = curr.ParentHash
			currNum--
		}
	}

	return common.Hash{}, consensus.ErrUnknownAncestor
}

// GetContext returns the execution context for a given block number on the canonical chain.
func (em *EpochManager) GetContext(blockNum uint64, chain consensus.ChainHeaderReader) (*RxContext, error) {
	epoch := EpochNumber(blockNum)
	seed, err := em.GetSeed(epoch, chain)
	if err != nil {
		return nil, err
	}
	return em.getContextBySeed(epoch, seed, em.IsFastMode())
}

// GetContextForHeader returns the execution context for a specific header, resolving its seed
// along the header's own ancestor branch so side-fork blocks across epoch boundaries verify accurately.
func (em *EpochManager) GetContextForHeader(header *types.Header, chain consensus.ChainHeaderReader) (*RxContext, error) {
	return em.getContextForHeaderMode(header, chain, em.IsFastMode(), false)
}

// GetLightContextForHeader returns a Light Mode (~256MB) execution context for verifying
// historical-epoch headers without allocating a 2.08GB FastDataset or evicting active mining epochs.
func (em *EpochManager) GetLightContextForHeader(header *types.Header, chain consensus.ChainHeaderReader) (*RxContext, error) {
	return em.getContextForHeaderMode(header, chain, false, true)
}

// GetVerifyContextForHeader returns an execution context for single-block PoW verification.
// If an active FastDataset or LightCache for the header's seed is already cached, it is reused
// with zero allocations; otherwise, only a Light Mode (~256MB) context is initialized so
// unverified side-fork headers can never trigger 2.08GB FastDataset allocations or evict mining epochs.
func (em *EpochManager) GetVerifyContextForHeader(header *types.Header, chain consensus.ChainHeaderReader) (*RxContext, error) {
	return em.getContextForHeaderMode(header, chain, false, true)
}

func (em *EpochManager) getContextForHeaderMode(header *types.Header, chain consensus.ChainHeaderReader, fastMode, historical bool) (*RxContext, error) {
	if header == nil || header.Number == nil || !header.Number.IsUint64() {
		return nil, consensus.ErrInvalidNumber
	}
	blockNum := header.Number.Uint64()
	epoch := EpochNumber(blockNum)
	seed, err := em.ResolveSeedForHeader(header, chain)
	if err != nil {
		return nil, err
	}
	ctx, err := em.getContextBySeedWithMode(epoch, seed, fastMode, historical)
	if err != nil {
		return nil, err
	}
	// Apply CacheRetentionBlocks window (128 blocks past epoch boundary) to retire superseded primary contexts.
	if !historical && blockNum >= EpochLength && blockNum%EpochLength >= CacheRetentionBlocks {
		em.mu.Lock()
		em.evictSupersededEpochsLocked(epoch, seed)
		em.mu.Unlock()
	}
	return ctx, nil
}

func (em *EpochManager) evictSupersededEpochsLocked(currentEpoch uint64, protectedSeed common.Hash) {
	for s, ep := range em.contextEpochs {
		if s == protectedSeed {
			continue
		}
		if ep < currentEpoch {
			if staleCtx, ok := em.contexts[s]; ok && staleCtx != nil {
				staleCtx.Close()
				delete(em.contexts, s)
			}
			delete(em.contextEpochs, s)
		}
	}
}

func (em *EpochManager) getContextBySeed(epoch uint64, seed common.Hash, targetFast bool) (*RxContext, error) {
	return em.getContextBySeedWithMode(epoch, seed, targetFast, !targetFast && em.IsFastMode())
}

// lookupCachedContextLocked returns an active reference to an existing cached context matching targetFast
// across primary contexts and (for Light queries) the bounded historicalLight cache. Caller must hold mu.
func (em *EpochManager) lookupCachedContextLocked(seed common.Hash, targetFast bool) (ctx *RxContext, fromHistorical bool) {
	if c, exists := em.contexts[seed]; exists && c != nil && (c.fastMode == targetFast || (!targetFast && c.fastMode)) && c.IncRef() {
		return c, false
	}
	if !targetFast {
		if c, exists := em.historicalLight[seed]; exists && c != nil && c.IncRef() {
			needsPromote := len(em.historicalOrder) > 1 && em.historicalOrder[len(em.historicalOrder)-1] != seed
			return c, needsPromote
		}
	}
	return nil, false
}

// promoteHistoricalLightLocked moves seed to the most-recently-used position in historicalOrder.
// Caller must hold mu.Lock().
func (em *EpochManager) promoteHistoricalLightLocked(seed common.Hash) {
	if _, exists := em.historicalLight[seed]; !exists {
		return
	}
	n := len(em.historicalOrder)
	if n <= 1 || em.historicalOrder[n-1] == seed {
		return
	}
	for i, s := range em.historicalOrder {
		if s == seed {
			em.historicalOrder = append(em.historicalOrder[:i], em.historicalOrder[i+1:]...)
			em.historicalOrder = append(em.historicalOrder, seed)
			return
		}
	}
}

// removeHistoricalLightLocked deletes seed from the historical Light cache and evicts/closes it if present.
// Caller must hold mu.Lock().
func (em *EpochManager) removeHistoricalLightLocked(seed common.Hash) {
	if ctx, exists := em.historicalLight[seed]; exists {
		if ctx != nil {
			ctx.Close()
		}
		delete(em.historicalLight, seed)
	}
	for i, s := range em.historicalOrder {
		if s == seed {
			em.historicalOrder = append(em.historicalOrder[:i], em.historicalOrder[i+1:]...)
			break
		}
	}
}

// storeHistoricalLightLocked caches a bounded Light Mode context for historical verification without
// ever disturbing active FastDataset or Light contexts in em.contexts. Caller must hold mu.Lock().
func (em *EpochManager) storeHistoricalLightLocked(seed common.Hash, newCtx *RxContext) {
	if oldCtx, exists := em.historicalLight[seed]; exists {
		if oldCtx != nil && oldCtx != newCtx {
			oldCtx.Close()
		}
		for i, s := range em.historicalOrder {
			if s == seed {
				em.historicalOrder = append(em.historicalOrder[:i], em.historicalOrder[i+1:]...)
				break
			}
		}
	}
	em.historicalLight[seed] = newCtx
	em.historicalOrder = append(em.historicalOrder, seed)

	for len(em.historicalLight) > MaxCachedHistoricalLight && len(em.historicalOrder) > 0 {
		oldest := em.historicalOrder[0]
		em.historicalOrder = em.historicalOrder[1:]
		if oldest == seed {
			continue
		}
		if staleCtx, ok := em.historicalLight[oldest]; ok && staleCtx != nil {
			staleCtx.Close()
		}
		delete(em.historicalLight, oldest)
	}
}

func (em *EpochManager) getContextBySeedWithMode(epoch uint64, seed common.Hash, targetFast, historical bool) (*RxContext, error) {
	for {
		em.mu.RLock()
		ctx, needsPromote := em.lookupCachedContextLocked(seed, targetFast)
		minPoolCap := em.minVMPoolCap
		em.mu.RUnlock()
		if ctx != nil {
			if needsPromote {
				em.mu.Lock()
				em.promoteHistoricalLightLocked(seed)
				em.mu.Unlock()
			}
			return ctx, nil
		}

		em.mu.Lock()
		if em.closed.Load() {
			em.mu.Unlock()
			return nil, errors.New("epoch manager is closed")
		}
		if ctx, needsPromote := em.lookupCachedContextLocked(seed, targetFast); ctx != nil {
			if needsPromote {
				em.promoteHistoricalLightLocked(seed)
			}
			em.mu.Unlock()
			return ctx, nil
		}

		key := inflightKey{seed: seed, fastMode: targetFast}
		if inflight, ok := em.inflight[key]; ok {
			em.mu.Unlock()
			<-inflight.done
			if inflight.err != nil {
				return nil, inflight.err
			}
			if inflight.ctx != nil && inflight.ctx.IncRef() {
				return inflight.ctx, nil
			}
			continue
		}

		initCall := &seedInit{done: make(chan struct{})}
		em.inflight[key] = initCall
		em.mu.Unlock()

		// Allocate RandomX cache/dataset outside the lock so concurrent RPCs and verifications never block.
		// fastAllocSem serializes ~2.34GB FastDataset allocations; allocSem bounds total allocations.
		if targetFast {
			em.fastAllocSem <- struct{}{}
		}
		em.allocSem <- struct{}{}
		newCtx, err := NewRxContextWithPoolCap(seed.Bytes(), targetFast, minPoolCap)
		<-em.allocSem
		if targetFast {
			<-em.fastAllocSem
		}

		em.mu.Lock()
		delete(em.inflight, key)
		if em.closed.Load() {
			if newCtx != nil {
				newCtx.Close()
			}
			initCall.err = errors.New("epoch manager is closed")
			close(initCall.done)
			em.mu.Unlock()
			return nil, initCall.err
		}
		if err != nil {
			initCall.err = fmt.Errorf("failed to create RxContext for epoch %d: %w", epoch, err)
			close(initCall.done)
			em.mu.Unlock()
			return nil, initCall.err
		}

		// Acquire caller's reference before storing/publishing the context
		newCtx.IncRef()

		if historical && !targetFast {
			// Store in dedicated bounded historical Light cache so active 2.08GB FastDatasets are never evicted.
			em.storeHistoricalLightLocked(seed, newCtx)
		} else {
			// Retire any stale historical Light copy of the same seed before publishing into primary contexts.
			em.removeHistoricalLightLocked(seed)

			// If another context was stored for this seed (e.g. previous mode), retire it lazily.
			if oldCtx, hadOld := em.contexts[seed]; hadOld && oldCtx != nil && oldCtx != newCtx {
				oldCtx.Close()
			}
			em.contexts[seed] = newCtx
			if prevEp, ok := em.contextEpochs[seed]; !ok || epoch > prevEp {
				em.contextEpochs[seed] = epoch
			}

			// Evict stale contexts older than epoch - 1
			for s, ep := range em.contextEpochs {
				if s == seed {
					continue
				}
				if ep+1 < epoch {
					if staleCtx, ok := em.contexts[s]; ok && staleCtx != nil {
						staleCtx.Close()
						delete(em.contexts, s)
					}
					delete(em.contextEpochs, s)
				}
			}

			// Bound primary cached contexts (3 in Light Mode, 2 in Fast Mode to prevent >4.2GB RAM spikes),
			// ensuring we never evict the newly created seed.
			maxContexts := MaxCachedContexts
			if targetFast || em.fastMode {
				maxContexts = MaxCachedContextsFast
			}
			for len(em.contexts) > maxContexts {
				var oldestSeed common.Hash
				var lowestEpoch uint64 = ^uint64(0)
				for s, ep := range em.contextEpochs {
					if s == seed {
						continue // Protect current context from being evicted
					}
					if ep < lowestEpoch {
						lowestEpoch = ep
						oldestSeed = s
					}
				}
				if lowestEpoch == ^uint64(0) || oldestSeed == (common.Hash{}) {
					break
				}
				if staleCtx, ok := em.contexts[oldestSeed]; ok && staleCtx != nil {
					staleCtx.Close()
					delete(em.contexts, oldestSeed)
				}
				delete(em.contextEpochs, oldestSeed)
			}
		}

		initCall.ctx = newCtx
		close(initCall.done)
		em.mu.Unlock()

		return newCtx, nil
	}
}

// PrecomputeNextEpoch initiates background initialization of the upcoming epoch cache
// once blockNum enters the SeedLag window (e.g. block 2048 - 64 = 1984).
func (em *EpochManager) PrecomputeNextEpoch(blockNum uint64, chain consensus.ChainHeaderReader) {
	if chain == nil || em.closed.Load() {
		return
	}
	epoch := EpochNumber(blockNum)
	nextEpoch := epoch + 1
	transitionBlock := nextEpoch * EpochLength
	seedBlock := transitionBlock - SeedLag

	if blockNum >= seedBlock && blockNum < transitionBlock {
		seedHeader := chain.GetHeaderByNumber(seedBlock)
		if seedHeader == nil {
			return
		}
		seed := seedHeader.Hash()

		em.mu.RLock()
		ctx, exists := em.contexts[seed]
		targetFast := em.fastMode
		_, isInflight := em.inflight[inflightKey{seed: seed, fastMode: targetFast}]
		em.mu.RUnlock()

		if (exists && ctx != nil && ctx.fastMode == targetFast) || isInflight {
			return
		}

		go func() {
			if em.closed.Load() {
				return
			}
			em.mu.Lock()
			if em.closed.Load() {
				em.mu.Unlock()
				return
			}
			em.epochSeeds[nextEpoch] = seed
			em.mu.Unlock()

			if ctx, err := em.getContextBySeedWithMode(nextEpoch, seed, targetFast, false); err == nil && ctx != nil {
				ctx.DecRef()
			}
		}()
	}
}

// PurgeAllVMs destroys idle pooled VMs across all cached contexts to reclaim RAM when mining stops.
func (em *EpochManager) PurgeAllVMs() {
	em.mu.RLock()
	defer em.mu.RUnlock()
	for _, ctx := range em.contexts {
		if ctx != nil {
			ctx.PurgeVMs()
		}
	}
	for _, ctx := range em.historicalLight {
		if ctx != nil {
			ctx.PurgeVMs()
		}
	}
}

// VMPoolStats returns aggregated VM pool counts and estimated allocated CGo memory in MB.
func (em *EpochManager) VMPoolStats() (totalVMs, activeVMs int64, allocatedMB uint64) {
	em.mu.RLock()
	defer em.mu.RUnlock()
	addStats := func(ctx *RxContext) {
		if ctx == nil {
			return
		}
		t, a := ctx.VMPoolStats()
		totalVMs += t
		activeVMs += a
		if ctx.fastMode {
			allocatedMB += 2080 // FastDataset (LightCache already released)
		} else {
			allocatedMB += 256 // LightCache
		}
	}
	for _, ctx := range em.contexts {
		addStats(ctx)
	}
	for _, ctx := range em.historicalLight {
		addStats(ctx)
	}
	if totalVMs > 0 {
		allocatedMB += uint64(totalVMs*22) / 10 // ~2.2 MB per VM
	}
	return totalVMs, activeVMs, allocatedMB
}

// LargePages returns true if any active cached RandomX context is using OS HugePages.
func (em *EpochManager) LargePages() bool {
	em.mu.RLock()
	defer em.mu.RUnlock()
	for _, ctx := range em.contexts {
		if ctx != nil && ctx.LargePages() {
			return true
		}
	}
	for _, ctx := range em.historicalLight {
		if ctx != nil && ctx.LargePages() {
			return true
		}
	}
	return false
}

// Close frees all active RandomX contexts.
func (em *EpochManager) Close() {
	em.closed.Store(true)
	em.mu.Lock()
	defer em.mu.Unlock()

	for s, ctx := range em.contexts {
		if ctx != nil {
			ctx.Close()
		}
		delete(em.contexts, s)
	}
	for s := range em.contextEpochs {
		delete(em.contextEpochs, s)
	}
	for s, ctx := range em.historicalLight {
		if ctx != nil {
			ctx.Close()
		}
		delete(em.historicalLight, s)
	}
	em.historicalOrder = em.historicalOrder[:0]
}
