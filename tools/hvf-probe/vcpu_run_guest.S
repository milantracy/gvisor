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

// Guest loops for the vcpu_run probe. Each loop exits to the host once per
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
