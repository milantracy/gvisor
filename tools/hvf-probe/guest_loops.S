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

// Guest loops shared by the probes. Each loop exits to the host once per
// iteration with "hvc #1". The code is position independent; the host copies
// it into guest memory.

	.text
	.p2align 2

// No FP/SIMD use in the guest.
	.globl _guest_clean
	.globl _guest_clean_end
_guest_clean:
1:	hvc #1
	b 1b
_guest_clean_end:

// Writes v0 and v1 before every exit, so the guest FP/SIMD state is live
// (dirty) at each exit.
	.globl _guest_fpdirty
	.globl _guest_fpdirty_end
_guest_fpdirty:
	mov x0, #0
1:	add x0, x0, #1
	scvtf d0, x0
	fadd d1, d1, d0
	hvc #1
	b 1b
_guest_fpdirty_end:

// Spins without exiting. Each iteration keeps these invariants, so the host
// can check guest state at any instruction boundary (host sets d2 = 1.0 and
// v4 = {1, 1}; everything else starts at 0):
//
//   PC offset  x1      d1      v3 lanes
//   0          2*x0    x0      x0
//   4          2*x0-2  x0-1    x0-1
//   8          2*x0    x0-1    x0-1
//   12         2*x0    x0      x0-1
//   16         2*x0    x0      x0
	.globl _guest_spin
	.globl _guest_spin_end
_guest_spin:
1:	add x0, x0, #1
	add x1, x1, #2
	fadd d1, d1, d2
	add v3.2d, v3.2d, v4.2d
	b 1b
_guest_spin_end:

// One memory access per run. The host sets x0 = 0 (load) or 1 (store),
// x1 = address, x2 = value to store, and PC = _guest_memop. A load returns the
// value in x3. The access is followed by "hvc #1". A stage-2 fault exits
// with PC at the ldr/str.
	.globl _guest_memop
	.globl _guest_memop_end
_guest_memop:
	cbnz x0, 1f
	ldr x3, [x1]
	hvc #1
	b _guest_memop
1:	str x2, [x1]
	hvc #1
	b _guest_memop
_guest_memop_end:

// TLBI mailbox, run on every guest entry. The host sets x20 = address of the
// requested generation, x21 = address of this vCPU's acknowledged
// generation, x22 = TLBI kind (0 none, 1 vmalle1, 2 vmalle1is, 3 vae1 of
// x23), and PC = _guest_tlbi_mailbox. If the requested generation is new,
// the stub runs the TLBI, then stores the generation to [x21]. It then spins
// until the host stops the vCPU.
	.globl _guest_tlbi_mailbox
	.globl _guest_tlbi_mailbox_end
_guest_tlbi_mailbox:
	ldr x0, [x20]
	ldr x1, [x21]
	cmp x0, x1
	b.eq 9f
	cbz x22, 8f
	cmp x22, #1
	b.ne 2f
	tlbi vmalle1
	dsb nsh
	isb
	b 8f
2:	cmp x22, #2
	b.ne 3f
	tlbi vmalle1is
	dsb ish
	isb
	b 8f
3:	tlbi vae1, x23
	dsb nsh
	isb
8:	str x0, [x21]
9:	b 9b
_guest_tlbi_mailbox_end:

// HVC loops with one TLBI before each exit, to time the TLBI itself.
	.globl _guest_hvc_vmalle1
	.globl _guest_hvc_vmalle1_end
_guest_hvc_vmalle1:
1:	tlbi vmalle1
	dsb nsh
	isb
	hvc #1
	b 1b
_guest_hvc_vmalle1_end:

	.globl _guest_hvc_vmalle1is
	.globl _guest_hvc_vmalle1is_end
_guest_hvc_vmalle1is:
1:	tlbi vmalle1is
	dsb ish
	isb
	hvc #1
	b 1b
_guest_hvc_vmalle1is_end:

	.globl _guest_hvc_vae1
	.globl _guest_hvc_vae1_end
_guest_hvc_vae1:
1:	tlbi vae1, x23
	dsb nsh
	isb
	hvc #1
	b 1b
_guest_hvc_vae1_end:

// HVC loop with dsb+isb only, the baseline for the TLBI loops.
	.globl _guest_hvc_dsb
	.globl _guest_hvc_dsb_end
_guest_hvc_dsb:
1:	dsb nsh
	isb
	hvc #1
	b 1b
_guest_hvc_dsb_end:
