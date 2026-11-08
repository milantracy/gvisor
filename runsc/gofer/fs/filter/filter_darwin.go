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

// Package filter confines the gofer.
//
// darwin has no seccomp, mount namespaces or chroot(2) without root. Install
// applies a Seatbelt (sandbox_init) profile in their place: the gofer keeps
// access to the files that it serves and loses access to all other files,
// exec, network and IPC.
package filter

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gvisor.dev/gvisor/pkg/seatbelt"
	"gvisor.dev/gvisor/pkg/seccomp"
	"gvisor.dev/gvisor/pkg/seccomp/precompiledseccomp"
)

// Mount is a host file or directory that the gofer serves.
type Mount struct {
	// Path is the real path of the file: Seatbelt matches real paths.
	Path     string
	ReadOnly bool
}

// Options has the fields of the Linux config.Options, and the darwin fields
// below.
type Options struct {
	UDSOpenEnabled   bool
	UDSCreateEnabled bool
	ProfileEnabled   bool
	DirectFS         bool
	LisafsNeeded     bool
	CgoEnabled       bool
	VendorSyscalls   precompiledseccomp.VarSyscallNumbers
	ExtraRules       []seccomp.SyscallRules

	// Mounts are the files that the gofer serves.
	Mounts []Mount
}

// HostVendorSyscalls returns no syscalls.
func HostVendorSyscalls() precompiledseccomp.VarSyscallNumbers {
	return nil
}

// Install applies the Seatbelt profile of the gofer. It cannot be undone.
//
// The memory of lisafs channels is in files that memutil.CreateMemFD makes
// in TMPDIR and unlinks at once. Install sets TMPDIR to a new private
// directory, so that the profile does not allow the files of other programs
// in TMPDIR. It returns the directory, which the caller removes when the
// gofer exits.
func Install(opt Options) (string, error) {
	if opt.UDSOpenEnabled || opt.UDSCreateEnabled || opt.DirectFS || len(opt.ExtraRules) > 0 {
		return "", fmt.Errorf("the gofer profile on darwin does not support host Unix sockets, directfs or extensions")
	}
	tmpDir, err := os.MkdirTemp("", "runsc-gofer-")
	if err == nil {
		tmpDir, err = filepath.EvalSymlinks(tmpDir)
	}
	if err != nil {
		return "", err
	}
	os.Setenv("TMPDIR", tmpDir)
	// Go reads /etc/localtime on first use, which the profile denies.
	_ = time.Local.String()
	if err := seatbelt.Init(Profile(opt.Mounts, tmpDir)); err != nil {
		os.RemoveAll(tmpDir)
		return "", fmt.Errorf("applying the gofer Seatbelt profile: %w", err)
	}
	return tmpDir, nil
}

// Profile returns the Seatbelt profile of a gofer that serves mounts, with
// tmpDir as TMPDIR. It denies everything, then allows:
//   - Reads of each mount and the files under it, and writes too for
//     read-write mounts.
//   - Reads and writes under tmpDir.
func Profile(mounts []Mount, tmpDir string) string {
	var b strings.Builder
	b.WriteString("(version 1)\n(deny default)\n")
	fmt.Fprintf(&b, "(allow file-read* file-write* (subpath %s))\n", seatbelt.Quote(tmpDir))
	for _, m := range mounts {
		ops := "file-read* file-write*"
		if m.ReadOnly {
			ops = "file-read*"
		}
		fmt.Fprintf(&b, "(allow %s (subpath %s))\n", ops, seatbelt.Quote(m.Path))
	}
	return b.String()
}
