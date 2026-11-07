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

package main

import (
	"fmt"
	"strings"
	"time"

	"gvisor.dev/gvisor/pkg/seatbelt"
)

// runsc confines the Sentry with pivot_root into an empty read-only tmpfs in
// a new mount namespace, namespaces and seccomp. darwin has no mount
// namespaces, and chroot(2) needs root. So the Sentry opens all host
// resources first (the image, the memory file, the gofer sockets, standard
// I/O), then applies a Seatbelt profile that denies all file access, exec,
// network and IPC. FDs that the Sentry opened before keep working for reads
// and writes.

// sentryProfile returns the Seatbelt profile of the Sentry.
//
// Seatbelt checks fstat of an FD that is not open for reading as a
// file-read-metadata of its path. So the profile allows file-read-metadata,
// and nothing more, for:
//   - files, the paths of host files that the Sentry has open, such as a
//     standard output redirected to a file.
//   - dirs, the mount sources of the gofer, under which the gofer gives the
//     Sentry host FDs of open files. The gofer serves all of them to the
//     Sentry already.
func sentryProfile(files, dirs []string) string {
	var b strings.Builder
	b.WriteString("(version 1)\n(deny default)\n")
	for _, f := range files {
		fmt.Fprintf(&b, "(allow file-read-metadata (literal %s))\n", seatbelt.Quote(f))
	}
	for _, d := range dirs {
		fmt.Fprintf(&b, "(allow file-read-metadata (subpath %s))\n", seatbelt.Quote(d))
	}
	return b.String()
}

// confineSentry applies the Sentry profile, with the gofer mount sources
// dirs. It cannot be undone.
func confineSentry(dirs []string) error {
	// Go reads /etc/localtime on first use, which the profile denies.
	_ = time.Local.String()
	var files []string
	for fd := 0; fd < 3; fd++ {
		// Pipes, sockets and closed FDs have no path.
		if path, err := seatbelt.FDPath(fd); err == nil {
			files = append(files, path)
		}
	}
	if err := seatbelt.Init(sentryProfile(files, dirs)); err != nil {
		return fmt.Errorf("confining the Sentry: %w", err)
	}
	return nil
}
