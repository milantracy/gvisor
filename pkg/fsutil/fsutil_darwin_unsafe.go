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

package fsutil

import (
	"unsafe"

	"golang.org/x/sys/unix"
)

// UnixDirentMaxSize is the maximum size of unix.Dirent in bytes.
var UnixDirentMaxSize = int(unsafe.Sizeof(unix.Dirent{}))

// ForEachDirent retrieves all dirents from dirfd using getdirentries(2) and
// invokes handleDirent on them.
func ForEachDirent(dirfd int, handleDirent DirentHandler) error {
	if _, err := unix.Seek(dirfd, 0, unix.SEEK_SET); err != nil {
		return err
	}
	var direntsBuf [8192]byte
	var base uintptr
	for {
		n, err := unix.Getdirentries(dirfd, direntsBuf[:], &base)
		if err != nil {
			return err
		}
		if n <= 0 {
			return nil
		}
		ParseDirents(direntsBuf[:n], handleDirent)
	}
}

// ParseDirents parses dirents from buf. buf must have been populated by
// getdirentries(2). It calls the handleDirent callback for each dirent, with
// the dirent's seek offset as off.
func ParseDirents(buf []byte, handleDirent DirentHandler) {
	for len(buf) > 0 {
		dirent := (*unix.Dirent)(unsafe.Pointer(&buf[0]))
		reclen := dirent.Reclen
		nameStart := unsafe.Offsetof(dirent.Name)
		name := string(buf[nameStart : nameStart+uintptr(dirent.Namlen)])
		ino, off, ftype := dirent.Ino, int64(dirent.Seekoff), dirent.Type

		// Advance buf for the next dirent.
		buf = buf[reclen:]

		// Skip `.` and `..` entries, as on Linux.
		if name == "." || name == ".." {
			continue
		}
		handleDirent(ino, off, ftype, name, reclen)
	}
}
