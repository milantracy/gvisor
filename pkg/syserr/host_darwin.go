// Copyright 2021 The gVisor Authors.
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
// +build darwin

package syserr

import (
	"fmt"

	"golang.org/x/sys/unix"
	"gvisor.dev/gvisor/pkg/abi/linux/errno"
	"gvisor.dev/gvisor/pkg/errors"
	"gvisor.dev/gvisor/pkg/errors/linuxerr"
)

const maxErrno = 107

var darwinHostTranslations [maxErrno]*Error

func getHostTranslation(err unix.Errno) *Error {
	if uint64(err) >= uint64(len(darwinHostTranslations)) {
		return nil
	}
	return darwinHostTranslations[err]
}

// TODO(gvisor.dev/issue/1270): We currently only add translations for errors
// that exist both on Darwin and Linux.
func addHostTranslation(host unix.Errno, trans *Error) {
	if darwinHostTranslations[host] != nil {
		panic(fmt.Sprintf("duplicate translation for host errno %q (%d)", host.Error(), host))
	}
	darwinHostTranslations[host] = trans
}

// backwardsError returns the error value for a Linux errno. darwin errnos
// have other values, so this is a linuxerr error, not a unix.Errno.
func backwardsError(linuxErrno errno.Errno, message string) error {
	if e, ok := linuxerr.FromErrno(linuxErrno); ok {
		return e
	}
	return errors.New(linuxErrno, message)
}

// addLinuxOnlyHostTranslation adds the host translation for an errno that
// only Linux has. darwin has none of these, except EDEADLK.
func addLinuxOnlyHostTranslation(linuxErrno errno.Errno, trans *Error) {
	if linuxErrno == errno.EDEADLOCK {
		addHostTranslation(unix.EDEADLK, trans)
	}
}
