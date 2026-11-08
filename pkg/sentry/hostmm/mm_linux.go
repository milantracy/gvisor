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

//go:build linux

package hostmm

import "golang.org/x/sys/unix"

// RawMremap calls mremap(2) without a Go scheduler interaction, and returns
// its result and error number.
func RawMremap(oldAddr, oldSize, newSize, flags, newAddr uintptr) (uintptr, unix.Errno) {
	r, _, errno := unix.RawSyscall6(unix.SYS_MREMAP, oldAddr, oldSize, newSize, flags, newAddr, 0)
	return r, errno
}

// MadvisePopulate faults in the pages of [addr, addr+length) with
// MADV_POPULATE_WRITE if write is true, or MADV_POPULATE_READ otherwise.
func MadvisePopulate(addr, length uintptr, write bool) unix.Errno {
	advice := unix.MADV_POPULATE_READ
	if write {
		advice = unix.MADV_POPULATE_WRITE
	}
	_, _, errno := unix.Syscall(unix.SYS_MADVISE, addr, length, uintptr(advice))
	return errno
}
