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

// RxContext manages cache/dataset for a specific epoch seed and provides a VM pool for thread-safe hashing.
type RxContext struct {
	seed     []byte
	fastMode bool
	cache    *LightCache
	dataset  *FastDataset
	flags    C.randomx_flags

	vmPool sync.Pool
	mu     sync.RWMutex
	closed bool
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
	workers := runtime.NumCPU()
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

	ctx := &RxContext{
		seed:     append([]byte(nil), seed...),
		fastMode: fastMode,
		cache:    cache,
		dataset:  dataset,
		flags:    flags,
	}

	ctx.vmPool.New = func() interface{} {
		return ctx.createVM()
	}

	return ctx, nil
}

func (ctx *RxContext) createVM() *VM {
	var cVM *C.randomx_vm
	if ctx.fastMode && ctx.dataset != nil {
		cVM = C.randomx_create_vm(ctx.flags, nil, ctx.dataset.raw)
	} else if ctx.cache != nil {
		cVM = C.randomx_create_vm(ctx.flags, ctx.cache.raw, nil)
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
		C.randomx_destroy_vm(v.raw)
		v.raw = nil
	}
}

// CalculateHash calculates the 32-byte RandomX hash using a pooled VM.
func (ctx *RxContext) CalculateHash(input []byte) ([32]byte, error) {
	ctx.mu.RLock()
	if ctx.closed {
		ctx.mu.RUnlock()
		return [32]byte{}, errors.New("context is closed")
	}
	ctx.mu.RUnlock()

	item := ctx.vmPool.Get()
	if item == nil {
		return [32]byte{}, errors.New("failed to allocate RandomX VM")
	}
	vm := item.(*VM)
	defer ctx.vmPool.Put(vm)

	var output [32]byte
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

// Close frees all memory associated with this context.
func (ctx *RxContext) Close() {
	ctx.mu.Lock()
	defer ctx.mu.Unlock()

	if ctx.closed {
		return
	}
	ctx.closed = true

	if ctx.dataset != nil {
		ctx.dataset.Release()
		ctx.dataset = nil
	} else if ctx.cache != nil {
		ctx.cache.Release()
		ctx.cache = nil
	}
}

