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

package hostmm

import "golang.org/x/sys/unix"

// membarrier returns ENOSYS: darwin has no membarrier(2), so Probe reports no
// memory barrier support.
func membarrier(cmd uintptr) (uintptr, uintptr, unix.Errno) {
	return 0, 0, unix.ENOSYS
}

// rawMembarrier returns ENOSYS, as membarrier does.
func rawMembarrier(cmd uintptr) (uintptr, uintptr, unix.Errno) {
	return 0, 0, unix.ENOSYS
}
