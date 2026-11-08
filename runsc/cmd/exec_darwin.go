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

package cmd

import (
	"io"
	"os"
	"sync"

	"golang.org/x/sys/unix"
	"gvisor.dev/gvisor/pkg/log"
	"gvisor.dev/gvisor/pkg/seatbelt"
)

// relayHostFiles replaces the files in fdMap that the Sentry cannot use with
// pipes, and copies between the pipes and the files. It returns a function
// that waits for the copies to end.
//
// Seatbelt checks fstat of an FD that is not open for reading as a
// file-read-metadata of its path, which the Sentry profile allows only for
// the files that the Sentry has open when it is confined (see
// runsc/boot/filter). So the Sentry cannot import a write-only file with a
// path, such as a standard output redirected to a file. Such a file is
// replaced with a pipe; pipes have no path.
func relayHostFiles(fdMap map[int]*os.File) func() {
	var wg sync.WaitGroup
	for guestFD, f := range fdMap {
		fl, err := unix.FcntlInt(f.Fd(), unix.F_GETFL, 0)
		if err != nil || fl&unix.O_ACCMODE != unix.O_WRONLY {
			continue
		}
		if _, err := seatbelt.FDPath(int(f.Fd())); err != nil {
			continue // No path.
		}
		r, w, err := os.Pipe()
		if err != nil {
			log.Warningf("Cannot relay FD %d: %v", guestFD, err)
			continue
		}
		log.Debugf("Relaying guest FD %d to %q through a pipe", guestFD, f.Name())
		fdMap[guestFD] = w
		wg.Add(1)
		go func(dst *os.File) {
			defer wg.Done()
			defer r.Close()
			// dst is not closed: it may be the standard output of this
			// process.
			if _, err := io.Copy(dst, r); err != nil {
				log.Warningf("Relaying guest FD %d: %v", guestFD, err)
			}
		}(f)
	}
	return wg.Wait
}
