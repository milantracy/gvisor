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

// Package seatbelt confines the calling process with a macOS Seatbelt
// profile (sandbox_init(3)). The profile language (SBPL) is not documented by
// Apple; see /System/Library/Sandbox/Profiles for examples.
//
// A profile restricts operations by kind and path (file-read*, file-write*,
// network*, process-exec, mach-lookup, ...). It applies to the calling
// process and its children, and it cannot be removed. Operations on FDs that
// the process opened before do not go through most file checks.
package seatbelt

import "strings"

// Quote returns s as an SBPL string literal.
func Quote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"', '\\':
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	b.WriteByte('"')
	return b.String()
}
