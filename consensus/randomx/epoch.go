package randomx

import (
	"fmt"
	"sync"

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

	// MaxResolvedSeedCache is the capacity of the O(1) header-to-seed LRU cache (~80KB RAM).
	MaxResolvedSeedCache = 2048
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
	fastMode bool
	mu       sync.RWMutex

	// contexts caches RxContext by 32-byte seed hash so competing forks at epoch boundaries do not thrash each other.
	contexts map[common.Hash]*RxContext

	// contextEpochs tracks the highest epoch number associated with each cached seed for eviction.
	contextEpochs map[common.Hash]uint64

	// epochSeeds maps epoch number to the most recently resolved canonical 32-byte seed.
	epochSeeds map[uint64]common.Hash

	// resolvedSeeds provides O(1) cached header-hash -> seed-hash lookup for side-forks and recent headers.
	resolvedSeeds map[common.Hash]common.Hash
	resolvedOrder []common.Hash

	// inflight deduplicates concurrent context allocations without holding the global lock during CGo allocation.
	inflight map[inflightKey]*seedInit

	// allocSem limits concurrent CGo dataset/cache initializations to prevent memory exhaustion DoS.
	allocSem chan struct{}
}

// NewEpochManager creates a new epoch coordinator.
func NewEpochManager(fastMode bool) *EpochManager {
	return &EpochManager{
		fastMode:      fastMode,
		contexts:      make(map[common.Hash]*RxContext),
		contextEpochs: make(map[common.Hash]uint64),
		epochSeeds:    make(map[uint64]common.Hash),
		resolvedSeeds: make(map[common.Hash]common.Hash, MaxResolvedSeedCache),
		resolvedOrder: make([]common.Hash, 0, MaxResolvedSeedCache),
		inflight:      make(map[inflightKey]*seedInit),
		allocSem:      make(chan struct{}, 2),
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

func (em *EpochManager) cacheResolvedSeed(headerHash, seed common.Hash) {
	em.mu.Lock()
	defer em.mu.Unlock()
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

// ResolveSeedForHeader resolves the exact RandomX seed hash for a specific header by following
// its ancestor chain back to SeedBlockNumber(epoch). Uses an O(1) bounded cache (~4KB RAM)
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
	if blockNum-1 > seedNum {
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

	// Walk parent hashes back to seedNum (short-circuiting on any cached ancestor in the same epoch)
	if blockNum > seedNum {
		currHash := header.ParentHash
		currNum := blockNum - 1
		for currNum >= seedNum {
			if currNum == seedNum {
				curr := chain.GetHeader(currHash, currNum)
				if curr == nil {
					break
				}
				seed := curr.Hash()
				em.cacheResolvedSeed(headerHash, seed)
				return seed, nil
			}
			em.mu.RLock()
			if cached, ok := em.resolvedSeeds[currHash]; ok {
				em.mu.RUnlock()
				em.cacheResolvedSeed(headerHash, cached)
				return cached, nil
			}
			em.mu.RUnlock()

			curr := chain.GetHeader(currHash, currNum)
			if curr == nil {
				break
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
	return em.getContextBySeed(epoch, seed)
}

// GetContextForHeader returns the execution context for a specific header, resolving its seed
// along the header's own ancestor branch so side-fork blocks across epoch boundaries verify accurately.
func (em *EpochManager) GetContextForHeader(header *types.Header, chain consensus.ChainHeaderReader) (*RxContext, error) {
	if header == nil || header.Number == nil || !header.Number.IsUint64() {
		return nil, consensus.ErrInvalidNumber
	}
	epoch := EpochNumber(header.Number.Uint64())
	seed, err := em.ResolveSeedForHeader(header, chain)
	if err != nil {
		return nil, err
	}
	return em.getContextBySeed(epoch, seed)
}

func (em *EpochManager) getContextBySeed(epoch uint64, seed common.Hash) (*RxContext, error) {
	for {
		em.mu.RLock()
		ctx, exists := em.contexts[seed]
		targetFast := em.fastMode
		if exists && ctx != nil && ctx.fastMode == targetFast && ctx.IncRef() {
			em.mu.RUnlock()
			return ctx, nil
		}
		em.mu.RUnlock()

		em.mu.Lock()
		targetFast = em.fastMode
		if ctx, exists = em.contexts[seed]; exists && ctx != nil && ctx.fastMode == targetFast && ctx.IncRef() {
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
		// allocSem bounds concurrent allocations to prevent RAM exhaustion spikes.
		em.allocSem <- struct{}{}
		newCtx, err := NewRxContext(seed.Bytes(), targetFast)
		<-em.allocSem

		em.mu.Lock()
		delete(em.inflight, key)
		if err != nil {
			initCall.err = fmt.Errorf("failed to create RxContext for epoch %d: %w", epoch, err)
			close(initCall.done)
			em.mu.Unlock()
			return nil, initCall.err
		}

		// Acquire caller's reference before storing/publishing the context
		newCtx.IncRef()

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

		// Bound total cached contexts (3 in Light Mode, 2 in Fast Mode to prevent >4.2GB RAM spikes),
		// ensuring we never evict the newly created seed.
		maxContexts := MaxCachedContexts
		if targetFast {
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

		initCall.ctx = newCtx
		close(initCall.done)
		em.mu.Unlock()

		return newCtx, nil
	}
}

// PrecomputeNextEpoch initiates background initialization of the upcoming epoch cache
// once blockNum enters the SeedLag window (e.g. block 2048 - 64 = 1984).
func (em *EpochManager) PrecomputeNextEpoch(blockNum uint64, chain consensus.ChainHeaderReader) {
	if chain == nil {
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
			em.mu.Lock()
			em.epochSeeds[nextEpoch] = seed
			em.mu.Unlock()

			if ctx, err := em.getContextBySeed(nextEpoch, seed); err == nil && ctx != nil {
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
}

// VMPoolStats returns aggregated VM pool counts and estimated allocated CGo memory in MB.
func (em *EpochManager) VMPoolStats() (totalVMs, activeVMs int64, allocatedMB uint64) {
	em.mu.RLock()
	defer em.mu.RUnlock()
	for _, ctx := range em.contexts {
		if ctx == nil {
			continue
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
	if totalVMs > 0 {
		allocatedMB += uint64(totalVMs*22) / 10 // ~2.2 MB per VM
	}
	return totalVMs, activeVMs, allocatedMB
}

// Close frees all active RandomX contexts.
func (em *EpochManager) Close() {
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
}
