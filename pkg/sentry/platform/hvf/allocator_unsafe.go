// Copyright 2026 The gVisor Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//go:build darwin && arm64

package hvf

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/unix"
	"gvisor.dev/gvisor/pkg/hostarch"
	"gvisor.dev/gvisor/pkg/ring0/pagetables"
	"gvisor.dev/gvisor/pkg/sentry/platform/hvf/ipa"
	"gvisor.dev/gvisor/pkg/sync"
)

// ptChunkSize is the size of each block of page table memory.
const ptChunkSize = 4 << 20

// ptChunk is a block of page table memory, mapped in the IPA space.
type ptChunk struct {
	host uintptr
	ipa  uint64
}

// ptMemory is the page table memory of a VM: anonymous host memory that the
// registry maps, so that the guest can walk the page tables in it. Each page
// table set gets its pages through an allocator.
type ptMemory struct {
	registry *ipa.Registry

	mu sync.Mutex
	// chunks are never freed.
	// +checklocks:mu
	chunks []ptChunk
	// next and end are the unused part of the last chunk.
	// +checklocks:mu
	next, end uintptr
	// pool has free pages. All their entries are zero.
	// +checklocks:mu
	pool []*pagetables.PTEs
}

func newPTMemory(registry *ipa.Registry) *ptMemory {
	return &ptMemory{registry: registry}
}

// +checklocks:m.mu
func (m *ptMemory) newChunk() {
	mem, err := unix.Mmap(-1, 0, ptChunkSize, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_ANON|unix.MAP_PRIVATE)
	if err != nil {
		panic(fmt.Sprintf("hvf: allocating page table memory: %v", err))
	}
	host := uintptr(unsafe.Pointer(&mem[0]))
	var parts int
	if err := m.registry.Translate(host, ptChunkSize, ipa.Read|ipa.Write, func(addr uintptr, ipa uint64, length uintptr) {
		m.chunks = append(m.chunks, ptChunk{host: addr, ipa: ipa})
		parts++
	}); err != nil {
		panic(fmt.Sprintf("hvf: mapping page table memory: %v", err))
	}
	if parts != 1 {
		panic(fmt.Sprintf("hvf: page table memory has %d IPA parts", parts))
	}
	m.next, m.end = host, host+ptChunkSize
}

// get returns a page with all entries zero.
func (m *ptMemory) get() *pagetables.PTEs {
	m.mu.Lock()
	defer m.mu.Unlock()
	if n := len(m.pool); n > 0 {
		ptes := m.pool[n-1]
		m.pool = m.pool[:n-1]
		return ptes
	}
	if m.next == m.end {
		m.newChunk()
	}
	ptes := (*pagetables.PTEs)(unsafe.Pointer(m.next))
	m.next += hostarch.PageSize
	return ptes
}

