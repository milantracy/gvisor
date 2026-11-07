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

TEXT libc_hv_vm_config_create_trampoline<>(SB),NOSPLIT,$0-0
	JMP	libc_hv_vm_config_create(SB)
GLOBL	·libc_hv_vm_config_create_trampoline_addr(SB), RODATA, $8
DATA	·libc_hv_vm_config_create_trampoline_addr(SB)/8, $libc_hv_vm_config_create_trampoline<>(SB)

TEXT libc_hv_vm_config_set_ipa_size_trampoline<>(SB),NOSPLIT,$0-0
	JMP	libc_hv_vm_config_set_ipa_size(SB)
GLOBL	·libc_hv_vm_config_set_ipa_size_trampoline_addr(SB), RODATA, $8
DATA	·libc_hv_vm_config_set_ipa_size_trampoline_addr(SB)/8, $libc_hv_vm_config_set_ipa_size_trampoline<>(SB)

TEXT libc_hv_vm_config_get_max_ipa_size_trampoline<>(SB),NOSPLIT,$0-0
	JMP	libc_hv_vm_config_get_max_ipa_size(SB)
GLOBL	·libc_hv_vm_config_get_max_ipa_size_trampoline_addr(SB), RODATA, $8
DATA	·libc_hv_vm_config_get_max_ipa_size_trampoline_addr(SB)/8, $libc_hv_vm_config_get_max_ipa_size_trampoline<>(SB)

TEXT libc_hv_vm_create_trampoline<>(SB),NOSPLIT,$0-0
	JMP	libc_hv_vm_create(SB)
GLOBL	·libc_hv_vm_create_trampoline_addr(SB), RODATA, $8
DATA	·libc_hv_vm_create_trampoline_addr(SB)/8, $libc_hv_vm_create_trampoline<>(SB)

TEXT libc_hv_vm_destroy_trampoline<>(SB),NOSPLIT,$0-0
	JMP	libc_hv_vm_destroy(SB)
GLOBL	·libc_hv_vm_destroy_trampoline_addr(SB), RODATA, $8
DATA	·libc_hv_vm_destroy_trampoline_addr(SB)/8, $libc_hv_vm_destroy_trampoline<>(SB)

TEXT libc_hv_vm_map_trampoline<>(SB),NOSPLIT,$0-0
	JMP	libc_hv_vm_map(SB)
GLOBL	·libc_hv_vm_map_trampoline_addr(SB), RODATA, $8
DATA	·libc_hv_vm_map_trampoline_addr(SB)/8, $libc_hv_vm_map_trampoline<>(SB)

TEXT libc_hv_vm_unmap_trampoline<>(SB),NOSPLIT,$0-0
	JMP	libc_hv_vm_unmap(SB)
GLOBL	·libc_hv_vm_unmap_trampoline_addr(SB), RODATA, $8
DATA	·libc_hv_vm_unmap_trampoline_addr(SB)/8, $libc_hv_vm_unmap_trampoline<>(SB)

TEXT libc_hv_vm_protect_trampoline<>(SB),NOSPLIT,$0-0
	JMP	libc_hv_vm_protect(SB)
GLOBL	·libc_hv_vm_protect_trampoline_addr(SB), RODATA, $8
DATA	·libc_hv_vm_protect_trampoline_addr(SB)/8, $libc_hv_vm_protect_trampoline<>(SB)

TEXT libc_hv_vcpu_create_trampoline<>(SB),NOSPLIT,$0-0
	JMP	libc_hv_vcpu_create(SB)
GLOBL	·libc_hv_vcpu_create_trampoline_addr(SB), RODATA, $8
DATA	·libc_hv_vcpu_create_trampoline_addr(SB)/8, $libc_hv_vcpu_create_trampoline<>(SB)

TEXT libc_hv_vcpu_destroy_trampoline<>(SB),NOSPLIT,$0-0
	JMP	libc_hv_vcpu_destroy(SB)
GLOBL	·libc_hv_vcpu_destroy_trampoline_addr(SB), RODATA, $8
DATA	·libc_hv_vcpu_destroy_trampoline_addr(SB)/8, $libc_hv_vcpu_destroy_trampoline<>(SB)

TEXT libc_hv_vcpu_run_trampoline<>(SB),NOSPLIT,$0-0
	JMP	libc_hv_vcpu_run(SB)
GLOBL	·libc_hv_vcpu_run_trampoline_addr(SB), RODATA, $8
DATA	·libc_hv_vcpu_run_trampoline_addr(SB)/8, $libc_hv_vcpu_run_trampoline<>(SB)

TEXT libc_hv_vcpus_exit_trampoline<>(SB),NOSPLIT,$0-0
	JMP	libc_hv_vcpus_exit(SB)
