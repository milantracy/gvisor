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

package gofer

import (
	"golang.org/x/sys/unix"
)

// dupOver makes newFD refer to the file description of oldFD, with
// FD_CLOEXEC set.
func dupOver(oldFD, newFD int) error {
	return unix.Dup3(oldFD, newFD, unix.O_CLOEXEC)
}

func fdatasync(fd int) error {
	return unix.Fdatasync(fd)
}

// hostFileSize returns the size of the file of the host FD fd.
func hostFileSize(fd int) (uint64, error) {
	// Using statx(2) with a minimal mask is faster than fstat(2).
	var stat unix.Statx_t
	if err := unix.Statx(fd, "", unix.AT_EMPTY_PATH, unix.STATX_SIZE, &stat); err != nil {
		return 0, err
	}
	return stat.Size, nil
}

// directfsSupported is true: the Sentry can make file system calls on host
// FDs that the gofer donates.
const directfsSupported = true
