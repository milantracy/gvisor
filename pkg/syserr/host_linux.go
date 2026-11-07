// Copyright 2018 The gVisor Authors.
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

//go:build linux
// +build linux

package syserr

import (
	"fmt"

	"golang.org/x/sys/unix"
	"gvisor.dev/gvisor/pkg/abi/linux/errno"
)

const maxErrno = 134

var linuxHostTranslations [maxErrno]*Error

func getHostTranslation(err unix.Errno) *Error {
	if uint64(err) >= uint64(len(linuxHostTranslations)) {
		return nil
	}
	return linuxHostTranslations[err]
}

func addHostTranslation(host unix.Errno, trans *Error) {
	if linuxHostTranslations[host] != nil {
		panic(fmt.Sprintf("duplicate translation for host errno %q (%d)", host.Error(), host))
	}
	linuxHostTranslations[host] = trans
}

// backwardsError returns the error value for a Linux errno. On Linux, the host
// errno has the same value.
func backwardsError(linuxErrno errno.Errno, message string) error {
	return unix.Errno(linuxErrno)
}

// addLinuxOnlyHostTranslation adds the host translation for an errno that
// only Linux has. On Linux, the host errno has the same value.
func addLinuxOnlyHostTranslation(linuxErrno errno.Errno, trans *Error) {
	addHostTranslation(unix.Errno(linuxErrno), trans)
}
