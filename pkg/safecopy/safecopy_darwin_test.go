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

package safecopy

// protNoneFault returns the error for an access to a PROT_NONE page at addr:
// darwin raises SIGBUS, with the same si_code as an access past the end of a
// mapped file.
func protNoneFault(addr uintptr) error {
	return BusError{addr}
}
