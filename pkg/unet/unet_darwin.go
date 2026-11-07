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

package unet

import (
	"syscall"

	"golang.org/x/sys/unix"
)

// msgNoSignal is 0: darwin has no MSG_NOSIGNAL. Sockets get SO_NOSIGPIPE
// instead.
const msgNoSignal = 0

// newSocket creates an AF_UNIX socket. darwin has no SOCK_CLOEXEC, and no
// AF_UNIX SOCK_SEQPACKET, so packet sockets fail with EPROTONOSUPPORT.
func newSocket(typ int) (int, error) {
	syscall.ForkLock.RLock()
	defer syscall.ForkLock.RUnlock()
	fd, err := unix.Socket(unix.AF_UNIX, typ, 0)
	if err != nil {
		return -1, err
	}
	if err := setupSocket(fd); err != nil {
		unix.Close(fd)
		return -1, err
	}
	return fd, nil
}

// newSocketPair is like newSocket, for a connected pair.
func newSocketPair(typ int) ([2]int, error) {
	syscall.ForkLock.RLock()
	defer syscall.ForkLock.RUnlock()
	fds, err := unix.Socketpair(unix.AF_UNIX, typ, 0)
	if err != nil {
		return fds, err
	}
	for _, fd := range fds {
		if err := setupSocket(fd); err != nil {
			unix.Close(fds[0])
			unix.Close(fds[1])
			return [2]int{-1, -1}, err
		}
	}
	return fds, nil
}

// setupSocket sets FD_CLOEXEC and SO_NOSIGPIPE. Callers hold
// syscall.ForkLock.
func setupSocket(fd int) error {
	unix.CloseOnExec(fd)
	return unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_NOSIGPIPE, 1)
}

// pollNoTimeout calls poll(2) on events with no timeout.
func pollNoTimeout(events []unix.PollFd) unix.Errno {
	if _, err := unix.Poll(events, -1); err != nil {
		return err.(unix.Errno)
	}
	return 0
}
