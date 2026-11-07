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
	"errors"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
	"gvisor.dev/gvisor/pkg/hvf/hvftest"
)

// TestMain runs the tests in a binary with the hypervisor entitlement.
func TestMain(m *testing.M) {
	hvftest.Main(m)
}

const (
	pageSize = 16 << 10
	// guestBase is the IPA of the first guest page.
	guestBase = 0x10000000
	// cpsrEL1h is EL1h with D, A, I and F masked.
	cpsrEL1h = 0x3c5
)

// Instructions.
const (
	insnHVC1   = 0xd4000022 // hvc #1
	insnBSelf  = 0x14000000 // b .
	insnBBack1 = 0x17ffffff // b .-4
	insnAddX0  = 0x91000400 // add x0, x0, #1
)

// movzX0 returns "movz x0, #imm".
func movzX0(imm uint16) uint32 {
	return 0xd2800000 | uint32(imm)<<5
}

var (
	vmOnce sync.Once
	vmErr  error
	// nextIPA is the IPA of the next guest page.
	nextIPA atomic.Uint64
)

func setupVM(t testing.TB) {
	t.Helper()
	vmOnce.Do(func() {
		bits, err := MaxIPASize()
		if err != nil {
			vmErr = fmt.Errorf("MaxIPASize: %w", err)
			return
		}
		vmErr = CreateVM(min(bits, 40))
		nextIPA.Store(guestBase)
	})
	if errors.Is(vmErr, ErrDenied) {
		t.Skipf("CreateVM: %v; the binary needs the com.apple.security.hypervisor entitlement", vmErr)
	}
	if vmErr != nil {
		t.Fatalf("CreateVM: %v", vmErr)
	}
}

// newGuest maps code at a new IPA and returns a vCPU at EL1h with the MMU off
// that starts at the first instruction. It locks the goroutine to its thread
// until the test ends, because a vCPU can only be used and destroyed on the
// thread that created it.
func newGuest(t testing.TB, code ...uint32) *VCPU {
	t.Helper()
	runtime.LockOSThread()
	// Cleanups run in reverse order, so this runs after Destroy.
	t.Cleanup(runtime.UnlockOSThread)
	setupVM(t)
	mem, err := unix.Mmap(-1, 0, pageSize, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_ANON|unix.MAP_PRIVATE)
	if err != nil {
		t.Fatalf("mmap: %v", err)
	}
	t.Cleanup(func() { unix.Munmap(mem) })
	insns := unsafe.Slice((*uint32)(unsafe.Pointer(&mem[0])), len(mem)/4)
	copy(insns, code)
	ipa := nextIPA.Add(pageSize) - pageSize
	if err := Map(uintptr(unsafe.Pointer(&mem[0])), ipa, pageSize, MemoryRead|MemoryWrite|MemoryExec); err != nil {
		t.Fatalf("Map: %v", err)
	}
	t.Cleanup(func() {
		if err := Unmap(ipa, pageSize); err != nil {
			t.Errorf("Unmap: %v", err)
		}
	})
	v, err := NewVCPU()
	if err != nil {
		t.Fatalf("NewVCPU: %v", err)
	}
	t.Cleanup(func() {
		if err := v.Destroy(); err != nil {
			t.Errorf("Destroy: %v", err)
		}
	})
	if err := v.SetReg(RegPC, ipa); err != nil {
		t.Fatalf("SetReg(PC): %v", err)
	}
	if err := v.SetReg(RegCPSR, cpsrEL1h); err != nil {
		t.Fatalf("SetReg(CPSR): %v", err)
	}
	return v
}

func TestHVC(t *testing.T) {
	v := newGuest(t, movzX0(0x1234), insnAddX0, insnHVC1, insnBSelf)
	if err := v.Run(); err != nil {
		t.Fatalf("Run: %v", err)
	}
	e := v.Exit()
	if e.Reason != ExitException || e.EC() != ECHVC64 || e.ISS()&0xffff != 1 {
		t.Fatalf("exit = %+v (EC %#x, ISS %#x), want an HVC #1 exception", *e, e.EC(), e.ISS())
	}
	x0, err := v.Reg(RegX0)
	if err != nil {
		t.Fatalf("Reg(X0): %v", err)
	}
	if x0 != 0x1235 {
		t.Errorf("X0 = %#x, want 0x1235", x0)
	}
}

