package randomx

/*
#cgo CFLAGS: -I${SRCDIR}/include -O3
#cgo LDFLAGS: -L${SRCDIR}/lib -lrandomx -lstdc++ -lm -lpthread
#include "randomx.h"
#include <stdlib.h>
*/
import "C"
import (
	"errors"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"unsafe"
)

// RxFlags wraps randomx_flags
type RxFlags uint32

// VM represents an allocated RandomX virtual machine.
// Note: A single VM must not be used concurrently from multiple goroutines.
type VM struct {
	raw      *C.randomx_vm
	fastMode bool
}

// LightCache wraps the ~256MB cache used in Light Mode.
type LightCache struct {
	raw  *C.randomx_cache
	seed []byte
}

// FastDataset wraps the ~2.08GB dataset used in Fast Mode.
type FastDataset struct {
	raw   *C.randomx_dataset
	cache *LightCache
	seed  []byte
}

// RxContext manages cache/dataset for a specific epoch seed and provides an elastic bounded VM pool
// with atomic reference counting for lock-free, thread-safe hashing and lazy CGo memory release.
type RxContext struct {
	seed     []byte
	fastMode bool
	cache    *LightCache
	dataset  *FastDataset
	flags    C.randomx_flags

	vmPool      chan *VM
	totalVMs    atomic.Int64
	activeVMs   atomic.Int64
	refs        atomic.Int64
	retired     bool
	releaseOnce sync.Once
	mu          sync.RWMutex
}

// GetRecommendedFlags returns machine-optimized flags (JIT, HardAES, etc.)
func GetRecommendedFlags(fastMode bool) C.randomx_flags {
	flags := C.randomx_get_flags()
	if fastMode {
		flags |= C.RANDOMX_FLAG_FULL_MEM
	}
	return flags
}

// NewLightCache allocates and initializes a 256MB RandomX cache for the given seed.
func NewLightCache(seed []byte, flags C.randomx_flags) (*LightCache, error) {
	cCache := C.randomx_alloc_cache(flags)
	if cCache == nil {
		return nil, errors.New("failed to allocate RandomX cache (out of memory)")
	}

	cSeed := C.CBytes(seed)
	defer C.free(cSeed)

	C.randomx_init_cache(cCache, cSeed, C.size_t(len(seed)))
	return &LightCache{raw: cCache, seed: append([]byte(nil), seed...)}, nil
}

// Release releases cache memory.
func (c *LightCache) Release() {
	if c.raw != nil {
		C.randomx_release_cache(c.raw)
		c.raw = nil
	}
}

// NewFastDataset allocates and initializes the ~2.08GB dataset using an initialized cache.
func NewFastDataset(cache *LightCache, flags C.randomx_flags) (*FastDataset, error) {
	cDataset := C.randomx_alloc_dataset(flags)
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

	return &FastDataset{raw: cDataset, cache: cache, seed: cache.seed}, nil
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
	flags := GetRecommendedFlags(fastMode)

	cache, err := NewLightCache(seed, flags)
	if err != nil {
		return nil, err
	}

	var dataset *FastDataset
	if fastMode {
		dataset, err = NewFastDataset(cache, flags)
		if err != nil {
			cache.Release()
			return nil, fmt.Errorf("failed to initialize fast dataset: %w", err)
		}
	}

	poolCap := runtime.NumCPU() + 2
	if poolCap < 4 {
		poolCap = 4
	}

	ctx := &RxContext{
		seed:     append([]byte(nil), seed...),
		fastMode: fastMode,
		cache:    cache,
		dataset:  dataset,
		flags:    flags,
		vmPool:   make(chan *VM, poolCap),
	}

	return ctx, nil
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

	cVM := allocVM(ctx.flags)
	// Fallback for W^X hardened kernels (macOS Hardened Runtime, strict SELinux/PaX):
	// retry with RANDOMX_FLAG_SECURE, and finally fall back to interpreter mode if JIT is blocked.
	if cVM == nil && (ctx.flags&C.RANDOMX_FLAG_JIT) != 0 {
		cVM = allocVM(ctx.flags | C.RANDOMX_FLAG_SECURE)
		if cVM == nil {
			cVM = allocVM(ctx.flags &^ C.RANDOMX_FLAG_JIT)
		}
	}
	if cVM == nil {
		return nil
	}
	vm := &VM{raw: cVM, fastMode: ctx.fastMode}
	runtime.SetFinalizer(vm, (*VM).Destroy)
	return vm
}

// Destroy cleans up the VM instance.
func (v *VM) Destroy() {
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
func (ctx *RxContext) AcquireVM() (*VM, error) {
	ctx.mu.RLock()
	if ctx.retired {
		ctx.mu.RUnlock()
		return nil, errors.New("context is retired")
	}
	ctx.mu.RUnlock()

	select {
	case vm := <-ctx.vmPool:
		if vm != nil && vm.raw != nil {
			ctx.activeVMs.Add(1)
			return vm, nil
		}
	default:
	}

	vm := ctx.createVM()
	if vm == nil {
		return nil, errors.New("failed to allocate RandomX VM")
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
	ctx.mu.RLock()
	retired := ctx.retired
	ctx.mu.RUnlock()
	if retired || count <= 0 {
		return
	}

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
	if vm == nil || vm.raw == nil {
		return output, errors.New("invalid RandomX VM")
	}
	if len(input) == 0 {
		return output, errors.New("empty input")
	}

	cInput := C.CBytes(input)
	defer C.free(cInput)

	C.randomx_calculate_hash(
		vm.raw,
		cInput,
		C.size_t(len(input)),
		unsafe.Pointer(&output[0]),
	)

	return output, nil
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

