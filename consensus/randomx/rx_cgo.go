package randomx

/*
#cgo CFLAGS: -I${SRCDIR}/include -O3
#cgo LDFLAGS: -L${SRCDIR}/lib -lrandomx -lstdc++ -lm -lpthread
#include "randomx.h"
#include <stdint.h>
#include <stdlib.h>
#include <string.h>

static inline void rx_put_le64(uint8_t *dst, uint64_t v) {
	dst[0] = (uint8_t)(v);
	dst[1] = (uint8_t)(v >> 8);
	dst[2] = (uint8_t)(v >> 16);
	dst[3] = (uint8_t)(v >> 24);
	dst[4] = (uint8_t)(v >> 32);
	dst[5] = (uint8_t)(v >> 40);
	dst[6] = (uint8_t)(v >> 48);
	dst[7] = (uint8_t)(v >> 56);
}

static inline int rx_hash_meets_target(const uint8_t *hash, const uint8_t *target_be) {
	for (int i = 0; i < 32; i++) {
		uint8_t b = hash[31 - i];
		uint8_t t = target_be[i];
		if (b < t) {
			return 1;
		}
		if (b > t) {
			return 0;
		}
	}
	return 1;
}

static uint32_t rx_mine_batch(
	randomx_vm *vm,
	const uint8_t *seal_hash,
	uint64_t start_nonce,
	uint32_t count,
	const uint8_t *target_be,
	uint64_t *win_nonce_out,
	uint8_t *win_hash_out,
	int *found_out
) {
	*found_out = 0;
	if (vm == NULL || count == 0) {
		return 0;
	}

	uint8_t blob[76];
	memset(blob, 0, sizeof(blob));
	memcpy(blob, seal_hash, 32);

	rx_put_le64(blob + 39, start_nonce);
	randomx_calculate_hash_first(vm, blob, sizeof(blob));

	uint8_t out_hash[32];
	for (uint32_t i = 1; i < count; i++) {
		rx_put_le64(blob + 39, start_nonce + (uint64_t)i);
		randomx_calculate_hash_next(vm, blob, sizeof(blob), out_hash);
		if (rx_hash_meets_target(out_hash, target_be)) {
			*win_nonce_out = start_nonce + (uint64_t)(i - 1);
			memcpy(win_hash_out, out_hash, 32);
			*found_out = 1;
			uint8_t dummy[32];
			randomx_calculate_hash_last(vm, dummy);
			return i;
		}
	}

	randomx_calculate_hash_last(vm, out_hash);
	if (rx_hash_meets_target(out_hash, target_be)) {
		*win_nonce_out = start_nonce + (uint64_t)(count - 1);
		memcpy(win_hash_out, out_hash, 32);
		*found_out = 1;
	}
	return count;
}
*/
import "C"
import (
	"errors"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"unsafe"

	"github.com/ethereum/go-ethereum/log"
)

// RxFlags wraps randomx_flags
type RxFlags uint32

// VM represents an allocated RandomX virtual machine.
// Note: A single VM must not be used concurrently from multiple goroutines.
type VM struct {
	raw       *C.randomx_vm
	fastMode  bool
	destroyed atomic.Bool
}

// LightCache wraps the ~256MB cache used in Light Mode.
type LightCache struct {
	raw        *C.randomx_cache
	seed       []byte
	largePages bool
}

// FastDataset wraps the ~2.08GB dataset used in Fast Mode.
type FastDataset struct {
	raw        *C.randomx_dataset
	cache      *LightCache
	seed       []byte
	largePages bool
}

// RxContext manages cache/dataset for a specific epoch seed and provides an elastic bounded VM pool
// with atomic reference counting for lock-free, thread-safe hashing and lazy CGo memory release.
type RxContext struct {
	seed       []byte
	fastMode   bool
	largePages bool
	cache      *LightCache
	dataset    *FastDataset
	flags      C.randomx_flags

	vmPool      chan *VM
	totalVMs    atomic.Int64
	activeVMs   atomic.Int64
	refs        atomic.Int64
	retired     bool
	releaseOnce sync.Once
	mu          sync.RWMutex
}

