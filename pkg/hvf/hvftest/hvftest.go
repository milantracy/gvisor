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
	"fmt"
	"os"
	"testing"

	"gvisor.dev/gvisor/pkg/hvf/entitle"
)

// Main runs the tests of m in a copy of the test binary that is ad-hoc signed
// with the hypervisor entitlement, then exits. If signing fails, it runs the
// tests unsigned; tests that need a VM then get HV_DENIED.
func Main(m *testing.M) {
	if os.Getenv(entitle.SignedEnv) == "" {
		if code, err := entitle.Reexec(); err == nil {
			os.Exit(code)
		} else {
			fmt.Fprintf(os.Stderr, "hvftest: running unsigned: %v\n", err)
		}
	}
	os.Exit(m.Run())
}
