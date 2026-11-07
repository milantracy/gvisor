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
	"encoding/binary"
	"errors"
	"os"
	"testing"
	"time"

	"gvisor.dev/gvisor/pkg/abi/linux"
	pkgcontext "gvisor.dev/gvisor/pkg/context"
	"gvisor.dev/gvisor/pkg/hostarch"
	"gvisor.dev/gvisor/pkg/hvf"
	"gvisor.dev/gvisor/pkg/hvf/hvftest"
	"gvisor.dev/gvisor/pkg/memutil"
	"gvisor.dev/gvisor/pkg/safemem"
	"gvisor.dev/gvisor/pkg/sentry/arch"
	"gvisor.dev/gvisor/pkg/sentry/memmap"
	"gvisor.dev/gvisor/pkg/sentry/pgalloc"
	"gvisor.dev/gvisor/pkg/sentry/platform"
	"gvisor.dev/gvisor/pkg/sentry/usage"
)

func TestMain(m *testing.M) {
	hvftest.Main(m)
}

// The test program, at codeAddr.
var program = []uint32{
	0xd2801588, // 0x00: movz x8, #172
	0xd4000001, // 0x04: svc #0
	0x9e660002, // 0x08: fmov x2, d0
	0xd53bd043, // 0x0c: mrs x3, tpidr_el0
	0xd4000001, // 0x10: svc #0
	0xf9000020, // 0x14: str x0, [x1]
	0xd4000001, // 0x18: svc #0
	0x14000000, // 0x1c: b .
	0xd4000001, // 0x20: svc #0
	0x17ffffff, // 0x24: b .-4
}

const (
	codeAddr = 0x400000
	dataAddr = 0x10000000
)

// testMM is a platform.MemoryManager with only an address space.
type testMM struct {
	platform.MemoryManager
	as platform.AddressSpace
}

func (m *testMM) AddressSpace() platform.AddressSpace {
	return m.as
}

type testEnv struct {
	p   *HVF
	mf  *pgalloc.MemoryFile
	as  platform.AddressSpace
	mm  *testMM
	c   platform.Context
	ac  *arch.Context64
	ctx pkgcontext.Context
}

func newTestEnv(t testing.TB) *testEnv {
	t.Helper()
	p, err := New()
	if errors.Is(err, hvf.ErrDenied) {
		t.Skipf("New: %v", err)
	}
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	fd, err := memutil.CreateMemFD("hvf-test", 0)
	if err != nil {
		t.Fatalf("CreateMemFD: %v", err)
	}
	mf, err := pgalloc.NewMemoryFile(os.NewFile(uintptr(fd), "hvf-test"), pgalloc.MemoryFileOpts{})
	if err != nil {
		t.Fatalf("NewMemoryFile: %v", err)
	}
	t.Cleanup(func() { mf.Destroy() })
	as, err := p.NewAddressSpace(platform.AddressSpaceOptions{})
	if err != nil {
		t.Fatalf("NewAddressSpace: %v", err)
	}
	t.Cleanup(as.Release)
	ctx := pkgcontext.Background()
	e := &testEnv{p: p, mf: mf, as: as, mm: &testMM{as: as}, c: p.NewContext(ctx), ac: arch.New(arch.ARM64), ctx: ctx}

	code := make([]byte, 4*len(program))
	for i, insn := range program {
		binary.LittleEndian.PutUint32(code[4*i:], insn)
	}
	fr := e.allocate(t, code)
	if err := as.MapFile(codeAddr, mf, fr, hostarch.ReadExecute, false); err != nil {
		t.Fatalf("MapFile(code): %v", err)
	}
	return e
}

// allocate returns a page of mf that starts with data.
func (e *testEnv) allocate(t testing.TB, data []byte) memmap.FileRange {
	t.Helper()
	fr, err := e.mf.Allocate(hostarch.PageSize, pgalloc.AllocOpts{Kind: usage.Anonymous})
	if err != nil {
		t.Fatalf("Allocate: %v", err)
	}
	ims, err := e.mf.MapInternal(fr, hostarch.Write)
	if err != nil {
		t.Fatalf("MapInternal: %v", err)
	}
	if _, err := safemem.CopySeq(ims, safemem.BlockSeqOf(safemem.BlockFromSafeSlice(data))); err != nil {
		t.Fatalf("CopySeq: %v", err)
	}
	return fr
}

func (e *testEnv) regs() *arch.Registers {
	return &e.ac.StateData().Regs
}

func (e *testEnv) switchTo(t testing.TB) (*linux.SignalInfo, hostarch.AccessType, error) {
	t.Helper()
	return e.c.Switch(e.ctx, e.mm, e.ac, -1)
}

func (e *testEnv) wantSyscall(t *testing.T, pc uint64) {
	t.Helper()
	if _, _, err := e.switchTo(t); err != nil {
		t.Fatalf("Switch: %v, want a system call", err)
	}
	if got := e.regs().Pc; got != pc {
		t.Fatalf("PC after system call = %#x, want %#x", got, pc)
	}
}

