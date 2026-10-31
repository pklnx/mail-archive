//go:build unix

package attachtext

import (
	"sync"

	"github.com/tetratelabs/wazero/experimental"
	"golang.org/x/sys/unix"
)

// mmapAllocator backs each WebAssembly linear memory with an anonymous
// mapping of the full page limit, reserved up front. Growing the memory
// only hands out more of the mapping: nothing is copied, nothing moves, and
// the memory lies outside the Go heap, so neither GC timing nor GOGC
// changes how much the process holds. Only pages the module touches become
// resident, so resident linear memory never exceeds the limit.
//
// Free, which wazero calls when a module closes, only gives the pages back
// to the system (MADV_DONTNEED); the mapping stays valid. Unmapping there
// would be fatal: Kill closes the module from another goroutine while the
// module may still run on the extracting goroutine, and compiled code that
// touches an unmapped address faults the whole process. Dropped pages only
// read back as zeros inside the sandbox, whose result is discarded anyway.
// The engine unmaps through release, after the go-pdfium call has
// returned and Kill has finished.
type mmapAllocator struct {
	mu    sync.Mutex
	maps  [][]byte // mappings of the current document
	peak  uint64   // largest length requested for the current document
	mmap  func(size int) ([]byte, error)
	hooks allocHooks
}

// allocHooks are test seams; nil hooks are skipped.
type allocHooks struct {
	free  func() // after Free gave the pages back
	unmap func() // after release unmapped a mapping
}

func newAllocator() *mmapAllocator {
	return &mmapAllocator{mmap: func(size int) ([]byte, error) {
		return unix.Mmap(-1, 0, size, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_PRIVATE|unix.MAP_ANON|unix.MAP_NORESERVE)
	}}
}

// Allocate implements experimental.MemoryAllocator. max is the page limit
// in bytes: wazero clamps the module's declared maximum to it.
func (a *mmapAllocator) Allocate(_, max uint64) experimental.LinearMemory {
	buf, err := a.mmap(int(max)) //nolint:gosec // max is the page limit, at most 128 MiB
	if err != nil {
		// Reallocate returns nil, so creating the instance panics; the
		// engine recovers that and reports the document as failed.
		return &linearMemory{a: a}
	}
	a.mu.Lock()
	a.maps = append(a.maps, buf)
	a.mu.Unlock()
	return &linearMemory{a: a, buf: buf}
}

// peakAndReset returns the peak length requested since the last call.
func (a *mmapAllocator) peakAndReset() uint64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	p := a.peak
	a.peak = 0
	return p
}

// release unmaps the mappings of the current document. It must only run
// when no module that uses them can run any more.
func (a *mmapAllocator) release() {
	a.mu.Lock()
	maps := a.maps
	a.maps = nil
	a.mu.Unlock()
	for _, m := range maps {
		_ = unix.Munmap(m)
		if a.hooks.unmap != nil {
			a.hooks.unmap()
		}
	}
}

// linearMemory is one module's memory: a prefix of its mapping.
type linearMemory struct {
	a   *mmapAllocator
	buf []byte // the whole mapping; nil when mmap failed
}

// Reallocate implements experimental.LinearMemory. It returns nil beyond
// the mapping, which wazero reports to the module as a failed grow.
func (m *linearMemory) Reallocate(size uint64) []byte {
	if m.buf == nil || size > uint64(len(m.buf)) {
		return nil
	}
	m.a.mu.Lock()
	m.a.peak = max(m.a.peak, size)
	m.a.mu.Unlock()
	return m.buf[:size]
}

// Free implements experimental.LinearMemory: see mmapAllocator.
func (m *linearMemory) Free() {
	if m.buf == nil {
		return
	}
	_ = unix.Madvise(m.buf, unix.MADV_DONTNEED)
	if m.a.hooks.free != nil {
		m.a.hooks.free()
	}
}

// memoryAllocator is what the engine passes to wazero.
func (a *mmapAllocator) memoryAllocator() experimental.MemoryAllocator { return a }
