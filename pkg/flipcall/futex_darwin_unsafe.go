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

//go:build darwin

package flipcall

import (
	"syscall"
	_ "unsafe" // for go:linkname
)

// syscall_rawSyscall calls the libSystem function at fn without telling the Go
// scheduler, as x/sys/unix does on darwin.
//
//go:linkname syscall_rawSyscall syscall.rawSyscall
func syscall_rawSyscall(fn, a1, a2, a3 uintptr) (r1, r2 uintptr, err syscall.Errno)

// syscall_syscall6 calls the libSystem function at fn as a blocking system
// call, as x/sys/unix does on darwin.
//
//go:linkname syscall_syscall6 syscall.syscall6
func syscall_syscall6(fn, a1, a2, a3, a4, a5, a6 uintptr) (r1, r2 uintptr, err syscall.Errno)

var libc_os_sync_wait_on_address_trampoline_addr uintptr

//go:cgo_import_dynamic libc_os_sync_wait_on_address os_sync_wait_on_address "/usr/lib/libSystem.B.dylib"

var libc_os_sync_wake_by_address_any_trampoline_addr uintptr

//go:cgo_import_dynamic libc_os_sync_wake_by_address_any os_sync_wake_by_address_any "/usr/lib/libSystem.B.dylib"

var libc_os_sync_wake_by_address_all_trampoline_addr uintptr

//go:cgo_import_dynamic libc_os_sync_wake_by_address_all os_sync_wake_by_address_all "/usr/lib/libSystem.B.dylib"

var libc_sched_yield_trampoline_addr uintptr

//go:cgo_import_dynamic libc_sched_yield sched_yield "/usr/lib/libSystem.B.dylib"
