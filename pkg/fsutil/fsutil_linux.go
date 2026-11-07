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

package fsutil

import "golang.org/x/sys/unix"

// ForEachDirent retrieves all dirents from dirfd using getdents64(2) and
// invokes handleDirent on them.
func ForEachDirent(dirfd int, handleDirent DirentHandler) error {
	if _, err := unix.Seek(dirfd, unix.SEEK_SET, 0); err != nil {
		return err
	}
	var direntsBuf [8192]byte
	for {
		n, err := unix.Getdents(dirfd, direntsBuf[:])
		if err != nil {
			return err
		}
		if n <= 0 {
			return nil
		}
		ParseDirents(direntsBuf[:n], handleDirent)
	}
}

// RenameAt is a convenience wrapper to make the renameat syscall.
//
// RenameAt is equivalent to RenameAt2 with a zero flags argument.
func RenameAt(oldDirFD int, oldName string, newDirFD int, newName string) error {
	return RenameAt2(oldDirFD, oldName, newDirFD, newName, 0)
}
