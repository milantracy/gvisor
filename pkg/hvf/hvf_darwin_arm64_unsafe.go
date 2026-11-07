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
	"syscall"
	"unsafe"
)

// syscall_rawSyscall calls the function at fn without telling the Go
// scheduler, as x/sys/unix does on darwin.
//
//go:linkname syscall_rawSyscall syscall.rawSyscall
func syscall_rawSyscall(fn, a1, a2, a3 uintptr) (r1, r2 uintptr, err syscall.Errno)

// syscall_rawSyscall6 is syscall_rawSyscall with six arguments.
//
//go:linkname syscall_rawSyscall6 syscall.rawSyscall6
func syscall_rawSyscall6(fn, a1, a2, a3, a4, a5, a6 uintptr) (r1, r2 uintptr, err syscall.Errno)

// syscall_syscall calls the function at fn as a blocking system call, so the
// scheduler can run other goroutines on its P.
//
//go:linkname syscall_syscall syscall.syscall
func syscall_syscall(fn, a1, a2, a3 uintptr) (r1, r2 uintptr, err syscall.Errno)

func vmConfigCreate() uintptr {
	r, _, _ := syscall_rawSyscall(libc_hv_vm_config_create_trampoline_addr, 0, 0, 0)
	return r
}

func vmConfigSetIPASize(config uintptr, bits uint32) error {
	r, _, _ := syscall_rawSyscall(libc_hv_vm_config_set_ipa_size_trampoline_addr, config, uintptr(bits), 0)
	return result(r)
}

func maxIPASize() (uint32, error) {
	var bits uint32
	r, _, _ := syscall_rawSyscall(libc_hv_vm_config_get_max_ipa_size_trampoline_addr, uintptr(unsafe.Pointer(&bits)), 0, 0)
	return bits, result(r)
}

func vmCreate(config uintptr) error {
	r, _, _ := syscall_rawSyscall(libc_hv_vm_create_trampoline_addr, config, 0, 0)
	return result(r)
}

func vmDestroy() error {
	r, _, _ := syscall_rawSyscall(libc_hv_vm_destroy_trampoline_addr, 0, 0, 0)
	return result(r)
}

func vmMap(host uintptr, ipa, size uint64, flags MemoryFlags) error {
	r, _, _ := syscall_rawSyscall6(libc_hv_vm_map_trampoline_addr, host, uintptr(ipa), uintptr(size), uintptr(flags), 0, 0)
	return result(r)
}

func vmUnmap(ipa, size uint64) error {
	r, _, _ := syscall_rawSyscall(libc_hv_vm_unmap_trampoline_addr, uintptr(ipa), uintptr(size), 0)
	return result(r)
}

func vmProtect(ipa, size uint64, flags MemoryFlags) error {
	r, _, _ := syscall_rawSyscall(libc_hv_vm_protect_trampoline_addr, uintptr(ipa), uintptr(size), uintptr(flags))
	return result(r)
}

func vcpuCreate(v *VCPU) error {
	r, _, _ := syscall_rawSyscall(libc_hv_vcpu_create_trampoline_addr, uintptr(unsafe.Pointer(&v.id)), uintptr(unsafe.Pointer(&v.exit)), 0)
	return result(r)
}

func vcpuDestroy(id uint64) error {
	r, _, _ := syscall_rawSyscall(libc_hv_vcpu_destroy_trampoline_addr, uintptr(id), 0, 0)
	return result(r)
}

func vcpuRun(id uint64) error {
	r, _, _ := syscall_syscall(libc_hv_vcpu_run_trampoline_addr, uintptr(id), 0, 0)
	return result(r)
}

func vcpusExit(ids []uint64) error {
	r, _, _ := syscall_rawSyscall(libc_hv_vcpus_exit_trampoline_addr, uintptr(unsafe.Pointer(&ids[0])), uintptr(len(ids)), 0)
	return result(r)
}

