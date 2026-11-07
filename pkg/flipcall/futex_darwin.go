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
	"fmt"
	"runtime"

	"golang.org/x/sys/unix"
)

// errNoFutex is returned by every Endpoint operation that waits for or wakes
// the peer on darwin. The darwin replacement for FUTEX_WAIT and FUTEX_WAKE
// on shared memory is os_sync_wait_on_address and os_sync_wake_by_address_any
// with OS_SYNC_WAIT_ON_ADDRESS_SHARED, which this package does not call yet.
var errNoFutex = fmt.Errorf("flipcall: no cross-process wait on darwin: %w", unix.ENOSYS)

func (ep *Endpoint) futexSetPeerActive() error {
	return errNoFutex
}

func (ep *Endpoint) futexWakePeer() error {
	return errNoFutex
}

func (ep *Endpoint) futexWaitUntilActive() error {
	return errNoFutex
}

func (ep *Endpoint) futexWakeConnState(numThreads int32) error {
	return errNoFutex
}

func yieldThread() {
	runtime.Gosched()
}
