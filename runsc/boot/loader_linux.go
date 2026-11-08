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

package boot

import (
	"golang.org/x/sys/unix"
	"gvisor.dev/gvisor/runsc/boot/filter"
	"gvisor.dev/gvisor/runsc/specutils"
)

// dupCloexec duplicates oldfd to newfd, with FD_CLOEXEC set on newfd.
func dupCloexec(oldfd, newfd int) error {
	return unix.Dup3(oldfd, newfd, unix.O_CLOEXEC)
}

// waitForHangup waits until the peer of the socket fd disconnects.
func waitForHangup(fd int) error {
	events := []unix.PollFd{
		{
			Fd:     int32(fd),
			Events: unix.POLLHUP | unix.POLLRDHUP,
		},
	}
	_, _, err := specutils.RetryEintr(func() (uintptr, uintptr, error) {
		// Use ppoll instead of poll because it's already allowed in seccomp.
		n, err := unix.Ppoll(events, nil, nil)
		return uintptr(n), 0, err
	})
	return err
}

// setHostFilterOptions sets the host-specific fields of opts. Linux has none.
func (l *Loader) setHostFilterOptions(opts *filter.Options) {}