// GetRecommendedFlags returns machine-optimized flags (JIT, HardAES, LargePages, etc.)
func GetRecommendedFlags(fastMode bool) C.randomx_flags {
	flags := C.randomx_get_flags() | C.RANDOMX_FLAG_LARGE_PAGES
	if fastMode {
		flags |= C.RANDOMX_FLAG_FULL_MEM
	}
	return flags
}

// NewLightCache allocates and initializes a 256MB RandomX cache for the given seed,
// attempting HugePages if requested and falling back to standard pages if unavailable.
func NewLightCache(seed []byte, flags C.randomx_flags) (*LightCache, error) {
	usedLargePages := (flags & C.RANDOMX_FLAG_LARGE_PAGES) != 0
	cCache := C.randomx_alloc_cache(flags)
	if cCache == nil && usedLargePages {
		usedLargePages = false
		cCache = C.randomx_alloc_cache(flags &^ C.RANDOMX_FLAG_LARGE_PAGES)
	}
	if cCache == nil {
		return nil, errors.New("failed to allocate RandomX cache (out of memory)")
	}

	cSeed := C.CBytes(seed)
	defer C.free(cSeed)

	C.randomx_init_cache(cCache, cSeed, C.size_t(len(seed)))
	return &LightCache{
		raw:        cCache,
		seed:       append([]byte(nil), seed...),
		largePages: usedLargePages,
	}, nil
}

// Release releases cache memory.
func (c *LightCache) Release() {
	if c.raw != nil {
		C.randomx_release_cache(c.raw)
		c.raw = nil
	}
}

// NewFastDataset allocates and initializes the ~2.08GB dataset using an initialized cache,
// attempting HugePages if requested and falling back to standard pages if unavailable.
func NewFastDataset(cache *LightCache, flags C.randomx_flags) (*FastDataset, error) {
	usedLargePages := (flags & C.RANDOMX_FLAG_LARGE_PAGES) != 0
	cDataset := C.randomx_alloc_dataset(flags)
	if cDataset == nil && usedLargePages {
		usedLargePages = false
		cDataset = C.randomx_alloc_dataset(flags &^ C.RANDOMX_FLAG_LARGE_PAGES)
	}
	if cDataset == nil {
		return nil, errors.New("failed to allocate RandomX dataset (need ~2.1GB RAM)")
	}

	itemCount := C.randomx_dataset_item_count()
	workers := runtime.NumCPU() / 2
	if workers < 1 {
		workers = 1
	}

	itemsPerWorker := uint64(itemCount) / uint64(workers)
	remainder := uint64(itemCount) % uint64(workers)

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		start := uint64(i) * itemsPerWorker
		count := itemsPerWorker
		if i == workers-1 {
			count += remainder
		}
		go func(s, c uint64) {
			defer wg.Done()
			C.randomx_init_dataset(cDataset, cache.raw, C.ulong(s), C.ulong(c))
		}(start, count)
	}
	wg.Wait()

	return &FastDataset{raw: cDataset, cache: cache, seed: cache.seed, largePages: usedLargePages}, nil
}

// Release releases dataset memory.
func (d *FastDataset) Release() {
	if d.raw != nil {
		C.randomx_release_dataset(d.raw)
		d.raw = nil
	}
	if d.cache != nil {
		d.cache.Release()
		d.cache = nil
	}
}

// NewRxContext creates an execution context for a given epoch seed.
// Defaults to Light Mode (256MB) unless fastMode is explicitly true.
func NewRxContext(seed []byte, fastMode bool) (*RxContext, error) {
	return NewRxContextWithPoolCap(seed, fastMode, 0)
}

