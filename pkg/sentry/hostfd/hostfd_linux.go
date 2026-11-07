// Copyright 2020 The gVisor Authors.
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

//go:build !false
// +build !false

package hostfd

import (
	"golang.org/x/sys/unix"
	"gvisor.dev/gvisor/pkg/safemem"
)

// Syscall numbers for iovecsReadWrite.
const (
	sysPreadv2  = unix.SYS_PREADV2
	sysPwritev2 = unix.SYS_PWRITEV2
)

// readBlock reads into dst with read(2), or with pread(2) if offset is not -1.
func readBlock(fd int32, dst safemem.Block, offset int64) (uintptr, unix.Errno) {
	if offset == -1 {
		n, _, e := unix.Syscall(unix.SYS_READ, uintptr(fd), dst.Addr(), uintptr(dst.Len()))
		return n, e
	}
	n, _, e := unix.Syscall6(unix.SYS_PREAD64, uintptr(fd), dst.Addr(), uintptr(dst.Len()), uintptr(offset), 0 /* pos_h */, 0 /* unused */)
	return n, e
}

// writeBlock writes from src with write(2), or with pwrite(2) if offset is
// not -1.
func writeBlock(fd int32, src safemem.Block, offset int64) (uintptr, unix.Errno) {
	if offset == -1 {
		n, _, e := unix.Syscall(unix.SYS_WRITE, uintptr(fd), src.Addr(), uintptr(src.Len()))
		return n, e
	}
	n, _, e := unix.Syscall6(unix.SYS_PWRITE64, uintptr(fd), src.Addr(), uintptr(src.Len()), uintptr(offset), 0 /* pos_h */, 0 /* unused */)
	return n, e
}

// MaxReadWriteIov is the maximum permitted size of a struct iovec array in a
// readv, writev, preadv, or pwritev host syscall.
const MaxReadWriteIov = 1024 // UIO_MAXIOV

// MaxSendRecvMsgIov is the maximum permitted size of a struct iovec array in a
// sendmsg or recvmsg host syscall.
const MaxSendRecvMsgIov = 1024 // UIO_MAXIOV
