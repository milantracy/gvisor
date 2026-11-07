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

package ipa

import (
	"errors"
	"fmt"
	"testing"

	"gvisor.dev/gvisor/pkg/memutil"
)

const (
	page = 16 << 10
	huge = 32 << 20
	// ipaStart is the start of the IPA space in tests.
	ipaStart = 1 << 30
)

var _ memutil.MappingObserver = (*Registry)(nil)

// fakeMapper is a Mapper that keeps a stage-2 page table. Like
// Hypervisor.framework, it rejects a Map over a mapped IPA.
type fakeMapper struct {
	// pages maps an IPA page to its host page and flags.
	pages map[uint64]fakePage
	maps  int
}

type fakePage struct {
	host  uintptr
	flags Flags
}

func newFakeMapper() *fakeMapper {
	return &fakeMapper{pages: make(map[uint64]fakePage)}
}

func (m *fakeMapper) Map(host uintptr, ipa, size uint64, flags Flags) error {
	for off := uint64(0); off < size; off += page {
		if _, ok := m.pages[ipa+off]; ok {
			return fmt.Errorf("IPA %#x is mapped", ipa+off)
		}
	}
	for off := uint64(0); off < size; off += page {
		m.pages[ipa+off] = fakePage{host + uintptr(off), flags}
	}
	m.maps++
	return nil
}

func (m *fakeMapper) Unmap(ipa, size uint64) error {
	for off := uint64(0); off < size; off += page {
		delete(m.pages, ipa+off)
	}
	return nil
}

func (m *fakeMapper) Protect(ipa, size uint64, flags Flags) error {
	for off := uint64(0); off < size; off += page {
		p, ok := m.pages[ipa+off]
		if !ok {
			return fmt.Errorf("IPA %#x is not mapped", ipa+off)
		}
		p.flags = flags
		m.pages[ipa+off] = p
	}
	return nil
}

type part struct {
	addr   uintptr
	ipa    uint64
	length uintptr
}

func translate(t *testing.T, r *Registry, addr, length uintptr, flags Flags) []part {
	t.Helper()
	var parts []part
	if err := r.Translate(addr, length, flags, func(addr uintptr, ipa uint64, length uintptr) {
		parts = append(parts, part{addr, ipa, length})
	}); err != nil {
		t.Fatalf("Translate(%#x, %#x): %v", addr, length, err)
	}
	return parts
}

// checkStage2 checks that each part is mapped in m to its host pages.
func checkStage2(t *testing.T, m *fakeMapper, parts []part) {
	t.Helper()
	for _, p := range parts {
		for off := uintptr(0); off < p.length; off += page {
			got, ok := m.pages[p.ipa+uint64(off)]
			if !ok || got.host != p.addr+off {
				t.Errorf("IPA %#x maps %+v (mapped %t), want host %#x", p.ipa+uint64(off), got, ok, p.addr+off)
			}
		}
	}
}

func TestTranslateRegistersOnce(t *testing.T) {
	m := newFakeMapper()
	r := New(m, ipaStart, 1<<40, page, huge)
	a := translate(t, r, 0x100000, 4*page, Read|Write)
	b := translate(t, r, 0x100000+page, 2*page, Read)
	if len(a) != 1 || len(b) != 1 {
		t.Fatalf("got parts %v and %v, want one each", a, b)
	}
	if b[0].ipa != a[0].ipa+page {
		t.Errorf("second IPA = %#x, want %#x", b[0].ipa, a[0].ipa+page)
	}
	if m.maps != 1 {
		t.Errorf("Map called %d times, want 1", m.maps)
	}
	checkStage2(t, m, append(a, b...))
}

func TestTranslateFillsGaps(t *testing.T) {
	m := newFakeMapper()
	r := New(m, ipaStart, 1<<40, page, huge)
	translate(t, r, 0x100000+page, page, Read)
	translate(t, r, 0x100000+3*page, page, Read)
	parts := translate(t, r, 0x100000, 5*page, Read)
	want := []uintptr{page, page, page, page, page}
	if len(parts) != len(want) {
		t.Fatalf("got %d parts (%v), want %d", len(parts), parts, len(want))
	}
	for i, p := range parts {
		if p.addr != 0x100000+uintptr(i)*page || p.length != want[i] {
			t.Errorf("part %d = %+v", i, p)
		}
	}
	if r.Len() != 5 {
		t.Errorf("Len() = %d, want 5", r.Len())
	}
	checkStage2(t, m, parts)
}