// NewRxContextWithPoolCap creates an execution context for a given epoch seed with a
// minimum VM pool capacity to avoid VM churn when mining with more than NumCPU threads.
func NewRxContextWithPoolCap(seed []byte, fastMode bool, minPoolCap int) (*RxContext, error) {
	flags := GetRecommendedFlags(fastMode)

	// In Fast Mode, keep HugePages reserved for the 2.08GB FastDataset rather than the temporary 256MB LightCache.
	cacheFlags := flags
	if fastMode {
		cacheFlags &^= C.RANDOMX_FLAG_LARGE_PAGES
	}

	cache, err := NewLightCache(seed, cacheFlags)
	if err != nil {
		return nil, err
	}

	usedLargePages := cache.largePages
	var dataset *FastDataset
	if fastMode {
		dataset, err = NewFastDataset(cache, flags)
		if err != nil {
			cache.Release()
			return nil, fmt.Errorf("failed to initialize fast dataset: %w", err)
		}
		usedLargePages = dataset.largePages
		// FastDataset VMs use dataset directly (cache=nil); release the 256MB LightCache immediately.
		cache.Release()
		dataset.cache = nil
		cache = nil
	}

	mode := "light"
	if fastMode {
		mode = "fast"
	}
	if usedLargePages {
		log.Info("RandomX context initialized with HugePages enabled", "mode", mode)
	} else {
		log.Info("RandomX context initialized with standard pages (HugePages unavailable)", "mode", mode)
	}

	poolCap := runtime.NumCPU() * 2
	if poolCap < 16 {
		poolCap = 16
	}
	if minPoolCap > poolCap {
		poolCap = minPoolCap
	}

	ctx := &RxContext{
		seed:       append([]byte(nil), seed...),
		fastMode:   fastMode,
		largePages: usedLargePages,
		cache:      cache,
		dataset:    dataset,
		flags:      flags,
		vmPool:     make(chan *VM, poolCap),
	}

	return ctx, nil
}

// LargePages returns true if this context's primary memory (dataset in Fast Mode, cache in Light Mode)
// was allocated using OS HugePages.
func (ctx *RxContext) LargePages() bool {
	ctx.mu.RLock()
	defer ctx.mu.RUnlock()
	return ctx.largePages
}

func (ctx *RxContext) createVM() *VM {
	allocVM := func(flags C.randomx_flags) *C.randomx_vm {
		if ctx.fastMode && ctx.dataset != nil {
			return C.randomx_create_vm(flags, nil, ctx.dataset.raw)
		} else if ctx.cache != nil {
			return C.randomx_create_vm(flags, ctx.cache.raw, nil)
		}
		return nil
	}

	ctx.mu.RLock()
	baseFlags := ctx.flags
	ctx.mu.RUnlock()

	cVM := allocVM(baseFlags)
	if cVM == nil && (baseFlags&C.RANDOMX_FLAG_LARGE_PAGES) != 0 {
		noLPFlags := baseFlags &^ C.RANDOMX_FLAG_LARGE_PAGES
		cVM = allocVM(noLPFlags)
		if cVM != nil {
			log.Info("RandomX VM scratchpad HugePages unavailable; falling back to standard pages")
			ctx.mu.Lock()
			ctx.flags = noLPFlags
			ctx.mu.Unlock()
		}
		baseFlags = noLPFlags
	}
	// Fallback for W^X hardened kernels (macOS Hardened Runtime, strict SELinux/PaX):
	// retry with RANDOMX_FLAG_SECURE, and finally fall back to interpreter mode if JIT is blocked.
	if cVM == nil && (baseFlags&C.RANDOMX_FLAG_JIT) != 0 {
		secureFlags := baseFlags | C.RANDOMX_FLAG_SECURE
		cVM = allocVM(secureFlags)
		if cVM != nil {
			log.Warn("RandomX JIT allocation failed; falling back to RANDOMX_FLAG_SECURE (W^X safe)")
			ctx.mu.Lock()
			ctx.flags = secureFlags
			ctx.mu.Unlock()
		} else {
			interpFlags := baseFlags &^ C.RANDOMX_FLAG_JIT
			cVM = allocVM(interpFlags)
			if cVM != nil {
				log.Warn("RandomX JIT and SECURE modes blocked by kernel; falling back to slow interpreter mode")
				ctx.mu.Lock()
				ctx.flags = interpFlags
				ctx.mu.Unlock()
			}
		}
	}
	if cVM == nil {
		return nil
	}
	vm := &VM{raw: cVM, fastMode: ctx.fastMode}
	runtime.SetFinalizer(vm, (*VM).Destroy)
	return vm
}

