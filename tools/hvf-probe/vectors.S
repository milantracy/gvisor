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

// Guest EL1 vector table. The host copies it into guest memory. Slot n
// (0..15) executes "hvc #(0x100 + n)" so the host can tell which vector the
// guest took.

	.text
	.p2align 11
	.globl _guest_vectors
	.globl _guest_vectors_end
_guest_vectors:
	.irp n, 0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15
	.p2align 7
	hvc #(0x100 + \n)
	b .
	.endr
_guest_vectors_end:
