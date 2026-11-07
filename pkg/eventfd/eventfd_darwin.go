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

package eventfd

import (
	"fmt"
	"io"
	"syscall"

	"golang.org/x/sys/unix"
	"gvisor.dev/gvisor/pkg/hostarch"
)

// Eventfd emulates a Linux eventfd object with a non-blocking pipe, because
// darwin has no eventfd. fd, the read end, becomes readable when the counter
// is non-zero. Each Write appends its value as 8 bytes to the pipe, and Read
// drains the pipe and returns the sum, so the counter semantics match eventfd
// without EFD_SEMAPHORE.
type Eventfd struct {
	fd       int
	wfd      int
	mmioAddr uintptr
	mmioCtrl MMIOController
}

// Create returns an initialized eventfd.
func Create() (Eventfd, error) {
	var p [2]int
	// darwin has no pipe2. syscall.ForkLock keeps a concurrent fork from
	// inheriting the pipe before FD_CLOEXEC is set.
	syscall.ForkLock.RLock()
	err := unix.Pipe(p[:])
	if err == nil {
		unix.CloseOnExec(p[0])
		unix.CloseOnExec(p[1])
	}
	syscall.ForkLock.RUnlock()
	if err != nil {
		return Eventfd{}, fmt.Errorf("failed to create eventfd pipe: %v", err)
	}
	for _, fd := range p {
		if err := unix.SetNonblock(fd, true); err != nil {
			unix.Close(p[0])
			unix.Close(p[1])
			return Eventfd{}, err
		}
	}
	return Eventfd{fd: p[0], wfd: p[1]}, nil
}

// Wrap returns an Eventfd that reads from and writes to fd.
func Wrap(fd int) Eventfd {
	return Eventfd{fd: fd, wfd: fd}
}

// Close closes the eventfd, after which it should not be used.
func (ev Eventfd) Close() error {
	if ev.mmioCtrl != nil {
		ev.mmioCtrl.Close(ev)
	}
	err := unix.Close(ev.fd)
	if ev.wfd != ev.fd {
		if werr := unix.Close(ev.wfd); err == nil {
			err = werr
		}
	}
	return err
}

// Dup copies the eventfd, calling dup(2) on the underlying file descriptors.
func (ev Eventfd) Dup() (Eventfd, error) {
	fd, err := unix.Dup(ev.fd)
	if err != nil {
		return Eventfd{}, fmt.Errorf("failed to dup: %v", err)
	}
	if ev.wfd == ev.fd {
		return Eventfd{fd: fd, wfd: fd}, nil
	}
	wfd, err := unix.Dup(ev.wfd)
	if err != nil {
		unix.Close(fd)
		return Eventfd{}, fmt.Errorf("failed to dup: %v", err)
	}
	return Eventfd{fd: fd, wfd: wfd}, nil
}

// writeValue appends buf, an encoded uint64, to the pipe. If the pipe is
// full, a reader has a wakeup pending already, so the value is dropped; Read
// then returns less than the true sum, which only matters for callers that use
// the value rather than the wakeup.
func (ev Eventfd) writeValue(buf []byte) error {
	for {
		n, err := unix.Write(ev.wfd, buf)
		if err == unix.EINTR {
			continue
		}
		if err == unix.EAGAIN {
			return nil
		}
		if err != nil || n != sizeofUint64 {
			panic(fmt.Sprintf("bad write to eventfd: got %d bytes, wanted %d with error %v", n, sizeofUint64, err))
		}
		return nil
	}
}

// Read blocks until eventfd is non-zero (i.e. someone calls Notify or Write)
// and returns the value read.
func (ev Eventfd) Read() (uint64, error) {
	// Writes of 8 bytes or less to a pipe are atomic, so the pipe holds
	// whole values only.
	var buf [64 * sizeofUint64]byte
	var sum uint64
	for {
		n, err := unix.Read(ev.fd, buf[:])
		switch {
		case err == unix.EINTR:
			continue
		case err == unix.EAGAIN && sum != 0:
			return sum, nil
		case err == unix.EAGAIN:
			fds := []unix.PollFd{{Fd: int32(ev.fd), Events: unix.POLLIN}}
			if _, err := unix.Poll(fds, -1); err != nil && err != unix.EINTR {
				return 0, err
			}
			continue
		case err != nil:
			return 0, err
		case n == 0 && sum == 0:
			return 0, io.EOF
		case n == 0:
			return sum, nil
		case n%sizeofUint64 != 0:
			panic(fmt.Sprintf("short read from eventfd: got %d bytes", n))
		}
		for i := 0; i < n; i += sizeofUint64 {
			sum += hostarch.ByteOrder.Uint64(buf[i : i+sizeofUint64])
		}
	}
}
