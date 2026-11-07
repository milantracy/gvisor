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
	"golang.org/x/sys/unix"
	"gvisor.dev/gvisor/pkg/sentry/memmap"
)

// madviseChunkMapping does nothing: darwin has no transparent huge pages.
func (f *MemoryFile) madviseChunkMapping(addr, len uintptr, huge bool) {}

// madvisePopulateWrite returns EINVAL, as Linux does without
// MADV_POPULATE_WRITE: darwin has no equivalent.
func madvisePopulateWrite(addr, len uintptr) unix.Errno {
	return unix.EINVAL
}

// commitZeros is the size of the buffer that commitFile writes.
const commitZeros = 1 << 20

var zeros [commitZeros]byte

// commitFile allocates file blocks for fr. darwin has no way to allocate blocks
// in a range inside the file, so commitFile writes zeros. This does not change
// the contents of fr, because pages are zero when they are allocated.
func (f *MemoryFile) commitFile(fr memmap.FileRange) error {
	fd := int(f.file.Fd())
	for off := fr.Start; off < fr.End; {
		n := min(fr.End-off, commitZeros)
		w, err := unix.Pwrite(fd, zeros[:n], int64(off))
		if err != nil {
			if err == unix.EINTR {
				continue
			}
			return err
		}
		off += uint64(w)
	}
	return nil
}
