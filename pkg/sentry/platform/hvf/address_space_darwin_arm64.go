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

package hvf

import (
	"fmt"
	"runtime"

	"gvisor.dev/gvisor/pkg/atomicbitops"
	pkgcontext "gvisor.dev/gvisor/pkg/context"
	"gvisor.dev/gvisor/pkg/hostarch"
	"gvisor.dev/gvisor/pkg/hvf"
	"gvisor.dev/gvisor/pkg/ring0/pagetables"
	"gvisor.dev/gvisor/pkg/sentry/memmap"
	"gvisor.dev/gvisor/pkg/sentry/platform"
	"gvisor.dev/gvisor/pkg/sentry/platform/hvf/ipa"
	"gvisor.dev/gvisor/pkg/sync"
)

// nextAddressSpaceID is the ID of the next address space. IDs are never
// reused, so that a vCPU can tell address spaces apart after one is freed.
var nextAddressSpaceID atomicbitops.Uint64

// addressSpace is a platform.AddressSpace.
type addressSpace struct {
	platform.NoAddressSpaceIO

	p  *HVF
	id uint64

	// mu serializes changes to pt.
	mu sync.Mutex
	pt *pagetables.PageTables

	// gen is incremented after each change to pt. A vCPU that last ran
	// this address space at an older generation invalidates its TLB.
	gen atomicbitops.Uint64
}

func newAddressSpace(p *HVF) *addressSpace {
	return &addressSpace{
		p:  p,
		id: nextAddressSpaceID.Add(1),
		pt: pagetables.NewWithUpper(p.alloc, p.kernel, upperBottom),
	}
}

// MapFile implements platform.AddressSpace.MapFile.
func (as *addressSpace) MapFile(addr hostarch.Addr, f memmap.File, fr memmap.FileRange, at hostarch.AccessType, precommit bool) error {
	// Internal mappings are never executable.
	bs, err := f.MapInternal(fr, hostarch.AccessType{
		Read:  at.Read || at.Execute || precommit,
		Write: at.Write,
	})
	if err != nil {
		return err
	}
	flags := ipa.Read | ipa.Exec
	if at.Write {
		flags |= ipa.Write
	}
	opts := pagetables.MapOpts{
		AccessType: at,
		User:       true,
		MemoryType: f.MemoryType(),
	}
	as.mu.Lock()
	defer func() {
		as.mu.Unlock()
		as.changed()
	}()
	va := uintptr(addr)
	for !bs.IsEmpty() {
		b := bs.Head()
		bs = bs.Tail()
		host := b.Addr()
		if err := as.p.registry.Translate(host, uintptr(b.Len()), flags, func(h uintptr, ipa uint64, n uintptr) {
			as.mapRange(va+(h-host), n, ipa, opts)
		}); err != nil {
			return err
		}
		va += uintptr(b.Len())
	}
	return nil
}

// mapRange maps [va, va+length) to [ipa, ipa+length).
//
// pagetables makes a block mapping when the VA range covers an aligned huge
// page, and a block maps an aligned IPA range. If va and ipa are not equal
// modulo the huge page size, mapRange maps in pieces of half a huge page,
// which never cover an aligned huge page.
//
// +checklocks:as.mu
func (as *addressSpace) mapRange(va, length uintptr, ipa uint64, opts pagetables.MapOpts) {
	step := length
	if (uint64(va)^ipa)&(hostarch.HugePageSize-1) != 0 {
		step = hostarch.HugePageSize / 2
	}
	for off := uintptr(0); off < length; off += step {
		n := min(step, length-off)
		as.pt.Map(hostarch.Addr(va+off), n, opts, uintptr(ipa)+off)
	}
}

// Unmap implements platform.AddressSpace.Unmap.
func (as *addressSpace) Unmap(addr hostarch.Addr, length uint64) {
	as.mu.Lock()
	as.pt.Unmap(addr, uintptr(length))
	as.mu.Unlock()
	as.changed()
}

// changed bumps the generation and waits until no vCPU runs the address
// space with an older generation.
func (as *addressSpace) changed() {
	as.gen.Add(1)
	for _, v := range as.p.vcpus {
		if v.curAS.Load() != as || !v.inGuest.Load() {
			// v reads the generation after it sets inGuest, so it
			// sees the new one.
			continue
		}
		runs := v.runs.Load()
		if err := hvf.ExitVCPUs(v.id); err != nil {
			panic(fmt.Sprintf("hvf: stopping vCPU %d: %v", v.id, err))
		}
		for v.runs.Load() == runs && v.inGuest.Load() {
			runtime.Gosched()
		}
	}
}

// Release implements platform.AddressSpace.Release.
func (as *addressSpace) Release() {
	as.mu.Lock()
	defer as.mu.Unlock()
	as.pt.Unmap(0, maxUserAddress)
	as.pt.Allocator.Recycle()
}

// PreFork implements platform.AddressSpace.PreFork.
func (*addressSpace) PreFork() {}

// PostFork implements platform.AddressSpace.PostFork.
func (*addressSpace) PostFork() {}

// UserModifiedGS implements platform.AddressSpace.UserModifiedGS.
func (*addressSpace) UserModifiedGS(pkgcontext.Context, platform.MemoryManager) error {
	return nil
}
