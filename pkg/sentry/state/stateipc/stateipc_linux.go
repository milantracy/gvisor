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

package stateipc

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// waitForHangup waits until stopFD is readable or the peer of the socket
// sockFD disconnects. It returns true if the peer disconnected, and false if
// stopFD is readable.
func waitForHangup(stopFD, sockFD int) bool {
	events := []unix.PollFd{
		{
			Fd:     int32(stopFD),
			Events: unix.POLLIN,
		},
		{
			Fd:     int32(sockFD),
			Events: unix.POLLHUP | unix.POLLRDHUP,
		},
	}
	for {
		_, err := unix.Ppoll(events, nil, nil)
		if err != nil {
			if err == unix.EINTR {
				continue
			}
			panic(fmt.Sprintf("stateipc.AsyncFileClient: watchdog got ppoll error: %v", err))
		}
		if events[0].Revents != 0 {
			return false
		}
		if events[1].Revents != 0 {
			return true
		}
	}
}

// madviseHugepage advises the host kernel to back b with huge pages.
func madviseHugepage(b []byte) error {
	return unix.Madvise(b, unix.MADV_HUGEPAGE)
}

// madviseNoHugepage advises the host kernel not to back b with huge pages.
func madviseNoHugepage(b []byte) error {
	return unix.Madvise(b, unix.MADV_NOHUGEPAGE)
}