func vcpuGetReg(id uint64, reg Reg) (uint64, error) {
	var val uint64
	r, _, _ := syscall_rawSyscall(libc_hv_vcpu_get_reg_trampoline_addr, uintptr(id), uintptr(reg), uintptr(unsafe.Pointer(&val)))
	return val, result(r)
}

func vcpuSetReg(id uint64, reg Reg, val uint64) error {
	r, _, _ := syscall_rawSyscall(libc_hv_vcpu_set_reg_trampoline_addr, uintptr(id), uintptr(reg), uintptr(val))
	return result(r)
}

func vcpuGetSysReg(id uint64, reg SysReg) (uint64, error) {
	var val uint64
	r, _, _ := syscall_rawSyscall(libc_hv_vcpu_get_sys_reg_trampoline_addr, uintptr(id), uintptr(reg), uintptr(unsafe.Pointer(&val)))
	return val, result(r)
}

func vcpuSetSysReg(id uint64, reg SysReg, val uint64) error {
	r, _, _ := syscall_rawSyscall(libc_hv_vcpu_set_sys_reg_trampoline_addr, uintptr(id), uintptr(reg), uintptr(val))
	return result(r)
}

func vcpuGetSIMDFPReg(id uint64, n int) ([16]byte, error) {
	var val [16]byte
	r, _, _ := syscall_rawSyscall(libc_hv_vcpu_get_simd_fp_reg_trampoline_addr, uintptr(id), uintptr(n), uintptr(unsafe.Pointer(&val)))
	return val, result(r)
}

// vcpuSetSIMDFPReg calls hv_vcpu_set_simd_fp_reg through a shim. The C
// function takes the value in Q0, and the shim loads Q0 from the pointer in
// the third argument.
func vcpuSetSIMDFPReg(id uint64, n int, val *[16]byte) error {
	r, _, _ := syscall_rawSyscall(setSIMDFPRegShimAddr, uintptr(id), uintptr(n), uintptr(unsafe.Pointer(val)))
	return result(r)
}

func vcpuSetVTimerMask(id uint64, masked bool) error {
	var b uintptr
	if masked {
		b = 1
	}
	r, _, _ := syscall_rawSyscall(libc_hv_vcpu_set_vtimer_mask_trampoline_addr, uintptr(id), b, 0)
	return result(r)
}

func vcpuSetTrapDebugExceptions(id uint64, trap bool) error {
	var b uintptr
	if trap {
		b = 1
	}
	r, _, _ := syscall_rawSyscall(libc_hv_vcpu_set_trap_debug_exceptions_trampoline_addr, uintptr(id), b, 0)
	return result(r)
}

var setSIMDFPRegShimAddr uintptr

var libc_hv_vm_config_create_trampoline_addr uintptr

//go:cgo_import_dynamic libc_hv_vm_config_create hv_vm_config_create "/System/Library/Frameworks/Hypervisor.framework/Versions/A/Hypervisor"

var libc_hv_vm_config_set_ipa_size_trampoline_addr uintptr

//go:cgo_import_dynamic libc_hv_vm_config_set_ipa_size hv_vm_config_set_ipa_size "/System/Library/Frameworks/Hypervisor.framework/Versions/A/Hypervisor"

var libc_hv_vm_config_get_max_ipa_size_trampoline_addr uintptr

//go:cgo_import_dynamic libc_hv_vm_config_get_max_ipa_size hv_vm_config_get_max_ipa_size "/System/Library/Frameworks/Hypervisor.framework/Versions/A/Hypervisor"

var libc_hv_vm_create_trampoline_addr uintptr

//go:cgo_import_dynamic libc_hv_vm_create hv_vm_create "/System/Library/Frameworks/Hypervisor.framework/Versions/A/Hypervisor"

var libc_hv_vm_destroy_trampoline_addr uintptr

//go:cgo_import_dynamic libc_hv_vm_destroy hv_vm_destroy "/System/Library/Frameworks/Hypervisor.framework/Versions/A/Hypervisor"

var libc_hv_vm_map_trampoline_addr uintptr

