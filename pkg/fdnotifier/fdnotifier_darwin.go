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

// Package fdnotifier contains an adapter that translates IO events (e.g., a
// file became readable/writable) from native FDs to the notifications in the
// waiter package. On darwin it uses kqueue with EV_CLEAR, which is
// edge-triggered like epoll with EPOLLET on Linux.
package fdnotifier

import (
	"fmt"

	"golang.org/x/sys/unix"
	"gvisor.dev/gvisor/pkg/sync"
	"gvisor.dev/gvisor/pkg/waiter"
)

// readEvents are the events that need EVFILT_READ.
const readEvents = waiter.EventIn | waiter.EventPri | waiter.EventRdNorm | waiter.EventRdHUp | waiter.EventHUp | waiter.EventErr

// writeEvents are the events that need EVFILT_WRITE.
const writeEvents = waiter.EventOut | waiter.EventWrNorm

type fdInfo struct {
	queue *waiter.Queue

	// read and write are true if the EVFILT_READ or EVFILT_WRITE filter is
	// registered for the FD.
	read  bool
	write bool
}

// notifier holds all the state necessary to issue notifications when IO events
// occur in the observed FDs.
type notifier struct {
	// kqFD is the kqueue file descriptor used to register for io
	// notifications.
	kqFD int

	// pauseMu is held by waitAndNotify while delivering notifications, and
	// by Pause until Resume, so that no notification is delivered while
	// paused.
	pauseMu sync.Mutex

	// mu protects fdMap.
	mu sync.Mutex

	// fdMap maps file descriptors to their notification queues and waiting
	// status.
	fdMap map[int32]*fdInfo
}

// waitFD waits on mask for fd. The fdMap mutex must be held.
func (n *notifier) waitFD(fd int32, fi *fdInfo, mask waiter.EventMask) error {
	read := mask&readEvents != 0
	write := mask&writeEvents != 0
	var changes [2]unix.Kevent_t
	nc := 0
	change := func(filter int16, want, have bool) {
		switch {
		case want && !have:
			unix.SetKevent(&changes[nc], int(fd), int(filter), unix.EV_ADD|unix.EV_CLEAR)
			nc++
		case !want && have:
			unix.SetKevent(&changes[nc], int(fd), int(filter), unix.EV_DELETE)
			nc++
		}
	}
	change(unix.EVFILT_READ, read, fi.read)
	change(unix.EVFILT_WRITE, write, fi.write)
	if nc == 0 {
		return nil
	}
	if _, err := unix.Kevent(n.kqFD, changes[:nc], nil, nil); err != nil {
		// EV_DELETE of a filter that the kernel already dropped (for
		// example after the FD was closed) fails with ENOENT.
		if err != unix.ENOENT || read || write {
			return err
		}
	}
	fi.read = read
	fi.write = write
	return nil
}

// addFD adds an FD to the list of FDs observed by n.
func (n *notifier) addFD(fd int32, queue *waiter.Queue) error {
	n.mu.Lock()
	defer n.mu.Unlock()

	// Create the kqueue and start the notification goroutine on first use.
	if n.fdMap == nil {
		kqfd, err := unix.Kqueue()
		if err != nil {
			return err
		}
		unix.CloseOnExec(kqfd)
		n.kqFD = kqfd
		n.fdMap = make(map[int32]*fdInfo)
		go n.waitAndNotify() // S/R-SAFE: pauseMu excludes callbacks during save/restore.
	}

	// Panic if we're already notifying on this FD.
	if _, ok := n.fdMap[fd]; ok {
		panic(fmt.Sprintf("File descriptor %v added twice", fd))
	}

	info := &fdInfo{queue: queue}
	// We might already have something in queue to wait for.
	if err := n.waitFD(fd, info, queue.Events()); err != nil {
		return err
	}
	// Add it to the map.
	n.fdMap[fd] = info
	return nil
}

// updateFD updates the set of events the FD needs to be notified on.
func (n *notifier) updateFD(fd int32) error {
	n.mu.Lock()
	defer n.mu.Unlock()

	if fi, ok := n.fdMap[fd]; ok {
		return n.waitFD(fd, fi, fi.queue.Events())
	}

	return nil
}

// removeFD removes an FD from the list of FDs observed by n.
func (n *notifier) removeFD(fd int32) {
	n.mu.Lock()
	defer n.mu.Unlock()

	// Remove from map, then from kqueue object.
	n.waitFD(fd, n.fdMap[fd], 0)
	delete(n.fdMap, fd)
}

// hasFD returns true if the FD is in the list of observed FDs.
func (n *notifier) hasFD(fd int32) bool {
	n.mu.Lock()
	defer n.mu.Unlock()

	_, ok := n.fdMap[fd]
	return ok
}

// eventMask returns the waiter events that a kqueue event reports.
func eventMask(ev *unix.Kevent_t) waiter.EventMask {
	var mask waiter.EventMask
	switch ev.Filter {
	case unix.EVFILT_READ:
		mask = waiter.EventIn
		if ev.Flags&unix.EV_EOF != 0 {
			mask |= waiter.EventRdHUp | waiter.EventHUp
		}
	case unix.EVFILT_WRITE:
		mask = waiter.EventOut
		if ev.Flags&unix.EV_EOF != 0 {
			mask |= waiter.EventHUp
		}
	}
	if ev.Flags&unix.EV_ERROR != 0 {
		mask |= waiter.EventErr
	}
	return mask
}

// waitAndNotify run is its own goroutine and loops waiting for io event
// notifications from the kqueue object. Once notifications arrive, they are
// dispatched to the registered queue.
func (n *notifier) waitAndNotify() error {
	e := make([]unix.Kevent_t, 100)
	for {
		v, err := unix.Kevent(n.kqFD, nil, e, nil)
		if err == unix.EINTR {
			continue
		}

		if err != nil {
			return err
		}

		notified := false
		n.pauseMu.Lock()
		n.mu.Lock()
		for i := 0; i < v; i++ {
			if fi, ok := n.fdMap[int32(e[i].Ident)]; ok {
				fi.queue.Notify(eventMask(&e[i]))
				notified = true
			}
		}
		n.mu.Unlock()
		n.pauseMu.Unlock()
		if notified {
			// Let goroutines woken by Notify get a chance to run before we
			// kevent again.
			sync.Goyield()
		}
	}
}

var shared notifier

// AddFD adds an FD to the list of observed FDs.
func AddFD(fd int32, queue *waiter.Queue) error {
	return shared.addFD(fd, queue)
}

// UpdateFD updates the set of events the FD needs to be notified on.
func UpdateFD(fd int32) error {
	return shared.updateFD(fd)
}

// RemoveFD removes an FD from the list of observed FDs.
func RemoveFD(fd int32) {
	shared.removeFD(fd)
}

// HasFD returns true if the FD is in the list of observed FDs.
//
// This should only be used by tests to assert that FDs are correctly registered.
func HasFD(fd int32) bool {
	return shared.hasFD(fd)
}

// Pause stops all notifications until Resume is called.
func Pause() {
	shared.pauseMu.Lock()
}

// Resume resumes notifications after Pause.
func Resume() {
	shared.pauseMu.Unlock()
}
