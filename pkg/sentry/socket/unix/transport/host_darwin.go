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

package transport

import (
	"golang.org/x/sys/unix"
	"gvisor.dev/gvisor/pkg/syserr"
)

// msgNoSignal is 0: darwin has no MSG_NOSIGNAL. setupHostSocket sets
// SO_NOSIGPIPE instead.
const msgNoSignal = 0

// setupHostSocket returns an error if fd is not a Unix domain socket, and sets
// SO_NOSIGPIPE on it if it is connected. darwin has no SO_DOMAIN, so the family comes from
// getsockname(2).
func setupHostSocket(fd int) *syserr.Error {
	sa, err := unix.Getsockname(fd)
	if err != nil {
		return syserr.FromError(err)
	}
	if _, ok := sa.(*unix.SockaddrUnix); !ok {
		// We only allow Unix sockets.
		return syserr.ErrInvalidEndpointState
	}
	// darwin fails setsockopt(2) with EINVAL on a socket whose peer has
	// closed. Writes to such a socket fail with EPIPE, and the Go runtime
	// ignores the SIGPIPE of writes that are not by os.File to standard
	// output or standard error.
	if err := unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_NOSIGPIPE, 1); err != nil && err != unix.EINVAL {
		return syserr.FromError(err)
	}
	return nil
}

// hostPasscred returns false: darwin has no SO_PASSCRED.
func hostPasscred(fd int) (bool, error) {
	return false, nil
}

// credsControl returns no control message. darwin has no SCM_CREDENTIALS, and
// the host peer cannot have SO_PASSCRED set, so Linux also drops the
// credentials in this case.
func credsControl(fd int) ([]byte, error) {
	return nil, nil
}

// shutdownHostSocket shuts down part of the connection of fd. darwin fails
// shutdown(2) with ENOTCONN on a socket whose peer has closed, where Linux
// succeeds; both directions of such a socket are already shut down.
func shutdownHostSocket(fd, how int) error {
	if err := unix.Shutdown(fd, how); err != nil && err != unix.ENOTCONN {
		return err
	}
	return nil
}
