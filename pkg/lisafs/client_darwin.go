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

package lisafs

import (
	"math"

	"golang.org/x/sys/unix"
	"gvisor.dev/gvisor/pkg/log"
)

// waitForHangup blocks until fd is shut down by either end.
//
// darwin has no POLLRDHUP, and poll(2) with POLLHUP alone does not report a
// shutdown while the socket has unread data. A kqueue read filter reports
// EV_EOF in that case. NOTE_LOWAT above the socket buffer size and EV_CLEAR
// make data arrival not wake the filter.
func waitForHangup(fd int) {
	kq, err := unix.Kqueue()
	if err != nil {
		log.Warningf("lisafs.Client.watch(): kqueue: %v", err)
		return
	}
	defer unix.Close(kq)
	changes := []unix.Kevent_t{{
		Ident:  uint64(fd),
		Filter: unix.EVFILT_READ,
		Flags:  unix.EV_ADD | unix.EV_CLEAR,
		Fflags: unix.NOTE_LOWAT,
		Data:   math.MaxInt32,
	}}
	if _, err := unix.Kevent(kq, changes, nil, nil); err != nil {
		log.Warningf("lisafs.Client.watch(): kevent: %v", err)
		return
	}
	events := make([]unix.Kevent_t, 1)
	for {
		n, err := unix.Kevent(kq, nil, events, nil)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			log.Warningf("lisafs.Client.watch(): %v", err)
			return
		}
		if n == 1 && events[0].Flags&unix.EV_EOF != 0 {
			return
		}
	}
}
