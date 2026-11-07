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

package eventfd

import (
	"fmt"
	"io"

	"golang.org/x/sys/unix"
	"gvisor.dev/gvisor/pkg/hostarch"
	"gvisor.dev/gvisor/pkg/rawfile"
)

// Eventfd represents a Linux eventfd object.
type Eventfd struct {
	fd       int
	mmioAddr uintptr
	mmioCtrl MMIOController
}

// Create returns an initialized eventfd.
func Create() (Eventfd, error) {
	fd, _, err := unix.RawSyscall(unix.SYS_EVENTFD2, 0, 0, 0)
	if err != 0 {
		return Eventfd{}, fmt.Errorf("failed to create eventfd: %v", error(err))
	}
	if err := unix.SetNonblock(int(fd), true); err != nil {
		unix.Close(int(fd))
		return Eventfd{}, err
	}
	return Eventfd{fd: int(fd)}, nil
}

// Wrap returns an initialized Eventfd using the provided fd.
func Wrap(fd int) Eventfd {
	return Eventfd{fd: fd}
}

// Close closes the eventfd, after which it should not be used.
func (ev Eventfd) Close() error {
	if ev.mmioCtrl != nil {
		ev.mmioCtrl.Close(ev)
	}
	return unix.Close(ev.fd)
}

// Dup copies the eventfd, calling dup(2) on the underlying file descriptor.
func (ev Eventfd) Dup() (Eventfd, error) {
	other, err := unix.Dup(ev.fd)
	if err != nil {
		return Eventfd{}, fmt.Errorf("failed to dup: %v", other)
	}
	return Eventfd{fd: other}, nil
}

// writeValue writes buf, an encoded uint64, with write(2).
func (ev Eventfd) writeValue(buf []byte) error {
	for {
		n, err := nonBlockingWrite(ev.fd, buf)
		if err == unix.EINTR {
			continue
		}
		if err != nil || n != sizeofUint64 {
			panic(fmt.Sprintf("bad write to eventfd: got %d bytes, wanted %d with error %v", n, sizeofUint64, err))
		}
		return err
	}
}

// Read blocks until eventfd is non-zero (i.e. someone calls Notify or Write)
// and returns the value read.
func (ev Eventfd) Read() (uint64, error) {
	var tmp [sizeofUint64]byte
	n, errno := rawfile.BlockingRead(ev.fd, tmp[:])
	if errno != 0 {
		return 0, errno
	}
	if n == 0 {
		return 0, io.EOF
	}
	if n != sizeofUint64 {
		panic(fmt.Sprintf("short read from eventfd: got %d bytes, wanted %d", n, sizeofUint64))
	}
	return hostarch.ByteOrder.Uint64(tmp[:]), nil
}
