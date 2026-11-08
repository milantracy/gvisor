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

package config

import "testing"

func TestDefaultRootDirDarwin(t *testing.T) {
	for _, tc := range []struct {
		xdg, tmp, want string
	}{
		{"", "/var/folders/ab/cd/T/", "/var/folders/ab/cd/T/runsc"},
		{"/run/user/501", "/var/folders/ab/cd/T/", "/run/user/501/runsc"},
		{"", "", "/var/run/runsc"},
		{"", "relative/tmp", "/var/run/runsc"},
	} {
		t.Setenv(xdgRuntimeDirEnvVar, tc.xdg)
		t.Setenv("TMPDIR", tc.tmp)
		if got := DefaultRootDir(); got != tc.want {
			t.Errorf("DefaultRootDir() with XDG_RUNTIME_DIR=%q, TMPDIR=%q = %q, want %q", tc.xdg, tc.tmp, got, tc.want)
		}
	}
}
