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

// Package ipa maps host memory into the intermediate physical address (IPA)
// space of a VM on demand.
//
// Hypervisor.framework stage-2 mappings hold the host memory that was mapped
// at a host address when the mapping was made; they do not follow later
// changes to the host mapping. So the owner of each host mapping that can be
// mapped into a guest tells the Registry before it unmaps or replaces the
// mapping (memutil.MappingObserver), and the Registry removes the stage-2
// mappings first.
//
// IPA is never reused: a stale guest stage-1 entry for an IPA whose host
// memory was unmapped causes a stage-2 fault that Refault does not handle,
// and never reaches other memory.
package ipa

import (
	"errors"
	"fmt"

	"gvisor.dev/gvisor/pkg/sync"
)

// Flags are stage-2 permissions. The values are those of
// hv_memory_flags_t.
type Flags uint64

// Permissions.
const (
	Read  Flags = 1 << 0
	Write Flags = 1 << 1
	Exec  Flags = 1 << 2
)

// Mapper changes the stage-2 mappings of a VM.
type Mapper interface {
	// Map maps size bytes of host memory at host to ipa.
	Map(host uintptr, ipa, size uint64, flags Flags) error

	// Unmap unmaps [ipa, ipa+size).
	Unmap(ipa, size uint64) error

	// Protect changes the permissions of [ipa, ipa+size).
	Protect(ipa, size uint64, flags Flags) error
}

// ErrNoSpace is returned when the IPA space is full.
var ErrNoSpace = errors.New("IPA space is full")

// region is a host range that is registered at an IPA.
type region struct {
	host   uintptr
	length uintptr
	ipa    uint64
	flags  Flags
	// mapped is false after WillReplace, until Translate or Refault maps
	// the region again.
	mapped bool
}

func (r *region) end() uintptr {
	return r.host + r.length
}

// Registry maps host ranges to IPA ranges.
type Registry struct {
	mapper   Mapper
	pageSize uintptr
	hugeSize uintptr

	mu sync.Mutex
	// next is the next free IPA. IPA below next is never reused.
	// +checklocks:mu
	next uint64
	// end is the end of the IPA space of the Registry.
	end uint64
	// regions do not overlap and are sorted by host address.
	// +checklocks:mu
	regions []region
}

// New returns a Registry that allocates IPA in [start, end). pageSize is the
// alignment of host ranges and IPA. A region of at least hugeSize bytes gets
// an IPA that is equal to its host address modulo hugeSize, so that the guest
// can map it with block descriptors.
func New(m Mapper, start, end uint64, pageSize, hugeSize uintptr) *Registry {
	return &Registry{
		mapper:   m,
		pageSize: pageSize,
		hugeSize: hugeSize,
		next:     start,
		end:      end,
	}
}

// +checklocks:r.mu
func (r *Registry) alloc(host, length uintptr) (uint64, error) {
	ipa := r.next
	if length >= r.hugeSize {
		huge := uint64(r.hugeSize)
		ipa = (ipa+huge-1)&^(huge-1) + uint64(host%r.hugeSize)
	}
	if ipa+uint64(length) > r.end || ipa+uint64(length) < ipa {
		return 0, ErrNoSpace
	}
	r.next = ipa + uint64(length)
	return ipa, nil
}

// search returns the index of the first region that ends after addr.
//
// +checklocks:r.mu
func (r *Registry) search(addr uintptr) int {
	lo, hi := 0, len(r.regions)
	for lo < hi {
		mid := int(uint(lo+hi) >> 1)
		if r.regions[mid].end() > addr {
			hi = mid
		} else {
			lo = mid + 1
		}
	}
	return lo
}

func (r *Registry) checkAligned(addr, length uintptr) {
	if addr%r.pageSize != 0 || length%r.pageSize != 0 || addr+length < addr {
		panic(fmt.Sprintf("ipa: range [%#x, %#x+%#x) is not page-aligned", addr, addr, length))
	}
}

// Translate calls fn for each part of [addr, addr+length) with the IPA of the
// part, in order. Parts that are not registered yet are registered and mapped
// with flags; registered parts get at least flags.
//
// Preconditions:
//   - addr and length are aligned to the page size.
//   - [addr, addr+length) is mapped on the host.
func (r *Registry) Translate(addr, length uintptr, flags Flags, fn func(addr uintptr, ipa uint64, length uintptr)) error {
	r.checkAligned(addr, length)
	r.mu.Lock()
	defer r.mu.Unlock()
	end := addr + length
	i := r.search(addr)
	for addr < end {
		if i == len(r.regions) || r.regions[i].host > addr {
			// A gap: register it.
			gapEnd := end
			if i < len(r.regions) && r.regions[i].host < end {
				gapEnd = r.regions[i].host
			}
			ipa, err := r.alloc(addr, gapEnd-addr)
			if err != nil {
				return err
			}
			if err := r.mapper.Map(addr, ipa, uint64(gapEnd-addr), flags); err != nil {
				return fmt.Errorf("mapping host [%#x, %#x) at IPA %#x: %w", addr, gapEnd, ipa, err)
			}
			r.regions = append(r.regions, region{})
			copy(r.regions[i+1:], r.regions[i:])
			r.regions[i] = region{host: addr, length: gapEnd - addr, ipa: ipa, flags: flags, mapped: true}
		}
		reg := &r.regions[i]
		if err := r.ensureMapped(reg, flags); err != nil {
			return err
		}
		n := min(end, reg.end()) - addr
		fn(addr, reg.ipa+uint64(addr-reg.host), n)
		addr += n
		i++
	}
	return nil
}

