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

package flipcall

import (
	"math"
	"runtime"
	"syscall"
)

// Flags for os_sync_wait_on_address and os_sync_wake_by_address_*, from
// <os/os_sync_wait_on_address.h>. The packet window is shared with the peer
// process.
const (
	osSyncWaitOnAddressShared = 0x1
	osSyncWakeByAddressShared = 0x1
)

// futexWakeConnState wakes up to numThreads waiters on the connection state.
// It is the darwin equivalent of FUTEX_WAKE. darwin can wake one waiter or
// all of them.
func (ep *Endpoint) futexWakeConnState(numThreads int32) error {
	fn := libc_os_sync_wake_by_address_any_trampoline_addr
	if numThreads == math.MaxInt32 {
		fn = libc_os_sync_wake_by_address_all_trampoline_addr
	}
	r, _, e := syscall_rawSyscall(fn, ep.packet, 4, osSyncWakeByAddressShared)
	// ENOENT means that there are no waiters.
	if int32(r) == -1 && e != syscall.ENOENT {
		return e
	}
	return nil
}

// futexWaitConnState blocks while the connection state is curState. It is the
// darwin equivalent of FUTEX_WAIT. The caller checks the state again after
// it returns, so early returns are not errors.
func (ep *Endpoint) futexWaitConnState(curState uint32) error {
	r, _, e := syscall_syscall6(libc_os_sync_wait_on_address_trampoline_addr, ep.packet, uintptr(curState), 4, osSyncWaitOnAddressShared, 0, 0)
	if int32(r) == -1 && e != syscall.EINTR && e != syscall.ENOMEM {
		return e
	}
	return nil
}

func yieldThread() {
	syscall_rawSyscall(libc_sched_yield_trampoline_addr, 0, 0, 0)
	// The thread we're trying to yield to may be waiting for a Go runtime P.
	// runtime.Gosched() will hand off ours if necessary.
	runtime.Gosched()
}