func TestRegisters(t *testing.T) {
	v := newGuest(t, insnBSelf)
	if err := v.SetReg(RegX0+7, 0xdeadbeef); err != nil {
		t.Fatalf("SetReg: %v", err)
	}
	if got, err := v.Reg(RegX0 + 7); err != nil || got != 0xdeadbeef {
		t.Errorf("Reg(X7) = %#x, %v, want 0xdeadbeef", got, err)
	}
	if err := v.SetSysReg(SysRegTPIDREL0, 0xcafe); err != nil {
		t.Fatalf("SetSysReg: %v", err)
	}
	if got, err := v.SysReg(SysRegTPIDREL0); err != nil || got != 0xcafe {
		t.Errorf("SysReg(TPIDR_EL0) = %#x, %v, want 0xcafe", got, err)
	}
	var q [16]byte
	for i := range q {
		q[i] = byte(i + 1)
	}
	if err := v.SetSIMDFPReg(3, q); err != nil {
		t.Fatalf("SetSIMDFPReg: %v", err)
	}
	if got, err := v.SIMDFPReg(3); err != nil || got != q {
		t.Errorf("SIMDFPReg(3) = %v, %v, want %v", got, err, q)
	}
}

func TestExitVCPUs(t *testing.T) {
	v := newGuest(t, insnBSelf)
	id := v.ID()
	timer := time.AfterFunc(20*time.Millisecond, func() {
		if err := ExitVCPUs(id); err != nil {
			t.Errorf("ExitVCPUs: %v", err)
		}
	})
	defer timer.Stop()
	if err := v.Run(); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if r := v.Exit().Reason; r != ExitCanceled {
		t.Errorf("exit reason = %d, want ExitCanceled", r)
	}
}

func TestMapErrors(t *testing.T) {
	setupVM(t)
	// IPA not aligned to 16 KiB.
	mem, err := unix.Mmap(-1, 0, pageSize, unix.PROT_READ, unix.MAP_ANON|unix.MAP_PRIVATE)
	if err != nil {
		t.Fatalf("mmap: %v", err)
	}
	defer unix.Munmap(mem)
	if err := Map(uintptr(unsafe.Pointer(&mem[0])), 0x1000, pageSize, MemoryRead); err == nil {
		Unmap(0x1000, pageSize)
		t.Errorf("Map at IPA 0x1000 succeeded, want an error")
	}
}

// BenchmarkHVCRoundTrip measures one guest HVC exit and re-entry.
func BenchmarkHVCRoundTrip(b *testing.B) {
	v := newGuest(b, insnHVC1, insnBBack1)
	for b.Loop() {
		if err := v.Run(); err != nil {
			b.Fatalf("Run: %v", err)
		}
	}
	if e := v.Exit(); e.EC() != ECHVC64 {
		b.Fatalf("exit = %+v, want HVC", *e)
	}
}

// BenchmarkSetReg measures one general register write.
func BenchmarkSetReg(b *testing.B) {
	v := newGuest(b, insnBSelf)
	for b.Loop() {
		if err := v.SetReg(RegX0, 1); err != nil {
			b.Fatalf("SetReg: %v", err)
		}
	}
}

// BenchmarkReg measures one general register read.
func BenchmarkReg(b *testing.B) {
	v := newGuest(b, insnBSelf)
	for b.Loop() {
		if _, err := v.Reg(RegX0); err != nil {
			b.Fatalf("Reg: %v", err)
		}
	}
}

// BenchmarkSetSIMDFPReg measures one SIMD and FP register write.
func BenchmarkSetSIMDFPReg(b *testing.B) {
	v := newGuest(b, insnBSelf)
	var q [16]byte
	for b.Loop() {
		if err := v.SetSIMDFPReg(0, q); err != nil {
			b.Fatalf("SetSIMDFPReg: %v", err)
		}
	}
}

// BenchmarkSIMDFPReg measures one SIMD and FP register read.
func BenchmarkSIMDFPReg(b *testing.B) {
	v := newGuest(b, insnBSelf)
	for b.Loop() {
		if _, err := v.SIMDFPReg(0); err != nil {
			b.Fatalf("SIMDFPReg: %v", err)
		}
	}
}