// Destroy cleans up the VM instance. Safe to call multiple times.
func (v *VM) Destroy() {
	if v == nil || !v.destroyed.CompareAndSwap(false, true) {
		return
	}
	if v.raw != nil {
		runtime.SetFinalizer(v, nil)
		C.randomx_destroy_vm(v.raw)
		v.raw = nil
	}
}

// IncRef atomically increments the active reference count if the context has not been retired.
// Returns false if the context is already retired and cannot be used.
func (ctx *RxContext) IncRef() bool {
	ctx.mu.RLock()
	if ctx.retired {
		ctx.mu.RUnlock()
		return false
	}
	ctx.refs.Add(1)
	ctx.mu.RUnlock()
	return true
}

// DecRef decrements the active reference count and lazily releases underlying CGo memory
// once the context is retired and no active references remain.
func (ctx *RxContext) DecRef() {
	if ctx.refs.Add(-1) == 0 {
		ctx.mu.RLock()
		retired := ctx.retired
		ctx.mu.RUnlock()
		if retired {
			ctx.tryRelease()
		}
	}
}

// AcquireVM retrieves a pre-warmed VM from the bounded elastic pool, or allocates a temporary
// VM if all pooled VMs are currently occupied (e.g. concurrent P2P burst validation).
// It acquires a temporary context reference so Close()/tryRelease() cannot free underlying
// CGo memory while a VM is being popped or initialized.
func (ctx *RxContext) AcquireVM() (*VM, error) {
	if !ctx.IncRef() {
		return nil, errors.New("context is retired")
	}
	defer ctx.DecRef()

	select {
	case vm := <-ctx.vmPool:
		if vm != nil && vm.raw != nil {
			ctx.mu.RLock()
			retired := ctx.retired
			ctx.mu.RUnlock()
			if retired {
				vm.Destroy()
				ctx.totalVMs.Add(-1)
				return nil, errors.New("context is retired")
			}
			ctx.activeVMs.Add(1)
			return vm, nil
		}
	default:
	}

	vm := ctx.createVM()
	if vm == nil {
		return nil, errors.New("failed to allocate RandomX VM")
	}
	ctx.mu.RLock()
	retired := ctx.retired
	ctx.mu.RUnlock()
	if retired {
		vm.Destroy()
		return nil, errors.New("context is retired")
	}
	ctx.totalVMs.Add(1)
	ctx.activeVMs.Add(1)
	return vm, nil
}

// ReleaseVM returns a VM to the bounded elastic pool, or destroys it immediately if the pool
// is already at capacity or the context has been retired.
func (ctx *RxContext) ReleaseVM(vm *VM) {
	if vm == nil {
		return
	}
	ctx.activeVMs.Add(-1)

	ctx.mu.RLock()
	retired := ctx.retired
	ctx.mu.RUnlock()
	if retired {
		vm.Destroy()
		ctx.totalVMs.Add(-1)
		return
	}

	select {
	case ctx.vmPool <- vm:
	default:
		vm.Destroy()
		ctx.totalVMs.Add(-1)
	}
}

// PrewarmVMs initializes up to count VMs in parallel using GOMAXPROCS workers
// so mining threads start with zero JIT compilation latency.
func (ctx *RxContext) PrewarmVMs(count int) {
	if count <= 0 || !ctx.IncRef() {
		return
	}
	defer ctx.DecRef()

	poolCap := cap(ctx.vmPool)
	if count > poolCap {
		count = poolCap
	}
	existing := len(ctx.vmPool)
	needed := count - existing
	if needed <= 0 {
		return
	}

	workers := runtime.GOMAXPROCS(0)
	if workers < 1 {
		workers = 1
	}
	if workers > needed {
		workers = needed
	}

	jobs := make(chan struct{}, needed)
	for i := 0; i < needed; i++ {
		jobs <- struct{}{}
	}
	close(jobs)

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range jobs {
				ctx.mu.RLock()
				if ctx.retired {
					ctx.mu.RUnlock()
					return
				}
				ctx.mu.RUnlock()

				vm := ctx.createVM()
				if vm == nil {
					continue
				}
				select {
				case ctx.vmPool <- vm:
					ctx.totalVMs.Add(1)
				default:
					vm.Destroy()
				}
			}
		}()
	}
	wg.Wait()
}

