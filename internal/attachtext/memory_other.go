//go:build !unix

package attachtext

import "github.com/tetratelabs/wazero/experimental"

// mmapAllocator is a stand-in on systems without mmap (Windows, plan9):
// wazero's default allocator is used there, with the same page limit.
// Releases are built for Linux only.
type mmapAllocator struct {
	mmap  func(size int) ([]byte, error)
	hooks allocHooks
}

type allocHooks struct {
	free  func()
	unmap func()
}

func newAllocator() *mmapAllocator { return &mmapAllocator{} }

func (a *mmapAllocator) memoryAllocator() experimental.MemoryAllocator { return nil }

func (a *mmapAllocator) peakAndReset() uint64 { return 0 }

func (a *mmapAllocator) release() {}
