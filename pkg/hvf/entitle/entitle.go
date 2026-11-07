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

// Package entitle runs the current binary with the hypervisor entitlement.
package entitle

import (
	_ "embed" // For Entitlements.
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// SignedEnv is set in the environment of the signed copy.
const SignedEnv = "HVF_SIGNED"

// Entitlements is a property list with the com.apple.security.hypervisor
// entitlement.
//
//go:embed hypervisor.entitlements
var Entitlements string

// Reexec runs a copy of the current binary, ad-hoc signed with the
// hypervisor entitlement, with the same arguments, standard I/O, and
// environment plus SignedEnv. It returns the exit code of the copy. Signing
// needs no developer account. A binary that is signed at build time with
// hvf_signed_binary (defs.bzl) does not need Reexec.
func Reexec() (int, error) {
	exe, err := os.Executable()
	if err != nil {
		return 0, err
	}
	dir, err := os.MkdirTemp("", "hvf")
	if err != nil {
		return 0, err
	}
	defer os.RemoveAll(dir)
	data, err := os.ReadFile(exe)
	if err != nil {
		return 0, err
	}
	bin := filepath.Join(dir, filepath.Base(exe))
	plist := filepath.Join(dir, "hvf.entitlements")
	if err := os.WriteFile(bin, data, 0755); err != nil {
		return 0, err
	}
	if err := os.WriteFile(plist, []byte(Entitlements), 0644); err != nil {
		return 0, err
	}
	if out, err := exec.Command("/usr/bin/codesign", "-f", "-s", "-", "--entitlements", plist, bin).CombinedOutput(); err != nil {
		return 0, fmt.Errorf("codesign: %v: %s", err, out)
	}
	cmd := exec.Command(bin, os.Args[1:]...)
	cmd.Env = append(os.Environ(), SignedEnv+"=1")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return ee.ExitCode(), nil
		}
		return 0, err
	}
	return 0, nil
}