func TestProgram(t *testing.T) {
	e := newTestEnv(t)
	regs := e.regs()

	// A system call.
	regs.Pc = codeAddr
	e.wantSyscall(t, codeAddr+0x8)
	if regs.Regs[8] != 172 {
		t.Errorf("X8 = %d, want 172", regs.Regs[8])
	}

	// FP and TLS state goes in and comes out.
	fpsr, fpcr, q := fpState(*e.ac.FloatingPointData())
	_, _ = fpsr, fpcr
	binary.LittleEndian.PutUint64(q[0][:], 0x1122334455667788)
	regs.TPIDR_EL0 = 0xabcd
	e.wantSyscall(t, codeAddr+0x14)
	if regs.Regs[2] != 0x1122334455667788 {
		t.Errorf("X2 = %#x, want D0 0x1122334455667788", regs.Regs[2])
	}
	if regs.Regs[3] != 0xabcd {
		t.Errorf("X3 = %#x, want TPIDR_EL0 0xabcd", regs.Regs[3])
	}

	// A store to unmapped memory faults.
	regs.Regs[0], regs.Regs[1] = 42, dataAddr
	info, at, err := e.switchTo(t)
	if err != platform.ErrContextSignal || info.Signo != int32(linux.SIGSEGV) || info.Addr() != dataAddr || !at.Write {
		t.Fatalf("Switch = %+v, %v, %v; want SIGSEGV at %#x for write", info, at, err, dataAddr)
	}
	if regs.Pc != codeAddr+0x14 {
		t.Fatalf("PC after fault = %#x, want %#x", regs.Pc, codeAddr+0x14)
	}

	// After the memory is mapped, the store succeeds.
	fr := e.allocate(t, make([]byte, 8))
	if err := e.as.MapFile(dataAddr, e.mf, fr, hostarch.ReadWrite, false); err != nil {
		t.Fatalf("MapFile(data): %v", err)
	}
	e.wantSyscall(t, codeAddr+0x1c)
	buf := make([]byte, 8)
	ims, _ := e.mf.MapInternal(fr, hostarch.Read)
	safemem.CopySeq(safemem.BlockSeqOf(safemem.BlockFromSafeSlice(buf)), ims)
	if got := binary.LittleEndian.Uint64(buf); got != 42 {
		t.Errorf("stored value = %d, want 42", got)
	}

	// An interrupt stops a guest loop.
	timer := time.AfterFunc(20*time.Millisecond, e.c.Interrupt)
	defer timer.Stop()
	if _, _, err := e.switchTo(t); err != platform.ErrContextInterrupt {
		t.Fatalf("Switch = %v, want ErrContextInterrupt", err)
	}
	if regs.Pc != codeAddr+0x1c {
		t.Errorf("PC after interrupt = %#x, want %#x", regs.Pc, codeAddr+0x1c)
	}

	// After Unmap, the store faults again: the TLB was invalidated.
	e.as.Unmap(dataAddr, hostarch.PageSize)
	regs.Pc = codeAddr + 0x14
	if info, _, err := e.switchTo(t); err != platform.ErrContextSignal || info.Signo != int32(linux.SIGSEGV) {
		t.Fatalf("Switch after Unmap = %+v, %v, want SIGSEGV", info, err)
	}
}

func TestUndefinedInstruction(t *testing.T) {
	e := newTestEnv(t)
	// The zero word after the program is an undefined instruction.
	e.regs().Pc = codeAddr + uint64(4*len(program))
	info, _, err := e.switchTo(t)
	if err != platform.ErrContextSignal || info.Signo != int32(linux.SIGILL) {
		t.Fatalf("Switch = %+v, %v, want SIGILL", info, err)
	}
}

// inUse returns the number of page table pages that are not free.
func (m *ptMemory) inUse() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.chunks)*ptChunkSize/hostarch.PageSize - int(m.end-m.next)/hostarch.PageSize - len(m.pool)
}

// TestPageTablePages checks that Unmap returns the page table pages that it
// frees, and that Release returns all pages of an address space, including
// the root.
func TestPageTablePages(t *testing.T) {
	e := newTestEnv(t)
	fr := e.allocate(t, []byte{1})
	// Addresses that are in different tables at every level.
	addrs := []hostarch.Addr{0x400000, 1 << 36, 1 << 46}
	base := e.p.ptMem.inUse()
	for i := 0; i < 50; i++ {
		as, err := e.p.NewAddressSpace(platform.AddressSpaceOptions{})
		if err != nil {
			t.Fatalf("NewAddressSpace: %v", err)
		}
		empty := e.p.ptMem.inUse()
		for _, addr := range addrs {
			if err := as.MapFile(addr, e.mf, fr, hostarch.Read, false); err != nil {
				t.Fatalf("MapFile(%#x): %v", addr, err)
			}
		}
		// pagetables frees a table only when the range covers all of it.
		as.Unmap(0, maxUserAddress)
		if got := e.p.ptMem.inUse(); got != empty {
			t.Fatalf("after Unmap: %d pages in use, want %d", got, empty)
		}
		for _, addr := range addrs {
			if err := as.MapFile(addr, e.mf, fr, hostarch.Read, false); err != nil {
				t.Fatalf("MapFile(%#x): %v", addr, err)
			}
		}
		as.Release()
	}
	if got := e.p.ptMem.inUse(); got != base {
		t.Errorf("after Release: %d pages in use, want %d", got, base)
	}
}

// BenchmarkSyscall measures a system call through Switch.
func BenchmarkSyscall(b *testing.B) {
	e := newTestEnv(b)
	e.regs().Pc = codeAddr + 0x20
	for b.Loop() {
		if _, _, err := e.switchTo(b); err != nil {
			b.Fatalf("Switch: %v", err)
		}
	}
}
