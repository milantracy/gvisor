// Copyright 2019 The gVisor Authors.
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

package flipcall

import "fmt"

func (ep *Endpoint) futexSetPeerActive() error {
	if ep.connState().CompareAndSwap(ep.activeState, ep.inactiveState) {
		return nil
	}
	switch cs := ep.connState().Load(); cs {
	case csShutdown:
		return ShutdownError{}
	default:
		return fmt.Errorf("unexpected connection state before FUTEX_WAKE: %v", cs)
	}
}

func (ep *Endpoint) futexWakePeer() error {
	if err := ep.futexWakeConnState(1); err != nil {
		return fmt.Errorf("failed to FUTEX_WAKE peer Endpoint: %v", err)
	}
	return nil
}

func (ep *Endpoint) futexWaitUntilActive() error {
	for {
		switch cs := ep.connState().Load(); cs {
		case ep.activeState:
			return nil
		case ep.inactiveState:
			if ep.isShutdownLocally() {
				return ShutdownError{}
			}
			if err := ep.futexWaitConnState(ep.inactiveState); err != nil {
				return fmt.Errorf("failed to FUTEX_WAIT for peer Endpoint: %v", err)
			}
			continue
		case csShutdown:
			return ShutdownError{}
		default:
			return fmt.Errorf("unexpected connection state before FUTEX_WAIT: %v", cs)
		}
	}
}
