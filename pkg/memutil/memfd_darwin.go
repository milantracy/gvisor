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

package memutil

import (
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"

	"golang.org/x/sys/unix"
	"gvisor.dev/gvisor/pkg/abi/linux"
)

var memFDCount atomic.Uint64

// CreateMemFD returns a file descriptor for an unlinked temporary file in
// os.TempDir(), which stands in for memfd_create(2) on darwin. flags are Linux
// MFD_* flags. MFD_CLOEXEC sets O_CLOEXEC. MFD_ALLOW_SEALING is accepted, but
// darwin has no file seals, so later F_ADD_SEALS calls fail. Other flags give
// EINVAL.
//
// A regular file rather than shm_open(2): on APFS, fcntl(F_PUNCHHOLE)
// releases its pages, also while they are mapped into a Hypervisor.framework
// guest, and ftruncate(2) can change its size more than once.
func CreateMemFD(name string, flags int) (int, error) {
	if flags&^(linux.MFD_CLOEXEC|linux.MFD_ALLOW_SEALING) != 0 {
		return -1, unix.EINVAL
	}
	oflags := unix.O_RDWR | unix.O_CREAT | unix.O_EXCL
	if flags&linux.MFD_CLOEXEC != 0 {
		oflags |= unix.O_CLOEXEC
	}
	for {
		path := filepath.Join(os.TempDir(), fmt.Sprintf("gvisor-memfd-%s-%d-%d", name, os.Getpid(), memFDCount.Add(1)))
		fd, err := unix.Open(path, oflags, 0o600)
		if err == unix.EEXIST {
			continue
		}
		if err != nil {
			return -1, err
		}
		if err := unix.Unlink(path); err != nil {
			unix.Close(fd)
			return -1, err
		}
		return fd, nil
	}
}
