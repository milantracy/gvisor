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

package hvf

import (
	"sync/atomic"

	"gvisor.dev/gvisor/pkg/abi/linux"
	"gvisor.dev/gvisor/pkg/atomicbitops"
	pkgcontext "gvisor.dev/gvisor/pkg/context"
	"gvisor.dev/gvisor/pkg/hostarch"
	"gvisor.dev/gvisor/pkg/hvf"
	"gvisor.dev/gvisor/pkg/sentry/arch"
	"gvisor.dev/gvisor/pkg/sentry/platform"
	"gvisor.dev/gvisor/pkg/sentry/platform/interrupt"
)

// switchRequest is a Switch handed to a pool thread.
type switchRequest struct {
	c  *context
	as *addressSpace
	ac *arch.Context64

	// Results.
	at  hostarch.AccessType
	err error

	// done receives a value when the request is done.
	done chan struct{}
}

// context is a platform.Context.
type context struct {
	platform.NoCPUNumbers

	p *HVF

	// interrupt forwards interrupts to c while c runs.
	interrupt interrupt.Forwarder

	// kicked is set by an interrupt while c runs. The vCPU clears it when
	// it returns ErrContextInterrupt.
	kicked atomicbitops.Bool

	// running is the vCPU that runs c, or nil.
	running atomic.Pointer[vcpu]

	// info is the signal information of the last Switch.
	info linux.SignalInfo

	req switchRequest
}

// Switch implements platform.Context.Switch.
func (c *context) Switch(ctx pkgcontext.Context, mm platform.MemoryManager, ac *arch.Context64, cpu int32) (*linux.SignalInfo, hostarch.AccessType, error) {
	as := mm.AddressSpace().(*addressSpace)
	c.kicked.Store(false)
	if !c.interrupt.Enable() {
		return nil, hostarch.NoAccess, platform.ErrContextInterrupt
	}
	c.req.c, c.req.as, c.req.ac = c, as, ac
	c.p.work <- &c.req
	<-c.req.done
	c.interrupt.Disable()
	c.req.as, c.req.ac = nil, nil
	return &c.info, c.req.at, c.req.err
}

// NotifyInterrupt implements interrupt.Receiver.NotifyInterrupt.
func (c *context) NotifyInterrupt() {
	c.kicked.Store(true)
	if v := c.running.Load(); v != nil {
		hvf.ExitVCPUs(v.id)
	}
}

// Interrupt implements platform.Context.Interrupt.
func (c *context) Interrupt() {
	c.interrupt.NotifyInterrupt()
}

// Preempt implements platform.Context.Preempt.
func (c *context) Preempt() {
	c.interrupt.Preempt()
}

// Release implements platform.Context.Release.
func (*context) Release() {}

// FullStateChanged implements platform.Context.FullStateChanged.
func (*context) FullStateChanged() {}

// PullFullState implements platform.Context.PullFullState.
func (*context) PullFullState(platform.AddressSpace, *arch.Context64) error {
	return nil
}

// PrepareSleep implements platform.Context.PrepareSleep.
func (*context) PrepareSleep() {}

// PrepareUninterruptibleSleep implements
// platform.Context.PrepareUninterruptibleSleep.
func (*context) PrepareUninterruptibleSleep() {}

// PrepareStop implements platform.Context.PrepareStop.
func (*context) PrepareStop() {}

// PrepareExecve implements platform.Context.PrepareExecve.
func (*context) PrepareExecve() {}

// PrepareExit implements platform.Context.PrepareExit.
func (*context) PrepareExit() {}