//go:cgo_import_dynamic libc_hv_vm_map hv_vm_map "/System/Library/Frameworks/Hypervisor.framework/Versions/A/Hypervisor"

var libc_hv_vm_unmap_trampoline_addr uintptr

//go:cgo_import_dynamic libc_hv_vm_unmap hv_vm_unmap "/System/Library/Frameworks/Hypervisor.framework/Versions/A/Hypervisor"

var libc_hv_vm_protect_trampoline_addr uintptr

//go:cgo_import_dynamic libc_hv_vm_protect hv_vm_protect "/System/Library/Frameworks/Hypervisor.framework/Versions/A/Hypervisor"

var libc_hv_vcpu_create_trampoline_addr uintptr

//go:cgo_import_dynamic libc_hv_vcpu_create hv_vcpu_create "/System/Library/Frameworks/Hypervisor.framework/Versions/A/Hypervisor"

var libc_hv_vcpu_destroy_trampoline_addr uintptr

//go:cgo_import_dynamic libc_hv_vcpu_destroy hv_vcpu_destroy "/System/Library/Frameworks/Hypervisor.framework/Versions/A/Hypervisor"

var libc_hv_vcpu_run_trampoline_addr uintptr

//go:cgo_import_dynamic libc_hv_vcpu_run hv_vcpu_run "/System/Library/Frameworks/Hypervisor.framework/Versions/A/Hypervisor"

var libc_hv_vcpus_exit_trampoline_addr uintptr

//go:cgo_import_dynamic libc_hv_vcpus_exit hv_vcpus_exit "/System/Library/Frameworks/Hypervisor.framework/Versions/A/Hypervisor"

var libc_hv_vcpu_get_reg_trampoline_addr uintptr

//go:cgo_import_dynamic libc_hv_vcpu_get_reg hv_vcpu_get_reg "/System/Library/Frameworks/Hypervisor.framework/Versions/A/Hypervisor"

var libc_hv_vcpu_set_reg_trampoline_addr uintptr

//go:cgo_import_dynamic libc_hv_vcpu_set_reg hv_vcpu_set_reg "/System/Library/Frameworks/Hypervisor.framework/Versions/A/Hypervisor"

var libc_hv_vcpu_get_sys_reg_trampoline_addr uintptr

//go:cgo_import_dynamic libc_hv_vcpu_get_sys_reg hv_vcpu_get_sys_reg "/System/Library/Frameworks/Hypervisor.framework/Versions/A/Hypervisor"

var libc_hv_vcpu_set_sys_reg_trampoline_addr uintptr

//go:cgo_import_dynamic libc_hv_vcpu_set_sys_reg hv_vcpu_set_sys_reg "/System/Library/Frameworks/Hypervisor.framework/Versions/A/Hypervisor"

var libc_hv_vcpu_get_simd_fp_reg_trampoline_addr uintptr

//go:cgo_import_dynamic libc_hv_vcpu_get_simd_fp_reg hv_vcpu_get_simd_fp_reg "/System/Library/Frameworks/Hypervisor.framework/Versions/A/Hypervisor"

var libc_hv_vcpu_set_simd_fp_reg_trampoline_addr uintptr

//go:cgo_import_dynamic libc_hv_vcpu_set_simd_fp_reg hv_vcpu_set_simd_fp_reg "/System/Library/Frameworks/Hypervisor.framework/Versions/A/Hypervisor"

var libc_hv_vcpu_set_vtimer_mask_trampoline_addr uintptr

//go:cgo_import_dynamic libc_hv_vcpu_set_vtimer_mask hv_vcpu_set_vtimer_mask "/System/Library/Frameworks/Hypervisor.framework/Versions/A/Hypervisor"

var libc_hv_vcpu_set_trap_debug_exceptions_trampoline_addr uintptr

//go:cgo_import_dynamic libc_hv_vcpu_set_trap_debug_exceptions hv_vcpu_set_trap_debug_exceptions "/System/Library/Frameworks/Hypervisor.framework/Versions/A/Hypervisor"
