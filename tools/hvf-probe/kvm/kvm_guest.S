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

// Guest code for the KVM version of the vcpu_run probe (Linux, ELF). The
// host copies it into guest memory.
//
// x1  = MMIO exit address (not mapped in stage 2).
// x2  = SMCCC function ID for the HVC loop.
// x10 = vector report address (not mapped in stage 2).

	.text
	.p2align 2

// One MMIO exit per iteration; no FP/SIMD.
	.globl kvm_guest_mmio
	.globl kvm_guest_mmio_end
kvm_guest_mmio:
1:	str x0, [x1]
	b 1b
kvm_guest_mmio_end:

// As kvm_guest_mmio, and writes d0/d1 before every exit.
	.globl kvm_guest_mmio_fp
	.globl kvm_guest_mmio_fp_end
kvm_guest_mmio_fp:
	mov x0, #0
1:	add x0, x0, #1
	scvtf d0, x0
	fadd d1, d1, d0
	str x0, [x1]
	b 1b
kvm_guest_mmio_fp_end:

// One HVC per iteration. KVM dispatches HVC by the SMCCC function ID in w0,
// so the immediate is 0 here (HVF needs a non-zero immediate instead). The
// host sets x2 to the function ID; x0 is reloaded because the call returns
// a value in it.
	.globl kvm_guest_hvc
	.globl kvm_guest_hvc_end
kvm_guest_hvc:
1:	mov x0, x2
	hvc #0
	b 1b
kvm_guest_hvc_end:

// Vector table: slot n stores n to [x10] (an MMIO exit) and spins.
	.p2align 11
	.globl kvm_guest_vectors
	.globl kvm_guest_vectors_end
kvm_guest_vectors:
	.irp n, 0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15
	.p2align 7
	movz x11, #\n
	str x11, [x10]
	b .
	.endr
kvm_guest_vectors_end:
