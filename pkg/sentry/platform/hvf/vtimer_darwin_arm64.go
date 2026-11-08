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

//go:build darwin && arm64

package hvf

import (
	"time"

	"gvisor.dev/gvisor/pkg/hvf"
	sentrytime "gvisor.dev/gvisor/pkg/sentry/time"
)

// The vDSO computes time from the guest virtual counter (CNTVCT_EL0) and the
// base counter values that the Sentry reads on the host (sentrytime.TSC). So
// the two counters must be equal.
//
// On the host, CNTVCT_EL0 counts while the host sleeps. In the guest,
// CNTVCT_EL0 is mach_absolute_time() minus the vtimer offset of the vCPU, and
// mach_absolute_time() does not count while the host sleeps. So the vtimer
// offset is mach_absolute_time() minus the host counter, and it changes each
// time the host sleeps. trackVTimerOffset measures it again periodically, and
// each vCPU applies a changed offset before it enters the guest.

const (
	// vtimerCheckInterval is the time between two measurements of the vtimer
	// offset.
	vtimerCheckInterval = time.Second

	// vtimerSlack is the change of the measured offset, in counter ticks,
	// below which the offset is not changed. A measurement has an error of
	// a few ticks; a host sleep changes the offset by much more. 24000 ticks
	// are 1 ms at 24 MHz.
	vtimerSlack = 24000
)

// currentVTimerOffset measures the vtimer offset that makes the guest virtual
// counter equal to the host counter.
func currentVTimerOffset() uint64 {
	before := sentrytime.TSC()
	abs := hvf.AbsoluteTime()
	after := sentrytime.TSC()
	host := uint64(before + (after-before)/2)
	return abs - host
}

// trackVTimerOffset updates p.vtimerOffset when the host sleeps. It does not
// return.
func (p *HVF) trackVTimerOffset() {
	t := time.NewTicker(vtimerCheckInterval)
	defer t.Stop()
	for range t.C {
		o := currentVTimerOffset()
		if d := int64(o - p.vtimerOffset.Load()); d > vtimerSlack || d < -vtimerSlack {
			p.vtimerOffset.Store(o)
		}
	}
}
