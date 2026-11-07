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

package seatbelt

import (
	"errors"
	"fmt"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

//go:linkname syscall_rawSyscall syscall.rawSyscall
func syscall_rawSyscall(fn, a1, a2, a3 uintptr) (r1, r2 uintptr, err syscall.Errno)

var libc_sandbox_init_trampoline_addr uintptr

//go:cgo_import_dynamic libc_sandbox_init sandbox_init "/usr/lib/libSystem.B.dylib"

var libc_sandbox_free_error_trampoline_addr uintptr

//go:cgo_import_dynamic libc_sandbox_free_error sandbox_free_error "/usr/lib/libSystem.B.dylib"

var libc_fcntl_trampoline_addr uintptr

//go:cgo_import_dynamic libc_fcntl fcntl "/usr/lib/libSystem.B.dylib"

// FDPath returns the path of the file of fd (fcntl(F_GETPATH)). Pipes and
// sockets have no path.
//
// A profile can allow operations on files that the process already has open
// by their paths. Seatbelt checks some operations on open FDs, such as fstat
// of an FD that is not open for reading.
func FDPath(fd int) (string, error) {
	var buf [unix.PathMax]byte
	r, _, errno := syscall_rawSyscall(libc_fcntl_trampoline_addr, uintptr(fd), unix.F_GETPATH, uintptr(unsafe.Pointer(&buf[0])))
	if int32(r) == -1 {
		return "", errno
	}
	return unix.ByteSliceToString(buf[:]), nil
}

// Init confines the calling process with profile, an SBPL profile. It cannot
// be undone.
func Init(profile string) error {
	p, err := unix.BytePtrFromString(profile)
	if err != nil {
		return err
	}
	var errBuf *byte
	r, _, _ := syscall_rawSyscall(libc_sandbox_init_trampoline_addr, uintptr(unsafe.Pointer(p)), 0 /* flags: profile is SBPL text */, uintptr(unsafe.Pointer(&errBuf)))
	if int32(r) == 0 {
		return nil
	}
	if errBuf == nil {
		return errors.New("sandbox_init failed")
	}
	msg := unix.BytePtrToString(errBuf)
	syscall_rawSyscall(libc_sandbox_free_error_trampoline_addr, uintptr(unsafe.Pointer(errBuf)), 0, 0)
	return fmt.Errorf("sandbox_init: %s", msg)
}
