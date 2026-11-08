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
// darwin has no seccomp. Install returns an error, so the Sentry does not
// run without confinement by mistake: callers must disable seccomp and
// confine the Sentry in a different way.
package filter

import (
	"fmt"

	"gvisor.dev/gvisor/pkg/seccomp/precompiledseccomp"
	"gvisor.dev/gvisor/pkg/sentry/devices/nvproxy/nvconf"
	"gvisor.dev/gvisor/pkg/sentry/platform"
	"gvisor.dev/gvisor/pkg/timing"
)

// Options has the fields of the Linux config.Options.
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
}

// HostVendorSyscalls returns no syscalls.
func HostVendorSyscalls() precompiledseccomp.VarSyscallNumbers {
	return nil
}

// Install returns an error: darwin has no seccomp.
func Install(opt Options, timer *timing.Timer) error {
	return fmt.Errorf("seccomp is not supported on darwin")
}
