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
	"time"
)

// A Switch hands its request to a vCPU thread and waits for the result. Both
// sides first spin and then block, as systrap does: a spinning hand-off costs
// tens of nanoseconds, and waking a blocked thread costs microseconds.
const (
	// vcpuSpin is how long an idle vCPU thread waits for a new request
	// before it blocks.
	vcpuSpin = 50 * time.Microsecond
	// switchSpin is how long a Switch waits for its result before it
	// blocks.
	switchSpin = 20 * time.Microsecond
)

// spinFor returns true when *addr becomes want, or false after d.
func spinFor(addr *uint32, want uint32, d time.Duration) bool {
	var start time.Time
	for i := 0; ; i++ {
		if atomic.LoadUint32(addr) == want {
			return true
		}
		if i%64 == 63 {
			if start.IsZero() {
				start = time.Now()
			} else if time.Since(start) > d {
				return false
			}
		}
	}
}

// claim returns a free vCPU and marks it as busy. It prefers last, the vCPU
// that the caller used before.
func (p *HVF) claim(last *vcpu) *vcpu {
	if last != nil && atomic.CompareAndSwapUint32(&last.busy, 0, 1) {
		return last
	}
	for {
		seq := atomic.LoadUint32(&p.freeSeq)
		for _, v := range p.vcpus {
			if atomic.CompareAndSwapUint32(&v.busy, 0, 1) {
				return v
			}
		}
		// All vCPUs are busy. release changes freeSeq after it frees one.
		atomic.AddInt32(&p.claimWaiters, 1)
		waitGoroutine(&p.freeSeq, seq)
		atomic.AddInt32(&p.claimWaiters, -1)
	}
}

// release marks v as free.
func (p *HVF) release(v *vcpu) {
	atomic.StoreUint32(&v.busy, 0)
	atomic.AddUint32(&p.freeSeq, 1)
	if atomic.LoadInt32(&p.claimWaiters) > 0 {
		wake(&p.freeSeq)
	}
}

// post gives r to v, which the caller has claimed.
func (v *vcpu) post(r *switchRequest) {
	v.req = r
	atomic.StoreUint32(&v.mbox, 1)
	if atomic.LoadUint32(&v.sleeping) != 0 {
		wake(&v.mbox)
	}
}

// next waits for a request on the vCPU thread and returns it.
func (v *vcpu) next() *switchRequest {
	if !spinFor(&v.mbox, 1, vcpuSpin) {
		for atomic.LoadUint32(&v.mbox) == 0 {
			atomic.StoreUint32(&v.sleeping, 1)
			if atomic.LoadUint32(&v.mbox) == 0 {
				waitThread(&v.mbox, 0)
			}
			atomic.StoreUint32(&v.sleeping, 0)
		}
	}
	r := v.req
	v.req = nil
	atomic.StoreUint32(&v.mbox, 0)
	return r
}

// complete tells the waiter of r that r is done.
func (r *switchRequest) complete() {
	atomic.StoreUint32(&r.done, 1)
	if atomic.LoadUint32(&r.sleeping) != 0 {
		wake(&r.done)
	}
}

// wait waits until r is done.
func (r *switchRequest) wait() {
	if spinFor(&r.done, 1, switchSpin) {
		return
	}
	for atomic.LoadUint32(&r.done) == 0 {
		atomic.StoreUint32(&r.sleeping, 1)
		if atomic.LoadUint32(&r.done) == 0 {
			waitGoroutine(&r.done, 0)
		}
		atomic.StoreUint32(&r.sleeping, 0)
	}
}
