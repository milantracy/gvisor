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

package aio

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/unix"
)

// LinuxQueue is not available on darwin, which has no Linux AIO.
// NewLinuxQueue always fails, so callers fall back to GoQueue.
type LinuxQueue struct{}

// NewLinuxQueue returns ENOSYS.
func NewLinuxQueue(cap int) (*LinuxQueue, error) {
	return nil, unix.ENOSYS
}

// Destroy implements Queue.Destroy.
func (*LinuxQueue) Destroy() { panic("aio.LinuxQueue on darwin") }

// Cap implements Queue.Cap.
func (*LinuxQueue) Cap() int { panic("aio.LinuxQueue on darwin") }

// Add implements Queue.Add.
func (*LinuxQueue) Add(Request) { panic("aio.LinuxQueue on darwin") }

// Wait implements Queue.Wait.
func (*LinuxQueue) Wait([]Completion, int) ([]Completion, error) {
	panic("aio.LinuxQueue on darwin")
}

// goQueueSyscall executes r with one blocking call to the libSystem pread,
// pwrite, preadv or pwritev wrapper.
func goQueueSyscall(r Request) (uintptr, unix.Errno) {
	var (
		n   int
		err error
	)
	switch r.Op {
	case OpRead:
		n, err = unix.Pread(int(r.FD), unsafe.Slice((*byte)(r.Buf), r.Len), r.Off)
	case OpWrite:
		n, err = unix.Pwrite(int(r.FD), unsafe.Slice((*byte)(r.Buf), r.Len), r.Off)
	case OpReadv:
		n, err = unix.Preadv(int(r.FD), iovecsToSlices(r), r.Off)
	case OpWritev:
		n, err = unix.Pwritev(int(r.FD), iovecsToSlices(r), r.Off)
	default:
		panic(fmt.Sprintf("unknown op %v", r.Op))
	}
	if err != nil {
		return 0, err.(unix.Errno)
	}
	return uintptr(n), 0
}

func iovecsToSlices(r Request) [][]byte {
	iovs := unsafe.Slice((*unix.Iovec)(r.Buf), r.Len)
	bufs := make([][]byte, len(iovs))
	for i, iov := range iovs {
		bufs[i] = unsafe.Slice(iov.Base, iov.Len)
	}
	return bufs
}
