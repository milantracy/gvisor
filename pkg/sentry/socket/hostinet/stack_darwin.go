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

package hostinet

import (
	"fmt"
	"os"

	"gvisor.dev/gvisor/pkg/sentry/inet"
)

// Stack is the host network stack. darwin does not support it: Configure
// returns an error, and the inet.Stack methods panic.
type Stack struct {
	inet.Stack
}

// NewStack returns a Stack that is not configured.
func NewStack() *Stack {
	return &Stack{}
}

// Configure returns an error: the host network stack is not supported on
// darwin.
func (s *Stack) Configure(allowRawSockets bool) error {
	return fmt.Errorf("hostinet: the host network is not supported on darwin")
}

// SetHostConfig does nothing.
func (s *Stack) SetHostConfig(netDev, netSNMP *os.File, ports [2]uint16) {}

// ReadPortRange returns the default ephemeral port range of Linux.
func ReadPortRange() [2]uint16 {
	return [2]uint16{32768, 60999}
}