func TestFlagsUpgrade(t *testing.T) {
	m := newFakeMapper()
	r := New(m, ipaStart, 1<<40, page, huge)
	p := translate(t, r, 0x100000, page, Read)
	translate(t, r, 0x100000, page, Write)
	if got := m.pages[p[0].ipa].flags; got != Read|Write {
		t.Errorf("flags = %#x, want Read|Write", got)
	}
}

func TestWillUnmap(t *testing.T) {
	m := newFakeMapper()
	r := New(m, ipaStart, 1<<40, page, huge)
	old := translate(t, r, 0x100000, 3*page, Read)
	r.WillUnmap(0x100000+page, page)
	if _, ok := m.pages[old[0].ipa+page]; ok {
		t.Errorf("middle IPA still mapped after WillUnmap")
	}
	if _, ok := m.pages[old[0].ipa]; !ok {
		t.Errorf("first IPA unmapped by WillUnmap of the middle page")
	}
	if ok, _ := r.Refault(old[0].ipa + page); ok {
		t.Errorf("Refault of an unmapped range returned true")
	}
	// New host memory at the same address gets a new IPA.
	parts := translate(t, r, 0x100000+page, page, Read)
	if parts[0].ipa == old[0].ipa+page {
		t.Errorf("IPA %#x reused", parts[0].ipa)
	}
	checkStage2(t, m, parts)
	if r.Len() != 3 {
		t.Errorf("Len() = %d, want 3", r.Len())
	}
}

func TestWillUnmapRoundsUp(t *testing.T) {
	m := newFakeMapper()
	r := New(m, ipaStart, 1<<40, page, huge)
	translate(t, r, 0x100000, 2*page, Read)
	r.WillUnmap(0x100000, page+1)
	if len(m.pages) != 0 || r.Len() != 0 {
		t.Errorf("after WillUnmap: %d pages mapped, %d regions, want 0", len(m.pages), r.Len())
	}
}

func TestWillReplace(t *testing.T) {
	m := newFakeMapper()
	r := New(m, ipaStart, 1<<40, page, huge)
	old := translate(t, r, 0x100000, 2*page, Read)
	r.WillReplace(0x100000, 2*page)
	if len(m.pages) != 0 {
		t.Fatalf("%d pages mapped after WillReplace, want 0", len(m.pages))
	}
	ok, err := r.Refault(old[0].ipa + page)
	if !ok || err != nil {
		t.Fatalf("Refault = %t, %v, want true, nil", ok, err)
	}
	checkStage2(t, m, old)
	// A mapped region does not refault.
	if ok, _ := r.Refault(old[0].ipa); ok {
		t.Errorf("Refault of a mapped IPA returned true")
	}
	// Translate maps a replaced region at the same IPA.
	r.WillReplace(0x100000, page)
	parts := translate(t, r, 0x100000, 2*page, Read|Write)
	if parts[0].ipa != old[0].ipa {
		t.Errorf("IPA after WillReplace = %#x, want %#x", parts[0].ipa, old[0].ipa)
	}
	checkStage2(t, m, parts)
}

func TestHugeAlignment(t *testing.T) {
	m := newFakeMapper()
	r := New(m, ipaStart+page, 1<<40, page, huge)
	host := uintptr(7*huge + 3*page)
	parts := translate(t, r, host, 2*huge, Read)
	if parts[0].ipa%huge != uint64(host%huge) {
		t.Errorf("IPA %#x is not equal to host %#x modulo %#x", parts[0].ipa, host, huge)
	}
}

func TestNoSpace(t *testing.T) {
	m := newFakeMapper()
	r := New(m, ipaStart, ipaStart+2*page, page, huge)
	translate(t, r, 0x100000, 2*page, Read)
	err := r.Translate(0x200000, page, Read, func(uintptr, uint64, uintptr) {})
	if !errors.Is(err, ErrNoSpace) {
		t.Errorf("Translate = %v, want ErrNoSpace", err)
	}
}
