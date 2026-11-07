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

package fs

import (
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// syscall_syscall calls the libSystem function at fn as a blocking system
// call, so the Go scheduler can run other goroutines on the P.
//
//go:linkname syscall_syscall syscall.syscall
func syscall_syscall(fn, a1, a2, a3 uintptr) (r1, r2 uintptr, err syscall.Errno)

var libc_freadlink_trampoline_addr uintptr

//go:cgo_import_dynamic libc_freadlink freadlink "/usr/lib/libSystem.B.dylib"

var libc_futimens_trampoline_addr uintptr

//go:cgo_import_dynamic libc_futimens futimens "/usr/lib/libSystem.B.dylib"

// freadlink reads the target of the symlink of fd into buf.
func freadlink(fd int, buf []byte) (int, error) {
	var p unsafe.Pointer
	if len(buf) > 0 {
		p = unsafe.Pointer(&buf[0])
	}
	n, _, errno := syscall_syscall(libc_freadlink_trampoline_addr, uintptr(fd), uintptr(p), uintptr(len(buf)))
	if errno != 0 {
		return 0, errno
	}
	return int(n), nil
}

// futimens sets the access and modification times of the file of fd.
func futimens(fd int, times *[2]unix.Timespec) error {
	_, _, errno := syscall_syscall(libc_futimens_trampoline_addr, uintptr(fd), uintptr(unsafe.Pointer(times)), 0)
	if errno != 0 {
		return errno
	}
	return nil
}