// put zeroes pages and returns them to the pool. No vCPU may walk them.
func (m *ptMemory) put(pages []*pagetables.PTEs) {
	for _, ptes := range pages {
		*ptes = pagetables.PTEs{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pool = append(m.pool, pages...)
}

func (m *ptMemory) physicalFor(ptes *pagetables.PTEs) uintptr {
	addr := uintptr(unsafe.Pointer(ptes))
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range m.chunks {
		if addr >= c.host && addr-c.host < ptChunkSize {
			return uintptr(c.ipa) + (addr - c.host)
		}
	}
	panic(fmt.Sprintf("hvf: PTEs %#x are not in page table memory", addr))
}

func (m *ptMemory) lookupPTEs(physical uintptr) *pagetables.PTEs {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range m.chunks {
		if uint64(physical) >= c.ipa && uint64(physical)-c.ipa < ptChunkSize {
			return (*pagetables.PTEs)(unsafe.Pointer(c.host + (physical - uintptr(c.ipa))))
		}
	}
	panic(fmt.Sprintf("hvf: IPA %#x is not in page table memory", physical))
}

// allocator is the pagetables.Allocator of one page table set. It knows the
// pages that the set uses, so that it can return a freed page to ptMemory
// only after the TLB shootdown that follows the change, and return all pages
// when the set is released.
type allocator struct {
	mem *ptMemory

	mu sync.Mutex
	// live has the pages that the page tables use, and freed pages that are
	// not yet returned to mem.
	// +checklocks:mu
	live map[*pagetables.PTEs]struct{}
	// freed has the pages that the page tables no longer use. A vCPU can
	// still walk them until the next TLB shootdown.
	// +checklocks:mu
	freed []*pagetables.PTEs
}

var _ pagetables.Allocator = (*allocator)(nil)

func newAllocator(mem *ptMemory) *allocator {
	return &allocator{
		mem:  mem,
		live: make(map[*pagetables.PTEs]struct{}),
	}
}

// NewPTEs implements pagetables.Allocator.NewPTEs.
func (a *allocator) NewPTEs() *pagetables.PTEs {
	ptes := a.mem.get()
	a.mu.Lock()
	defer a.mu.Unlock()
	a.live[ptes] = struct{}{}
	return ptes
}

// PhysicalFor implements pagetables.Allocator.PhysicalFor. It returns the IPA
// of ptes.
func (a *allocator) PhysicalFor(ptes *pagetables.PTEs) uintptr {
	return a.mem.physicalFor(ptes)
}

// LookupPTEs implements pagetables.Allocator.LookupPTEs.
func (a *allocator) LookupPTEs(physical uintptr) *pagetables.PTEs {
	return a.mem.lookupPTEs(physical)
}

// FreePTEs implements pagetables.Allocator.FreePTEs.
func (a *allocator) FreePTEs(ptes *pagetables.PTEs) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.freed = append(a.freed, ptes)
}

// Recycle implements pagetables.Allocator.Recycle. The page tables do not
// call it; takeFreed and release do its work.
func (a *allocator) Recycle() {}

// takeFreed returns the pages freed since the last call. They can be
// returned with release after the next TLB shootdown completes.
func (a *allocator) takeFreed() []*pagetables.PTEs {
	a.mu.Lock()
	defer a.mu.Unlock()
	freed := a.freed
	a.freed = nil
	for _, ptes := range freed {
		delete(a.live, ptes)
	}
	return freed
}

// release returns pages taken with takeFreed to ptMemory.
func (a *allocator) release(pages []*pagetables.PTEs) {
	if len(pages) > 0 {
		a.mem.put(pages)
	}
}

// drain returns all pages of the page tables to ptMemory. The page tables
// must not be used again, and no vCPU may walk them.
func (a *allocator) drain() {
	a.mu.Lock()
	defer a.mu.Unlock()
	pages := make([]*pagetables.PTEs, 0, len(a.live))
	for ptes := range a.live {
		pages = append(pages, ptes)
	}
	a.live = nil
	a.freed = nil
	a.mem.put(pages)
}

// newStubPage maps the guest EL1 code into the IPA space and returns its IPA.
func newStubPage(registry *ipa.Registry) (uint64, error) {
	mem, err := unix.Mmap(-1, 0, hostarch.PageSize, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_ANON|unix.MAP_PRIVATE)
	if err != nil {
		return 0, err
	}
	code := stubCode()
	copy(unsafe.Slice((*uint32)(unsafe.Pointer(&mem[0])), len(mem)/4), code)
	var stubIPA uint64
	err = registry.Translate(uintptr(unsafe.Pointer(&mem[0])), hostarch.PageSize, ipa.Read|ipa.Exec, func(_ uintptr, ipa uint64, _ uintptr) {
		stubIPA = ipa
	})
	return stubIPA, err
}

// fpState returns the floating point state of the application: FPSR at 0,
// FPCR at 8, then Q0 to Q31.
func fpState(data []byte) (fpsr, fpcr *uint64, q *[32][16]byte) {
	p := unsafe.Pointer(&data[0])
	return (*uint64)(p), (*uint64)(unsafe.Add(p, 8)), (*[32][16]byte)(unsafe.Add(p, 16))
}
