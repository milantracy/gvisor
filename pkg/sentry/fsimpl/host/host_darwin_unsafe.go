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

package host

import (
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// setTimestamps calls libSystem's futimens(2).
func setTimestamps(fd int, ts *[2]unix.Timespec) error {
	if _, _, e := syscall_syscall(libc_futimens_trampoline_addr, uintptr(fd), uintptr(unsafe.Pointer(ts)), 0); e != 0 {
		return e
	}
	return nil
}

// syscall_syscall calls the libSystem function at fn as a blocking system
// call, as x/sys/unix does on darwin.
//
//go:linkname syscall_syscall syscall.syscall
func syscall_syscall(fn, a1, a2, a3 uintptr) (r1, r2 uintptr, err syscall.Errno)

var libc_futimens_trampoline_addr uintptr

//go:cgo_import_dynamic libc_futimens futimens "/usr/lib/libSystem.B.dylib"
