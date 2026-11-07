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
	"unsafe"
)

// batchOp is one call fn(a0, a1, a2|p) that batchShim makes. Exactly one of
// a2 and p is not zero; p holds the pointer arguments so that the garbage
// collector sees them.
type batchOp struct {
	fn uintptr
	a0 uintptr
	a1 uintptr
	a2 uintptr
	p  unsafe.Pointer
}

// Batch is a list of register reads and writes on one vCPU. Do makes all of
// them with one call out of Go. Each call out of Go costs more than the
// Hypervisor.framework register access that it makes.
//
// The values that read operations store are valid after Do returns. Pointers
// given to Batch methods must stay valid until Do returns.
type Batch struct {
	id  uint64
	ops []batchOp
}

// NewBatch returns an empty Batch for v.
func (v *VCPU) NewBatch() *Batch {
	return &Batch{id: v.id, ops: make([]batchOp, 0, 128)}
}

// Reset removes all operations from b.
func (b *Batch) Reset() {
	clear(b.ops)
	b.ops = b.ops[:0]
}

// Len returns the number of operations in b.
func (b *Batch) Len() int {
	return len(b.ops)
}

func (b *Batch) add(fn, a1, a2 uintptr, p unsafe.Pointer) {
	b.ops = append(b.ops, batchOp{fn: fn, a0: uintptr(b.id), a1: a1, a2: a2, p: p})
}

// SetReg adds a write of val to r.
func (b *Batch) SetReg(r Reg, val uint64) {
	b.add(libc_hv_vcpu_set_reg_trampoline_addr, uintptr(r), uintptr(val), nil)
}

// Reg adds a read of r into dst.
func (b *Batch) Reg(r Reg, dst *uint64) {
	b.add(libc_hv_vcpu_get_reg_trampoline_addr, uintptr(r), 0, unsafe.Pointer(dst))
}

// SetSysReg adds a write of val to r.
func (b *Batch) SetSysReg(r SysReg, val uint64) {
	b.add(libc_hv_vcpu_set_sys_reg_trampoline_addr, uintptr(r), uintptr(val), nil)
}

// SysReg adds a read of r into dst.
func (b *Batch) SysReg(r SysReg, dst *uint64) {
	b.add(libc_hv_vcpu_get_sys_reg_trampoline_addr, uintptr(r), 0, unsafe.Pointer(dst))
}

// SetSIMDFPReg adds a write of *val to Qn. *val is read when Do runs.
func (b *Batch) SetSIMDFPReg(n int, val *[16]byte) {
	b.add(setSIMDFPRegShimAddr, uintptr(n), 0, unsafe.Pointer(val))
}

// SIMDFPReg adds a read of Qn into dst.
func (b *Batch) SIMDFPReg(n int, dst *[16]byte) {
	b.add(libc_hv_vcpu_get_simd_fp_reg_trampoline_addr, uintptr(n), 0, unsafe.Pointer(dst))
}

// Do makes the operations of b in order, on the thread that created the
// vCPU. It stops at the first operation that fails and returns its error.
// Do does not remove the operations from b.
func (b *Batch) Do() error {
	if len(b.ops) == 0 {
		return nil
	}
	r, _, _ := syscall_rawSyscall(batchShimAddr, uintptr(unsafe.Pointer(&b.ops[0])), uintptr(len(b.ops)), 0)
	return result(r)
}

// batchShimAddr is the address of batchShim, a C function that takes an
// array of batchOp and its length.
var batchShimAddr uintptr