GLOBL	·libc_hv_vcpus_exit_trampoline_addr(SB), RODATA, $8
DATA	·libc_hv_vcpus_exit_trampoline_addr(SB)/8, $libc_hv_vcpus_exit_trampoline<>(SB)

TEXT libc_hv_vcpu_get_reg_trampoline<>(SB),NOSPLIT,$0-0
	JMP	libc_hv_vcpu_get_reg(SB)
GLOBL	·libc_hv_vcpu_get_reg_trampoline_addr(SB), RODATA, $8
DATA	·libc_hv_vcpu_get_reg_trampoline_addr(SB)/8, $libc_hv_vcpu_get_reg_trampoline<>(SB)

TEXT libc_hv_vcpu_set_reg_trampoline<>(SB),NOSPLIT,$0-0
	JMP	libc_hv_vcpu_set_reg(SB)
GLOBL	·libc_hv_vcpu_set_reg_trampoline_addr(SB), RODATA, $8
DATA	·libc_hv_vcpu_set_reg_trampoline_addr(SB)/8, $libc_hv_vcpu_set_reg_trampoline<>(SB)

TEXT libc_hv_vcpu_get_sys_reg_trampoline<>(SB),NOSPLIT,$0-0
	JMP	libc_hv_vcpu_get_sys_reg(SB)
GLOBL	·libc_hv_vcpu_get_sys_reg_trampoline_addr(SB), RODATA, $8
DATA	·libc_hv_vcpu_get_sys_reg_trampoline_addr(SB)/8, $libc_hv_vcpu_get_sys_reg_trampoline<>(SB)

TEXT libc_hv_vcpu_set_sys_reg_trampoline<>(SB),NOSPLIT,$0-0
	JMP	libc_hv_vcpu_set_sys_reg(SB)
GLOBL	·libc_hv_vcpu_set_sys_reg_trampoline_addr(SB), RODATA, $8
DATA	·libc_hv_vcpu_set_sys_reg_trampoline_addr(SB)/8, $libc_hv_vcpu_set_sys_reg_trampoline<>(SB)

TEXT libc_hv_vcpu_get_simd_fp_reg_trampoline<>(SB),NOSPLIT,$0-0
	JMP	libc_hv_vcpu_get_simd_fp_reg(SB)
GLOBL	·libc_hv_vcpu_get_simd_fp_reg_trampoline_addr(SB), RODATA, $8
DATA	·libc_hv_vcpu_get_simd_fp_reg_trampoline_addr(SB)/8, $libc_hv_vcpu_get_simd_fp_reg_trampoline<>(SB)

TEXT libc_hv_vcpu_set_simd_fp_reg_trampoline<>(SB),NOSPLIT,$0-0
	JMP	libc_hv_vcpu_set_simd_fp_reg(SB)
GLOBL	·libc_hv_vcpu_set_simd_fp_reg_trampoline_addr(SB), RODATA, $8
DATA	·libc_hv_vcpu_set_simd_fp_reg_trampoline_addr(SB)/8, $libc_hv_vcpu_set_simd_fp_reg_trampoline<>(SB)

TEXT libc_hv_vcpu_set_vtimer_mask_trampoline<>(SB),NOSPLIT,$0-0
	JMP	libc_hv_vcpu_set_vtimer_mask(SB)
GLOBL	·libc_hv_vcpu_set_vtimer_mask_trampoline_addr(SB), RODATA, $8
DATA	·libc_hv_vcpu_set_vtimer_mask_trampoline_addr(SB)/8, $libc_hv_vcpu_set_vtimer_mask_trampoline<>(SB)

TEXT libc_hv_vcpu_set_trap_debug_exceptions_trampoline<>(SB),NOSPLIT,$0-0
	JMP	libc_hv_vcpu_set_trap_debug_exceptions(SB)
GLOBL	·libc_hv_vcpu_set_trap_debug_exceptions_trampoline_addr(SB), RODATA, $8
DATA	·libc_hv_vcpu_set_trap_debug_exceptions_trampoline_addr(SB)/8, $libc_hv_vcpu_set_trap_debug_exceptions_trampoline<>(SB)

// setSIMDFPRegShim loads Q0 from the address in R2, then calls
// hv_vcpu_set_simd_fp_reg(R0, R1, Q0).
TEXT setSIMDFPRegShim<>(SB),NOSPLIT,$0-0
	FMOVQ	(R2), F0
	JMP	libc_hv_vcpu_set_simd_fp_reg(SB)
GLOBL	·setSIMDFPRegShimAddr(SB), RODATA, $8
DATA	·setSIMDFPRegShimAddr(SB)/8, $setSIMDFPRegShim<>(SB)
