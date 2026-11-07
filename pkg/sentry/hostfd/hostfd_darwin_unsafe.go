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

package hostfd

import (
	"unsafe"

	"golang.org/x/sys/unix"
	"gvisor.dev/gvisor/pkg/safemem"
)

// MaxReadWriteIov is the maximum permitted size of a struct iovec array in a
// readv, writev, preadv, or pwritev host syscall.
const MaxReadWriteIov = 1024 // IOV_MAX

// MaxSendRecvMsgIov is the maximum permitted size of a struct iovec array in a
// sendmsg or recvmsg host syscall.
const MaxSendRecvMsgIov = 1024 // IOV_MAX

// darwin has no preadv2 or pwritev2. iovecsSyscall uses readv, preadv,
// writev or pwritev, and supports no flags.
const (
	sysPreadv2  = unix.SYS_READV
	sysPwritev2 = unix.SYS_WRITEV
)

// readBlock reads into dst with read(2), or with pread(2) if offset is not -1.
func readBlock(fd int32, dst safemem.Block, offset int64) (uintptr, unix.Errno) {
	var (
		n   int
		err error
	)
	if offset == -1 {
		n, err = unix.Read(int(fd), dst.ToSlice())
	} else {
		n, err = unix.Pread(int(fd), dst.ToSlice(), offset)
	}
	return result(n, err)
}

// writeBlock writes from src with write(2), or with pwrite(2) if offset is
// not -1.
func writeBlock(fd int32, src safemem.Block, offset int64) (uintptr, unix.Errno) {
	var (
		n   int
		err error
	)
	if offset == -1 {
		n, err = unix.Write(int(fd), src.ToSlice())
	} else {
		n, err = unix.Pwrite(int(fd), src.ToSlice(), offset)
	}
	return result(n, err)
}

// iovecsSyscall reads into iovs if sysno is SYS_READV, or writes from iovs if
// sysno is SYS_WRITEV. If offset is not -1, it uses preadv or pwritev.
func iovecsSyscall(sysno uintptr, fd int32, iovs []unix.Iovec, offset int64, flags uint32) (uintptr, unix.Errno) {
	if flags != 0 {
		return 0, unix.EOPNOTSUPP
	}
	bufs := make([][]byte, len(iovs))
	for i, iov := range iovs {
		bufs[i] = unsafe.Slice(iov.Base, iov.Len)
	}
	var (
		n   int
		err error
	)
	switch {
	case sysno == unix.SYS_READV && offset == -1:
		n, err = unix.Readv(int(fd), bufs)
	case sysno == unix.SYS_READV:
		n, err = unix.Preadv(int(fd), bufs, offset)
	case sysno == unix.SYS_WRITEV && offset == -1:
		n, err = unix.Writev(int(fd), bufs)
	case sysno == unix.SYS_WRITEV:
		n, err = unix.Pwritev(int(fd), bufs, offset)
	default:
		return 0, unix.ENOSYS
	}
	return result(n, err)
}

func result(n int, err error) (uintptr, unix.Errno) {
	if err != nil {
		return 0, err.(unix.Errno)
	}
	return uintptr(n), 0
}
