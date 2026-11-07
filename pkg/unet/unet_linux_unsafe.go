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

package unet

import (
	"unsafe"

	"golang.org/x/sys/unix"
)

const msgNoSignal = unix.MSG_NOSIGNAL

func newSocket(typ int) (int, error) {
	return unix.Socket(unix.AF_UNIX, typ|unix.SOCK_CLOEXEC, 0)
}

func newSocketPair(typ int) ([2]int, error) {
	return unix.Socketpair(unix.AF_UNIX, typ|unix.SOCK_CLOEXEC, 0)
}

// pollNoTimeout calls ppoll(2) on events with no timeout.
func pollNoTimeout(events []unix.PollFd) unix.Errno {
	_, _, e := unix.Syscall6(unix.SYS_PPOLL, uintptr(unsafe.Pointer(&events[0])), uintptr(len(events)), 0, 0, 0, 0)
	return e
}
