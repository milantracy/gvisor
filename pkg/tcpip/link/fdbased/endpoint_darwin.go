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

package fdbased

import (
	"fmt"

	"golang.org/x/sys/unix"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
)

// New returns an error: the fd-based link endpoint is not implemented on
// darwin.
func New(opts *Options) (stack.LinkEndpoint, error) {
	return nil, fmt.Errorf("fdbased: not supported on darwin")
}

// IsPacketSocket returns false: darwin has no AF_PACKET sockets.
func IsPacketSocket(fd int, isSocket bool) (bool, error) {
	return false, nil
}

// CreatePacketFanoutGroup returns an error: darwin has no AF_PACKET sockets.
func CreatePacketFanoutGroup(fd int) (int32, error) {
	return 0, fmt.Errorf("fdbased: packet fanout is not supported on darwin")
}

// JoinPacketFanoutGroup returns an error: darwin has no AF_PACKET sockets.
func JoinPacketFanoutGroup(fd int, fID int32) error {
	return fmt.Errorf("fdbased: packet fanout is not supported on darwin")
}

// IsSocketFD returns true if fd is a socket.
func IsSocketFD(fd int) (bool, error) {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return false, fmt.Errorf("unix.Fstat(%v,...) failed: %v", fd, err)
	}
	return (stat.Mode & unix.S_IFMT) == unix.S_IFSOCK, nil
}
