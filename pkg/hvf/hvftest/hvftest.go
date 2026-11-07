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

// Package hvftest runs tests in a binary that has the hypervisor
// entitlement.
package hvftest

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// signedEnv is set in the environment of the signed copy of the test.
const signedEnv = "HVF_TEST_SIGNED"

// Entitlements is a property list with the com.apple.security.hypervisor
// entitlement.
const Entitlements = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>com.apple.security.hypervisor</key>
	<true/>
</dict>
</plist>
`

// Main runs the tests of m in a copy of the test binary that is ad-hoc signed
// with the hypervisor entitlement, then exits. If signing fails, it runs the
// tests unsigned; tests that need a VM then get HV_DENIED.
func Main(m *testing.M) {
	if os.Getenv(signedEnv) == "" {
		if code, err := runSigned(); err == nil {
			os.Exit(code)
		} else {
			fmt.Fprintf(os.Stderr, "hvftest: running unsigned: %v\n", err)
		}
	}
	os.Exit(m.Run())
}

func runSigned() (int, error) {
	exe, err := os.Executable()
	if err != nil {
		return 0, err
	}
	dir, err := os.MkdirTemp("", "hvftest")
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
	cmd.Env = append(os.Environ(), signedEnv+"=1")
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
