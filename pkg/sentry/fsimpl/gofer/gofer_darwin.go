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
	if err := unix.Dup2(oldFD, newFD); err != nil {
		return err
	}
	unix.CloseOnExec(newFD)
	return nil
}

// fdatasync is fsync(2): darwin has no fdatasync(2).
func fdatasync(fd int) error {
	return unix.Fsync(fd)
}

// hostFileSize returns the size of the file of the host FD fd.
func hostFileSize(fd int) (uint64, error) {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return 0, err
	}
	return uint64(stat.Size), nil
}

// directfsSupported is false: directfs uses Linux host file system calls
// (O_PATH, statx, AT_EMPTY_PATH) in the Sentry. The gofer client uses lisafs
// for all operations.
const directfsSupported = false
