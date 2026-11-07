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
	"golang.org/x/sys/unix"
	"gvisor.dev/gvisor/pkg/abi/linux"
)

// RenameAt is a convenience wrapper to make the renameat syscall.
func RenameAt(oldDirFD int, oldName string, newDirFD int, newName string) error {
	return unix.Renameat(oldDirFD, oldName, newDirFD, newName)
}

// RenameAt2 is renameat2(2) with Linux flags, made with renameatx_np(2).
// RENAME_NOREPLACE becomes RENAME_EXCL and RENAME_EXCHANGE becomes
// RENAME_SWAP. darwin has no RENAME_WHITEOUT.
func RenameAt2(oldDirFD int, oldName string, newDirFD int, newName string, flags uint32) error {
	var hostFlags uint32
	switch flags {
	case 0:
	case linux.RENAME_NOREPLACE:
		hostFlags = unix.RENAME_EXCL
	case linux.RENAME_EXCHANGE:
		hostFlags = unix.RENAME_SWAP
	default:
		return unix.EINVAL
	}
	return unix.RenameatxNp(oldDirFD, oldName, newDirFD, newName, hostFlags)
}

// Fallocate is fallocate(2) with mode 0: it reserves space up to
// offset+length with F_PREALLOCATE and extends the file to offset+length.
// Other modes are not supported.
func Fallocate(fd int, mode, offset, length uint64) error {
	if mode != 0 {
		return unix.EOPNOTSUPP
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return err
	}
	end := int64(offset + length)
	if end <= stat.Size {
		return nil
	}
	store := unix.Fstore_t{
		Flags:   unix.F_ALLOCATEALL,
		Posmode: unix.F_PEOFPOSMODE,
		Length:  end - stat.Size,
	}
	if err := unix.FcntlFstore(uintptr(fd), unix.F_PREALLOCATE, &store); err != nil {
		return err
	}
	return unix.Ftruncate(fd, end)
}
