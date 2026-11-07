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

package fdnotifier

import (
	"golang.org/x/sys/unix"
	"gvisor.dev/gvisor/pkg/waiter"
)

// pollBits are the poll(2) bits that have the same values on darwin and
// Linux. darwin has no POLLRDHUP, and its POLLRDNORM and POLLWRNORM are
// covered by POLLIN and POLLOUT.
const pollBits = unix.POLLIN | unix.POLLPRI | unix.POLLOUT | unix.POLLERR | unix.POLLHUP | unix.POLLNVAL

// NonBlockingPoll polls the given FD in non-blocking fashion. It is used just
// to query the FD's current state.
func NonBlockingPoll(fd int32, mask waiter.EventMask) waiter.EventMask {
	events := int16(mask.ToLinux()) & pollBits
	if mask&waiter.EventRdNorm != 0 {
		events |= unix.POLLIN
	}
	if mask&waiter.EventWrNorm != 0 {
		events |= unix.POLLOUT
	}
	fds := []unix.PollFd{{Fd: fd, Events: events}}
	for {
		n, err := unix.Poll(fds, 0)
		// Interrupted by a signal, try again.
		if err == unix.EINTR {
			continue
		}

		// If an error occur we'll conservatively say the FD is ready for
		// whatever is being checked.
		if err != nil {
			return mask
		}

		// If no FDs were returned, it wasn't ready for anything.
		if n == 0 {
			return 0
		}

		return waiter.EventMaskFromLinux(uint32(fds[0].Revents & pollBits))
	}
}
