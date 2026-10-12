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

//go:build cgo && seccomp

package main

import (
	"fmt"
	_ "unsafe" // for go:linkname

	"github.com/opencontainers/runc/libcontainer/configs"
	runcseccomp "github.com/opencontainers/runc/libcontainer/seccomp"
	_ "github.com/opencontainers/runc/libcontainer/seccomp/patchbpf"
	"github.com/opencontainers/runc/libcontainer/specconv"
	specs "github.com/opencontainers/runtime-spec/specs-go"
	libseccomp "github.com/seccomp/libseccomp-golang"
	"golang.org/x/sys/unix"
)

//go:linkname runcGetAction github.com/opencontainers/runc/libcontainer/seccomp.getAction
func runcGetAction(configs.Action, *uint) (libseccomp.ScmpAction, error)

//go:linkname runcMatchCall github.com/opencontainers/runc/libcontainer/seccomp.matchCall
func runcMatchCall(*libseccomp.ScmpFilter, *configs.Syscall, libseccomp.ScmpAction) error

//go:linkname runcPatchFilter github.com/opencontainers/runc/libcontainer/seccomp/patchbpf.enosysPatchFilter
func runcPatchFilter(*configs.Seccomp, *libseccomp.ScmpFilter) ([]unix.SockFilter, error)

func init() {
	variants = append(variants, variant{name: "runc", program: runcProgram, install: runcInstall})
}

// runcInstall is the runc path: spec conversion, then InitSeccomp.
func runcInstall(s *specs.LinuxSeccomp) error {
	// runc sets no_new_privs itself before InitSeccomp.
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return err
	}
	cfg, err := specconv.SetupSeccomp(s)
	if err != nil {
		return err
	}
	_, err = runcseccomp.InitSeccomp(cfg)
	return err
}

// runcProgram mirrors InitSeccomp up to the load, and returns the patched
// program that runc would load.
func runcProgram(s *specs.LinuxSeccomp) ([]unix.SockFilter, error) {
	cfg, err := specconv.SetupSeccomp(s)
	if err != nil {
		return nil, err
	}
	def, err := runcGetAction(cfg.DefaultAction, cfg.DefaultErrnoRet)
	if err != nil {
		return nil, err
	}
	filter, err := libseccomp.NewFilter(def)
	if err != nil {
		return nil, err
	}
	defer filter.Release()
	for _, a := range cfg.Architectures {
		arch, err := libseccomp.GetArchFromString(a)
		if err != nil {
			return nil, err
		}
		if err := filter.AddArch(arch); err != nil {
			return nil, err
		}
	}
	if len(cfg.Syscalls) > 32 {
		if err := filter.SetOptimize(2); err != nil {
			return nil, err
		}
	}
	if err := filter.SetNoNewPrivsBit(false); err != nil {
		return nil, err
	}
	for _, call := range cfg.Syscalls {
		if err := runcMatchCall(filter, call, def); err != nil {
			return nil, err
		}
	}
	return runcPatchFilter(cfg, filter)
}

func libseccompVersion() string {
	major, minor, micro := libseccomp.GetLibraryVersion()
	return fmt.Sprintf("%d.%d.%d (runc libcontainer)", major, minor, micro)
}
