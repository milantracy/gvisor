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

// syscall_rawSyscall calls the libSystem function at fn without telling the Go
// scheduler.
//
//go:linkname syscall_rawSyscall syscall.rawSyscall
func syscall_rawSyscall(fn, a1, a2, a3 uintptr) (r1, r2 uintptr, err syscall.Errno)

// syscall_syscall6 calls the libSystem function at fn as a blocking system
// call, so the Go scheduler can run other goroutines on the P.
//
//go:linkname syscall_syscall6 syscall.syscall6
func syscall_syscall6(fn, a1, a2, a3, a4, a5, a6 uintptr) (r1, r2 uintptr, err syscall.Errno)

var libc_os_sync_wait_on_address_trampoline_addr uintptr

//go:cgo_import_dynamic libc_os_sync_wait_on_address os_sync_wait_on_address "/usr/lib/libSystem.B.dylib"

var libc_os_sync_wake_by_address_any_trampoline_addr uintptr

//go:cgo_import_dynamic libc_os_sync_wake_by_address_any os_sync_wake_by_address_any "/usr/lib/libSystem.B.dylib"

// waitThread blocks the calling thread while *addr is val. It does not tell
// the Go scheduler, so it is only for a thread that is locked to its
// goroutine and has nothing else to run. It can return early.
func waitThread(addr *uint32, val uint32) {
	syscall_rawSyscall(libc_os_sync_wait_on_address_trampoline_addr, uintptr(unsafe.Pointer(addr)), uintptr(val), 4)
}

// waitGoroutine blocks the calling goroutine while *addr is val, and lets the
// Go scheduler run other goroutines meanwhile. It can return early.
func waitGoroutine(addr *uint32, val uint32) {
	syscall_syscall6(libc_os_sync_wait_on_address_trampoline_addr, uintptr(unsafe.Pointer(addr)), uintptr(val), 4, 0, 0, 0)
}

// wake wakes one thread that waits on addr.
func wake(addr *uint32) {
	syscall_rawSyscall(libc_os_sync_wake_by_address_any_trampoline_addr, uintptr(unsafe.Pointer(addr)), 4, 0)
}
