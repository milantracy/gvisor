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

package transport

import (
	"golang.org/x/sys/unix"
	"gvisor.dev/gvisor/pkg/syserr"
)

// setupHostSocket returns an error if fd is not a Unix domain socket.
func setupHostSocket(fd int) *syserr.Error {
	family, err := unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_DOMAIN)
	if err != nil {
		return syserr.FromError(err)
	}
	if family != unix.AF_UNIX {
		// We only allow Unix sockets.
		return syserr.ErrInvalidEndpointState
	}
	return nil
}

// hostPasscred returns true if SO_PASSCRED is set on fd.
func hostPasscred(fd int) (bool, error) {
	passcred, err := unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_PASSCRED)
	return passcred != 0, err
}

// shutdownHostSocket shuts down part of the connection of fd.
func shutdownHostSocket(fd, how int) error {
	return unix.Shutdown(fd, how)
}
