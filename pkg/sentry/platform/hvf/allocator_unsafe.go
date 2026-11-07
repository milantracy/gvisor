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

// allocator is a pagetables.Allocator that allocates page tables in memory
// that the guest can walk: anonymous host memory that the registry maps.
type allocator struct {
	registry *ipa.Registry

	mu sync.Mutex
	// chunks are never freed.
	// +checklocks:mu
	chunks []ptChunk
	// next and end are the unused part of the last chunk.
	// +checklocks:mu
	next, end uintptr
	// +checklocks:mu
	pool []*pagetables.PTEs
	// +checklocks:mu
	freed []*pagetables.PTEs
}

var _ pagetables.Allocator = (*allocator)(nil)

func newAllocator(registry *ipa.Registry) *allocator {
	return &allocator{registry: registry}
}

// +checklocks:a.mu
func (a *allocator) newChunk() {
	mem, err := unix.Mmap(-1, 0, ptChunkSize, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_ANON|unix.MAP_PRIVATE)
	if err != nil {
		panic(fmt.Sprintf("hvf: allocating page table memory: %v", err))
	}
	host := uintptr(unsafe.Pointer(&mem[0]))
	var parts int
	if err := a.registry.Translate(host, ptChunkSize, ipa.Read|ipa.Write, func(addr uintptr, ipa uint64, length uintptr) {
		a.chunks = append(a.chunks, ptChunk{host: addr, ipa: ipa})
		parts++
	}); err != nil {
		panic(fmt.Sprintf("hvf: mapping page table memory: %v", err))
	}
	if parts != 1 {
		panic(fmt.Sprintf("hvf: page table memory has %d IPA parts", parts))
	}
	a.next, a.end = host, host+ptChunkSize
}

// NewPTEs implements pagetables.Allocator.NewPTEs.
func (a *allocator) NewPTEs() *pagetables.PTEs {
	a.mu.Lock()
	defer a.mu.Unlock()
	if n := len(a.pool); n > 0 {
		ptes := a.pool[n-1]
		a.pool = a.pool[:n-1]
		return ptes
	}
	if a.next == a.end {
		a.newChunk()
	}
	ptes := (*pagetables.PTEs)(unsafe.Pointer(a.next))
	a.next += hostarch.PageSize
	return ptes
}

// PhysicalFor implements pagetables.Allocator.PhysicalFor. It returns the IPA
// of ptes.
func (a *allocator) PhysicalFor(ptes *pagetables.PTEs) uintptr {
	addr := uintptr(unsafe.Pointer(ptes))
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, c := range a.chunks {
		if addr >= c.host && addr-c.host < ptChunkSize {
			return uintptr(c.ipa) + (addr - c.host)
		}
	}
	panic(fmt.Sprintf("hvf: PTEs %#x are not in page table memory", addr))
}

// LookupPTEs implements pagetables.Allocator.LookupPTEs.
func (a *allocator) LookupPTEs(physical uintptr) *pagetables.PTEs {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, c := range a.chunks {
		if uint64(physical) >= c.ipa && uint64(physical)-c.ipa < ptChunkSize {
			return (*pagetables.PTEs)(unsafe.Pointer(c.host + (physical - uintptr(c.ipa))))
		}
	}
	panic(fmt.Sprintf("hvf: IPA %#x is not in page table memory", physical))
}

// FreePTEs implements pagetables.Allocator.FreePTEs.
func (a *allocator) FreePTEs(ptes *pagetables.PTEs) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.freed = append(a.freed, ptes)
}

// Recycle implements pagetables.Allocator.Recycle.
func (a *allocator) Recycle() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.pool = append(a.pool, a.freed...)
	a.freed = a.freed[:0]
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
