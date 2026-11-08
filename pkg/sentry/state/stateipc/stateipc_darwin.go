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

package stateipc

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// waitForHangup waits until stopFD is readable or the peer of the socket
// sockFD disconnects. It returns true if the peer disconnected, and false if
// stopFD is readable.
//
// darwin has no POLLRDHUP, and poll(2) reports POLLHUP only when both
// directions of sockFD are shut down. kqueue reports EV_EOF on the read
// filter as soon as the peer shuts down its write side.
func waitForHangup(stopFD, sockFD int) bool {
	kq, err := unix.Kqueue()
	if err != nil {
		panic(fmt.Sprintf("stateipc.AsyncFileClient: watchdog got kqueue error: %v", err))
	}
	defer unix.Close(kq)
	changes := []unix.Kevent_t{
		{Ident: uint64(stopFD), Filter: unix.EVFILT_READ, Flags: unix.EV_ADD},
		// EV_CLEAR: data to read on sockFD reports one event, not one event
		// per call until the data is read.
		{Ident: uint64(sockFD), Filter: unix.EVFILT_READ, Flags: unix.EV_ADD | unix.EV_CLEAR},
	}
	if _, err := unix.Kevent(kq, changes, nil, nil); err != nil {
		panic(fmt.Sprintf("stateipc.AsyncFileClient: watchdog got kevent error: %v", err))
	}
	events := make([]unix.Kevent_t, 2)
	for {
		n, err := unix.Kevent(kq, nil, events, nil)
		if err != nil {
			if err == unix.EINTR {
				continue
			}
			panic(fmt.Sprintf("stateipc.AsyncFileClient: watchdog got kevent error: %v", err))
		}
		hangup := false
		for _, ev := range events[:n] {
			switch int(ev.Ident) {
			case stopFD:
				return false
			case sockFD:
				if ev.Flags&unix.EV_EOF != 0 {
					hangup = true
				}
			}
		}
		if hangup {
			return true
		}
	}
}

// madviseHugepage does nothing: darwin has no transparent huge pages for
// file mappings.
func madviseHugepage(b []byte) error {
	return nil
}

// madviseNoHugepage does nothing: darwin has no transparent huge pages for
// file mappings.
func madviseNoHugepage(b []byte) error {
	return nil
}
