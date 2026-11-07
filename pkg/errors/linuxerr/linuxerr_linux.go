// Copyright 2026 The gVisor Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License"),;
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

//go:build linux

package linuxerr

import (
	"golang.org/x/sys/unix"
	"gvisor.dev/gvisor/pkg/abi/linux/errno"
)

// hostToLinux returns the Linux errno for a host errno. They are the same on
// Linux.
func hostToLinux(e unix.Errno) errno.Errno {
	return errno.Errno(e)
}

// linuxToHost returns the host errno for a Linux errno. They are the same on
// Linux.
func linuxToHost(e errno.Errno) unix.Errno {
	return unix.Errno(e)
}
