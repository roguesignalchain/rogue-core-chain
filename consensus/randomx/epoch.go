package randomx

import (
	"bytes"
	"fmt"
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
)

const (
	// EpochLength is the number of blocks per RandomX epoch (2048 blocks, ~17.07 hours at 30s/block).
	EpochLength = uint64(2048)

	// SeedLag is the number of blocks prior to epoch transition when the new seed is locked (64 blocks, ~32 minutes at 30s/block).
	SeedLag = uint64(64)

	// CacheRetentionBlocks is the number of blocks to retain the previous epoch cache to handle chain reorgs.
	CacheRetentionBlocks = uint64(128)
)

// EpochManager coordinates RandomX cache/dataset lifecycle across epochs with precomputation and reorg support.
type EpochManager struct {
	fastMode bool
	mu       sync.RWMutex

	// activeContexts caches RxContext by epoch number
	contexts map[uint64]*RxContext

	// epochSeeds maps epoch number to 32-byte seed
	epochSeeds map[uint64]common.Hash
}

// NewEpochManager creates a new epoch coordinator.
func NewEpochManager(fastMode bool) *EpochManager {
	return &EpochManager{
		fastMode:   fastMode,
		contexts:   make(map[uint64]*RxContext),
		epochSeeds: make(map[uint64]common.Hash),
	}
}

// SetFastMode updates the memory mode (Light Mode ~256MB vs Fast Mode ~2.08GB) dynamically.
func (em *EpochManager) SetFastMode(fastMode bool) {
	em.mu.Lock()
	defer em.mu.Unlock()
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

// GetSeed retrieves or resolves the seed hash for the given epoch.
// It cross-checks against the canonical header at SeedBlockNumber(epoch) so that if a
// chain reorganization modifies the seed block, any stale cached context is invalidated.
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
	if oldSeed, hadSeed := em.epochSeeds[epoch]; hadSeed && oldSeed != headerHash {
		if oldCtx, hasCtx := em.contexts[epoch]; hasCtx {
			oldCtx.Close()
			delete(em.contexts, epoch)
		}
	}
	em.epochSeeds[epoch] = headerHash
	em.mu.Unlock()
	return headerHash, nil
}

// GetContext returns the execution context for a given block number.
// If the context does not exist or its seed changed due to a reorg, it initializes it
// (in Light Mode ~256MB or Fast Mode ~2.08GB).
func (em *EpochManager) GetContext(blockNum uint64, chain consensus.ChainHeaderReader) (*RxContext, error) {
	epoch := EpochNumber(blockNum)

	seed, err := em.GetSeed(epoch, chain)
	if err != nil {
		return nil, err
	}

	em.mu.RLock()
	ctx, exists := em.contexts[epoch]
	targetFast := em.fastMode
	em.mu.RUnlock()
	if exists && ctx.fastMode == targetFast && bytes.Equal(ctx.seed, seed.Bytes()) {
		return ctx, nil
	}

	em.mu.Lock()
	defer em.mu.Unlock()

	// Double check under write lock
	if ctx, exists = em.contexts[epoch]; exists && ctx.fastMode == em.fastMode && bytes.Equal(ctx.seed, seed.Bytes()) {
		return ctx, nil
	}
	if exists && ctx != nil {
		ctx.Close()
		delete(em.contexts, epoch)
	}

	newCtx, err := NewRxContext(seed.Bytes(), em.fastMode)
	if err != nil {
		return nil, fmt.Errorf("failed to create RxContext for epoch %d: %w", epoch, err)
	}

	em.contexts[epoch] = newCtx

	// Clean up stale contexts older than current epoch - 1
	for ep, oldCtx := range em.contexts {
		if ep+1 < epoch {
			oldCtx.Close()
			delete(em.contexts, ep)
		}
	}

	return newCtx, nil
}

// PrecomputeNextEpoch initiates background initialization of the upcoming epoch cache
// once blockNum enters the SeedLag window (e.g. block 2048 - 64 = 1984).
func (em *EpochManager) PrecomputeNextEpoch(blockNum uint64, chain consensus.ChainHeaderReader) {
	epoch := EpochNumber(blockNum)
	nextEpoch := epoch + 1
	transitionBlock := nextEpoch * EpochLength
	seedBlock := transitionBlock - SeedLag

	if blockNum >= seedBlock && blockNum < transitionBlock {
		em.mu.RLock()
		_, exists := em.contexts[nextEpoch]
		em.mu.RUnlock()
		if !exists {
			go func() {
				seedHeader := chain.GetHeaderByNumber(seedBlock)
				if seedHeader == nil {
					return
				}
				seed := seedHeader.Hash()

				em.mu.Lock()
				if _, already := em.contexts[nextEpoch]; !already {
					em.epochSeeds[nextEpoch] = seed
					em.mu.Unlock()

					// Allocate cache in background
					bgCtx, err := NewRxContext(seed.Bytes(), em.fastMode)
					if err == nil {
						em.mu.Lock()
						em.contexts[nextEpoch] = bgCtx
						em.mu.Unlock()
					}
				} else {
					em.mu.Unlock()
				}
			}()
		}
	}
}

// Close frees all active RandomX contexts.
func (em *EpochManager) Close() {
	em.mu.Lock()
	defer em.mu.Unlock()

	for ep, ctx := range em.contexts {
		ctx.Close()
		delete(em.contexts, ep)
	}
}
