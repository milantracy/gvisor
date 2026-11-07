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

#include "textflag.h"

TEXT libc_sandbox_init_trampoline<>(SB),NOSPLIT,$0-0
	JMP	libc_sandbox_init(SB)
GLOBL	·libc_sandbox_init_trampoline_addr(SB), RODATA, $8
DATA	·libc_sandbox_init_trampoline_addr(SB)/8, $libc_sandbox_init_trampoline<>(SB)

TEXT libc_sandbox_free_error_trampoline<>(SB),NOSPLIT,$0-0
	JMP	libc_sandbox_free_error(SB)
GLOBL	·libc_sandbox_free_error_trampoline_addr(SB), RODATA, $8
DATA	·libc_sandbox_free_error_trampoline_addr(SB)/8, $libc_sandbox_free_error_trampoline<>(SB)

TEXT libc_fcntl_trampoline<>(SB),NOSPLIT,$0-0
	JMP	libc_fcntl(SB)
GLOBL	·libc_fcntl_trampoline_addr(SB), RODATA, $8
DATA	·libc_fcntl_trampoline_addr(SB)/8, $libc_fcntl_trampoline<>(SB)
