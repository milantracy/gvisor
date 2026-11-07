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

package fdchannel

import (
	"syscall"

	"golang.org/x/sys/unix"
)

// newConnectedSockets uses SOCK_DGRAM because darwin has no AF_UNIX
// SOCK_SEQPACKET. Endpoints send only zero-length datagrams with one
// SCM_RIGHTS message, so message boundaries are kept the same way.
//
// darwin has no SOCK_CLOEXEC. syscall.ForkLock keeps a concurrent fork from
// inheriting the sockets before FD_CLOEXEC is set.
func newConnectedSockets() ([2]int, error) {
	syscall.ForkLock.RLock()
	defer syscall.ForkLock.RUnlock()
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_DGRAM, 0)
	if err != nil {
		return fds, err
	}
	unix.CloseOnExec(fds[0])
	unix.CloseOnExec(fds[1])
	return fds, nil
}

// endpointIov is the one iovec that darwin needs in every msghdr: sendmsg
// and recvmsg fail with EMSGSIZE when msg_iovlen is 0. Sends use a length of
// 0, so datagrams stay zero-length. Receives use a length of 1, because
// recvmsg returns 0 at once, without waiting for a datagram, when the iovec
// has no space.
type endpointIov struct {
	iovec unix.Iovec
	buf   byte
}

func (i *endpointIov) init(m *unix.Msghdr) {
	i.iovec.Base = &i.buf
	m.Iov = &i.iovec
	m.SetIovlen(1)
}

func (i *endpointIov) forSend() { i.iovec.SetLen(0) }
func (i *endpointIov) forRecv() { i.iovec.SetLen(1) }
