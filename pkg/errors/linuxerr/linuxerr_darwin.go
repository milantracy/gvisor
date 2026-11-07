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

//go:build darwin

package linuxerr

import (
	"fmt"

	"golang.org/x/sys/unix"
	"gvisor.dev/gvisor/pkg/abi/linux/errno"
)

// darwinToLinux maps each darwin errno that has a Linux equivalent to the
// Linux errno.
var darwinToLinux = map[unix.Errno]errno.Errno{
	unix.E2BIG:           errno.E2BIG,
	unix.EACCES:          errno.EACCES,
	unix.EADDRINUSE:      errno.EADDRINUSE,
	unix.EADDRNOTAVAIL:   errno.EADDRNOTAVAIL,
	unix.EAFNOSUPPORT:    errno.EAFNOSUPPORT,
	unix.EAGAIN:          errno.EAGAIN,
	unix.EALREADY:        errno.EALREADY,
	unix.EBADF:           errno.EBADF,
	unix.EBADMSG:         errno.EBADMSG,
	unix.EBUSY:           errno.EBUSY,
	unix.ECANCELED:       errno.ECANCELED,
	unix.ECHILD:          errno.ECHILD,
	unix.ECONNABORTED:    errno.ECONNABORTED,
	unix.ECONNREFUSED:    errno.ECONNREFUSED,
	unix.ECONNRESET:      errno.ECONNRESET,
	unix.EDEADLK:         errno.EDEADLK,
	unix.EDESTADDRREQ:    errno.EDESTADDRREQ,
	unix.EDOM:            errno.EDOM,
	unix.EDQUOT:          errno.EDQUOT,
	unix.EEXIST:          errno.EEXIST,
	unix.EFAULT:          errno.EFAULT,
	unix.EFBIG:           errno.EFBIG,
	unix.EHOSTDOWN:       errno.EHOSTDOWN,
	unix.EHOSTUNREACH:    errno.EHOSTUNREACH,
	unix.EIDRM:           errno.EIDRM,
	unix.EILSEQ:          errno.EILSEQ,
	unix.EINPROGRESS:     errno.EINPROGRESS,
	unix.EINTR:           errno.EINTR,
	unix.EINVAL:          errno.EINVAL,
	unix.EIO:             errno.EIO,
	unix.EISCONN:         errno.EISCONN,
	unix.EISDIR:          errno.EISDIR,
	unix.ELOOP:           errno.ELOOP,
	unix.EMFILE:          errno.EMFILE,
	unix.EMLINK:          errno.EMLINK,
	unix.EMSGSIZE:        errno.EMSGSIZE,
	unix.EMULTIHOP:       errno.EMULTIHOP,
	unix.ENAMETOOLONG:    errno.ENAMETOOLONG,
	unix.ENETDOWN:        errno.ENETDOWN,
	unix.ENETRESET:       errno.ENETRESET,
	unix.ENETUNREACH:     errno.ENETUNREACH,
	unix.ENFILE:          errno.ENFILE,
	unix.ENOBUFS:         errno.ENOBUFS,
	unix.ENODATA:         errno.ENODATA,
	unix.ENODEV:          errno.ENODEV,
	unix.ENOENT:          errno.ENOENT,
	unix.ENOEXEC:         errno.ENOEXEC,
	unix.ENOLCK:          errno.ENOLCK,
	unix.ENOLINK:         errno.ENOLINK,
	unix.ENOMEM:          errno.ENOMEM,
	unix.ENOMSG:          errno.ENOMSG,
	unix.ENOPROTOOPT:     errno.ENOPROTOOPT,
	unix.ENOSPC:          errno.ENOSPC,
	unix.ENOSR:           errno.ENOSR,
	unix.ENOSTR:          errno.ENOSTR,
	unix.ENOSYS:          errno.ENOSYS,
	unix.ENOTBLK:         errno.ENOTBLK,
	unix.ENOTCONN:        errno.ENOTCONN,
	unix.ENOTDIR:         errno.ENOTDIR,
	unix.ENOTEMPTY:       errno.ENOTEMPTY,
	unix.ENOTRECOVERABLE: errno.ENOTRECOVERABLE,
	unix.ENOTSOCK:        errno.ENOTSOCK,
	unix.ENOTTY:          errno.ENOTTY,
	unix.ENXIO:           errno.ENXIO,
	unix.EOPNOTSUPP:      errno.EOPNOTSUPP,
	unix.EOVERFLOW:       errno.EOVERFLOW,
	unix.EOWNERDEAD:      errno.EOWNERDEAD,
	unix.EPERM:           errno.EPERM,
	unix.EPFNOSUPPORT:    errno.EPFNOSUPPORT,
	unix.EPIPE:           errno.EPIPE,
	unix.EPROTO:          errno.EPROTO,
	unix.EPROTONOSUPPORT: errno.EPROTONOSUPPORT,
	unix.EPROTOTYPE:      errno.EPROTOTYPE,
	unix.ERANGE:          errno.ERANGE,
	unix.EREMOTE:         errno.EREMOTE,
	unix.EROFS:           errno.EROFS,
	unix.ESHUTDOWN:       errno.ESHUTDOWN,
	unix.ESOCKTNOSUPPORT: errno.ESOCKTNOSUPPORT,
	unix.ESPIPE:          errno.ESPIPE,
	unix.ESRCH:           errno.ESRCH,
	unix.ESTALE:          errno.ESTALE,
	unix.ETIME:           errno.ETIME,
	unix.ETIMEDOUT:       errno.ETIMEDOUT,
	unix.ETOOMANYREFS:    errno.ETOOMANYREFS,
	unix.ETXTBSY:         errno.ETXTBSY,
	unix.EUSERS:          errno.EUSERS,
	unix.EXDEV:           errno.EXDEV, unix.ENOTSUP: errno.EOPNOTSUPP,
	unix.ENOATTR: errno.ENODATA,
}

// linuxToDarwin is the inverse of darwinToLinux. Where two darwin errnos map
// to one Linux errno, it has the one with the same name.
var linuxToDarwin = func() map[errno.Errno]unix.Errno {
	m := make(map[errno.Errno]unix.Errno, len(darwinToLinux))
	for d, l := range darwinToLinux {
		if d == unix.ENOTSUP || d == unix.ENOATTR {
			continue
		}
		m[l] = d
	}
	return m
}()

// hostToLinux returns the Linux errno for a darwin errno. It panics if there
// is none.
func hostToLinux(e unix.Errno) errno.Errno {
	l, ok := darwinToLinux[e]
	if !ok {
		panic(fmt.Sprintf("no Linux errno for host errno %q (%d)", e.Error(), e))
	}
	return l
}

// linuxToHost returns the darwin errno for a Linux errno. For a Linux errno
// that darwin does not have, it returns an errno that is not a darwin errno,
// so that it compares unequal to all of them.
func linuxToHost(e errno.Errno) unix.Errno {
	if d, ok := linuxToDarwin[e]; ok {
		return d
	}
	return unix.Errno(linuxOnlyErrnoBase + uint32(e))
}

// linuxOnlyErrnoBase is above all darwin errnos.
const linuxOnlyErrnoBase = 0x10000
