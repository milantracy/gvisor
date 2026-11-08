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

package boot

import (
	"path/filepath"

	"golang.org/x/sys/unix"
	"gvisor.dev/gvisor/pkg/log"
	"gvisor.dev/gvisor/runsc/boot/filter"
	"gvisor.dev/gvisor/runsc/specutils"
)

// dupCloexec duplicates oldfd to newfd, with FD_CLOEXEC set on newfd. darwin
// has no dup3(2); the Sentry does not fork or exec between the two calls.
func dupCloexec(oldfd, newfd int) error {
	if err := unix.Dup2(oldfd, newfd); err != nil {
		return err
	}
	unix.CloseOnExec(newfd)
	return nil
}

// waitForHangup waits until the peer of the socket fd disconnects.
//
// darwin has no POLLRDHUP, and poll(2) reports POLLHUP only when both
// directions of fd are shut down. kqueue reports EV_EOF on the read filter
// as soon as the peer shuts down its write side.
func waitForHangup(fd int) error {
	kq, err := unix.Kqueue()
	if err != nil {
		return err
	}
	defer unix.Close(kq)
	// EV_CLEAR: data to read on fd reports one event, not one event per call
	// until the data is read.
	changes := []unix.Kevent_t{{Ident: uint64(fd), Filter: unix.EVFILT_READ, Flags: unix.EV_ADD | unix.EV_CLEAR}}
	if _, err := unix.Kevent(kq, changes, nil, nil); err != nil {
		return err
	}
	events := make([]unix.Kevent_t, 1)
	for {
		n, err := unix.Kevent(kq, nil, events, nil)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return err
		}
		if n == 1 && events[0].Flags&unix.EV_EOF != 0 {
			return nil
		}
	}
}

// setHostFilterOptions sets opts.GoferDirs to the real paths of the host
// directories that the gofer serves for the root container: the root
// filesystem if it is a lisafs mount, and the bind mounts.
func (l *Loader) setHostFilterOptions(opts *filter.Options) {
	spec := l.root.spec
	var dirs []string
	if len(l.root.goferMountConfs) > 0 && l.root.goferMountConfs[0].ShouldUseLisafs() && spec.Root != nil {
		dirs = append(dirs, spec.Root.Path)
	}
	for _, m := range spec.Mounts {
		if specutils.IsGoferMount(m) {
			dirs = append(dirs, m.Source)
		}
	}
	for _, d := range dirs {
		// Seatbelt matches real paths.
		real, err := filepath.EvalSymlinks(d)
		if err != nil {
			log.Warningf("Cannot resolve gofer directory %q: %v", d, err)
			continue
		}
		opts.GoferDirs = append(opts.GoferDirs, real)
	}
}
