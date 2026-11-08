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

package fsutil

import "golang.org/x/sys/unix"

// readOnlyMapFlags are the mmap flags of read-only host file mappings.
// Hypervisor.framework cannot map a MAP_SHARED mapping of a file that is open
// read-only into a guest, but it can map a MAP_PRIVATE one.
//
// darwin does not keep a MAP_PRIVATE mapping coherent with the file: it does
// not show changes made to the file after the pages were read, also by
// write(2) through another FD. Writable mappings are MAP_SHARED.
const readOnlyMapFlags = unix.MAP_PRIVATE

// mapFixedNoreplace is 0: darwin has no MAP_FIXED_NOREPLACE, and treats the
// address as a hint.
const mapFixedNoreplace = 0