// PurgeVMs destroys all idle VMs in the pool to immediately reclaim RAM when mining stops.
func (ctx *RxContext) PurgeVMs() {
	for {
		select {
		case vm := <-ctx.vmPool:
			if vm != nil {
				vm.Destroy()
				ctx.totalVMs.Add(-1)
			}
		default:
			return
		}
	}
}

// VMPoolStats returns the current total allocated VMs and actively hashing VMs for this context.
func (ctx *RxContext) VMPoolStats() (total, active int64) {
	return ctx.totalVMs.Load(), ctx.activeVMs.Load()
}

// CalculateHashWithVM computes the 32-byte RandomX hash using an already-acquired thread-local VM.
// The caller must hold an active context reference via IncRef() for the duration of the call.
func (ctx *RxContext) CalculateHashWithVM(vm *VM, input []byte) ([32]byte, error) {
	var output [32]byte
	if vm == nil || vm.raw == nil || vm.destroyed.Load() {
		return output, errors.New("invalid RandomX VM")
	}
	if len(input) == 0 {
		return output, errors.New("empty input")
	}

	C.randomx_calculate_hash(
		vm.raw,
		unsafe.Pointer(&input[0]),
		C.size_t(len(input)),
		unsafe.Pointer(&output[0]),
	)
	runtime.KeepAlive(input)

	return output, nil
}

// MineBatchWithVM evaluates up to count consecutive nonces starting at startNonce
// using RandomX's pipelined first/next/last API in a single CGo transition.
// It stops as soon as a hash meets targetBE or count hashes have been evaluated.
func (ctx *RxContext) MineBatchWithVM(vm *VM, sealHash [32]byte, startNonce uint64, count uint32, targetBE [32]byte) (winNonce uint64, winHash [32]byte, attempted uint32, found bool, err error) {
	if vm == nil || vm.raw == nil || vm.destroyed.Load() {
		return 0, winHash, 0, false, errors.New("invalid RandomX VM")
	}
	if count == 0 {
		return 0, winHash, 0, false, nil
	}

	var (
		cWinNonce C.uint64_t
		cFound    C.int
	)
	attempts := C.rx_mine_batch(
		vm.raw,
		(*C.uint8_t)(unsafe.Pointer(&sealHash[0])),
		C.uint64_t(startNonce),
		C.uint32_t(count),
		(*C.uint8_t)(unsafe.Pointer(&targetBE[0])),
		&cWinNonce,
		(*C.uint8_t)(unsafe.Pointer(&winHash[0])),
		&cFound,
	)
	runtime.KeepAlive(sealHash)
	runtime.KeepAlive(targetBE)

	return uint64(cWinNonce), winHash, uint32(attempts), cFound != 0, nil
}

// CalculateHash calculates the 32-byte RandomX hash using a pooled VM and atomic reference counting.
func (ctx *RxContext) CalculateHash(input []byte) ([32]byte, error) {
	if !ctx.IncRef() {
		return [32]byte{}, errors.New("context is closed")
	}
	defer ctx.DecRef()

	vm, err := ctx.AcquireVM()
	if err != nil {
		return [32]byte{}, err
	}
	defer ctx.ReleaseVM(vm)

	return ctx.CalculateHashWithVM(vm, input)
}

func (ctx *RxContext) tryRelease() {
	ctx.releaseOnce.Do(func() {
		ctx.mu.Lock()
		defer ctx.mu.Unlock()

		ctx.PurgeVMs()
		if ctx.dataset != nil {
			ctx.dataset.Release()
			ctx.dataset = nil
			ctx.cache = nil
		} else if ctx.cache != nil {
			ctx.cache.Release()
			ctx.cache = nil
		}
	})
}

// Close marks the context as retired and releases all memory once active references reach zero.
func (ctx *RxContext) Close() {
	ctx.mu.Lock()
	if ctx.retired {
		ctx.mu.Unlock()
		return
	}
	ctx.retired = true
	refs := ctx.refs.Load()
	ctx.mu.Unlock()

	if refs == 0 {
		ctx.tryRelease()
	}
}

