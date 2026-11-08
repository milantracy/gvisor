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

// Package filter installs seccomp filters to prevent prohibited syscalls
// in case it's compromised.
//
// darwin has no seccomp. Install applies a Seatbelt (sandbox_init) profile
// in its place. The profile denies all file access, exec, network and IPC;
// FDs that the Sentry opened before keep working. It filters by operation
// and path, not by system call.
package filter

import (
	"fmt"
	"strings"
	"time"

	"golang.org/x/sys/unix"
	"gvisor.dev/gvisor/pkg/log"
	"gvisor.dev/gvisor/pkg/seatbelt"
	"gvisor.dev/gvisor/pkg/seccomp/precompiledseccomp"
	"gvisor.dev/gvisor/pkg/sentry/devices/nvproxy/nvconf"
	"gvisor.dev/gvisor/pkg/sentry/platform"
	"gvisor.dev/gvisor/pkg/timing"
)

// Options has the fields of the Linux config.Options, and the darwin fields
// below.
type Options struct {
	Platform              platform.SeccompInfo
	HostNetwork           bool
	HostNetworkRawSockets bool
	HostFilesystem        bool
	ProfileEnable         bool
	NVProxy               bool
	NVProxyCaps           nvconf.DriverCaps
	TPUProxy              bool
	RDMAProxy             bool
	ControllerFD          uint32
	CgoEnabled            bool
	PluginNetwork         bool
	VendorSyscalls        precompiledseccomp.VarSyscallNumbers

	// GoferDirs are the real paths of the host directories that the gofer
	// serves. The gofer gives the Sentry open FDs of files under them.
	GoferDirs []string
}

// HostVendorSyscalls returns no syscalls.
func HostVendorSyscalls() precompiledseccomp.VarSyscallNumbers {
	return nil
}

// Install applies the Seatbelt profile of the Sentry. It cannot be undone.
func Install(opt Options, timer *timing.Timer) error {
	if opt.HostNetwork || opt.HostFilesystem || opt.NVProxy || opt.TPUProxy || opt.RDMAProxy || opt.PluginNetwork {
		return fmt.Errorf("the Sentry profile on darwin does not support host network, directfs or device proxies")
	}
	// Go reads /etc/localtime on first use, which the profile denies.
	_ = time.Local.String()
	files := openFilePaths()
	log.Infof("Applying the Sentry Seatbelt profile: %d open files, gofer dirs %v", len(files), opt.GoferDirs)
	if err := seatbelt.Init(SentryProfile(files, opt.GoferDirs)); err != nil {
		return fmt.Errorf("applying the Sentry Seatbelt profile: %w", err)
	}
	timer.Reached("Seatbelt profile applied")
	return nil
}

// maxScannedFD is the largest FD that openFilePaths checks.
const maxScannedFD = 4096

// openFilePaths returns the paths of the files that the Sentry has open.
// Pipes, sockets and closed FDs have no path.
func openFilePaths() []string {
	var files []string
	seen := make(map[string]bool)
	limit := maxScannedFD
	var rl unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_NOFILE, &rl); err == nil && rl.Cur < uint64(limit) {
		limit = int(rl.Cur)
	}
	for fd := 0; fd < limit; fd++ {
		path, err := seatbelt.FDPath(fd)
		if err != nil || seen[path] {
			continue
		}
		seen[path] = true
		files = append(files, path)
	}
	return files
}

// SentryProfile returns the Seatbelt profile of the Sentry.
//
// Seatbelt checks fstat of an FD that is not open for reading as a
// file-read-metadata of its path. So the profile allows file-read-metadata,
// and nothing more, for:
//   - files, the paths of host files that the Sentry has open, such as a
//     standard output redirected to a file.
//   - dirs, the directories that the gofer serves. The gofer gives the
//     Sentry FDs of open files under them.
func SentryProfile(files, dirs []string) string {
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
