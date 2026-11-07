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

package pgalloc

import (
	"unsafe"

	"golang.org/x/sys/unix"
	"gvisor.dev/gvisor/pkg/sentry/memmap"
)

// fpunchhole is fpunchhole_t from <sys/fcntl.h>.
type fpunchhole struct {
	flags    uint32
	reserved uint32
	offset   int64
	length   int64
}

// decommitFile releases the file blocks of fr with F_PUNCHHOLE. Later reads
// of fr return zeros, also through existing mappings.
func (f *MemoryFile) decommitFile(fr memmap.FileRange) error {
	arg := fpunchhole{
		offset: int64(fr.Start),
		length: int64(fr.Length()),
	}
	if _, _, errno := unix.Syscall(unix.SYS_FCNTL, f.file.Fd(), unix.F_PUNCHHOLE, uintptr(unsafe.Pointer(&arg))); errno != 0 {
		return errno
	}
	return nil
}
