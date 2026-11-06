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