// ensureMapped maps reg if WillReplace unmapped it, and adds flags to it.
//
// +checklocks:r.mu
func (r *Registry) ensureMapped(reg *region, flags Flags) error {
	want := reg.flags | flags
	if !reg.mapped {
		if err := r.mapper.Map(reg.host, reg.ipa, uint64(reg.length), want); err != nil {
			return fmt.Errorf("remapping host [%#x, %#x) at IPA %#x: %w", reg.host, reg.end(), reg.ipa, err)
		}
		reg.mapped = true
		reg.flags = want
		return nil
	}
	if want != reg.flags {
		if err := r.mapper.Protect(reg.ipa, uint64(reg.length), want); err != nil {
			return fmt.Errorf("protecting IPA [%#x, %#x): %w", reg.ipa, reg.ipa+uint64(reg.length), err)
		}
		reg.flags = want
	}
	return nil
}

// Refault handles a stage-2 fault at ipa. If ipa is in a region that
// WillReplace unmapped, Refault maps the region again and returns true. It
// returns false if the fault is not for such a region.
func (r *Registry) Refault(ipa uint64) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.regions {
		reg := &r.regions[i]
		if ipa >= reg.ipa && ipa-reg.ipa < uint64(reg.length) {
			if reg.mapped {
				return false, nil
			}
			return true, r.ensureMapped(reg, 0)
		}
	}
	return false, nil
}

// isolate splits regions so that no region crosses addr or end, and returns
// the indices [first, last) of the regions in [addr, end).
//
// +checklocks:r.mu
func (r *Registry) isolate(addr, end uintptr) (int, int) {
	i := r.search(addr)
	if i < len(r.regions) && r.regions[i].host < addr {
		r.split(i, addr)
		i++
	}
	j := i
	for j < len(r.regions) && r.regions[j].host < end {
		if r.regions[j].end() > end {
			r.split(j, end)
		}
		j++
	}
	return i, j
}

// split splits region i at addr, which is inside it.
//
// +checklocks:r.mu
func (r *Registry) split(i int, addr uintptr) {
	reg := r.regions[i]
	left, right := reg, reg
	left.length = addr - reg.host
	right.host = addr
	right.length = reg.end() - addr
	right.ipa = reg.ipa + uint64(left.length)
	r.regions = append(r.regions, region{})
	copy(r.regions[i+2:], r.regions[i+1:])
	r.regions[i], r.regions[i+1] = left, right
}

// +checklocks:r.mu
func (r *Registry) unmapRegions(first, last int) {
	for k := first; k < last; k++ {
		reg := &r.regions[k]
		if !reg.mapped {
			continue
		}
		if err := r.mapper.Unmap(reg.ipa, uint64(reg.length)); err != nil {
			// The host memory stays reachable from the guest, so do not
			// continue.
			panic(fmt.Sprintf("ipa: unmapping IPA [%#x, %#x) of host [%#x, %#x): %v", reg.ipa, reg.ipa+uint64(reg.length), reg.host, reg.end(), err))
		}
		reg.mapped = false
	}
}

// pageRange returns the page-aligned range that contains [addr,
// addr+length), as munmap and mmap round it.
func (r *Registry) pageRange(addr, length uintptr) (uintptr, uintptr) {
	return addr &^ (r.pageSize - 1), (addr + length + r.pageSize - 1) &^ (r.pageSize - 1)
}

// WillUnmap implements memutil.MappingObserver.WillUnmap. It unmaps the
// stage-2 mappings of [addr, addr+length) and forgets them.
func (r *Registry) WillUnmap(addr, length uintptr) {
	start, end := r.pageRange(addr, length)
	r.mu.Lock()
	defer r.mu.Unlock()
	first, last := r.isolate(start, end)
	r.unmapRegions(first, last)
	r.regions = append(r.regions[:first], r.regions[last:]...)
}

// WillReplace implements memutil.MappingObserver.WillReplace. It unmaps the
// stage-2 mappings of [addr, addr+length) and keeps their IPA, so that
// Translate and Refault map the new host mapping at the same IPA.
func (r *Registry) WillReplace(addr, length uintptr) {
	start, end := r.pageRange(addr, length)
	r.mu.Lock()
	defer r.mu.Unlock()
	first, last := r.isolate(start, end)
	r.unmapRegions(first, last)
}

// Len returns the number of registered regions.
func (r *Registry) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.regions)
}
