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

import (
	"os"
	"path/filepath"
)

// Default values of flags that differ by host OS. On darwin, hvf is the only
// platform, directfs is not supported (no O_PATH), and there are no cgroups.
const (
	defaultPlatform      = "hvf"
	defaultDirectFS      = false
	defaultIgnoreCgroups = true
	defaultRootDir       = "/var/run/runsc"
	defaultRootDirHelp   = "$TMPDIR/runsc, " + defaultRootDir
)

// osDefaultRootDir returns the runtime root directory when neither --root nor
// $XDG_RUNTIME_DIR is set.
//
// /var/run is writable only by root on macOS. $TMPDIR is a directory of the
// user that only the user can open, and macOS empties it at boot, as Linux
// empties /var/run.
func osDefaultRootDir() string {
	if tmp := os.Getenv("TMPDIR"); filepath.IsAbs(tmp) {
		return filepath.Join(tmp, "runsc")
	}
	return defaultRootDir
}
