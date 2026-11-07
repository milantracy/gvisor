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
	"fmt"
	"runtime"

	"gvisor.dev/gvisor/pkg/abi/linux"
	pkgcontext "gvisor.dev/gvisor/pkg/context"
	"gvisor.dev/gvisor/pkg/hostarch"
	"gvisor.dev/gvisor/pkg/hvf"
	"gvisor.dev/gvisor/pkg/memutil"
	"gvisor.dev/gvisor/pkg/ring0/pagetables"
	"gvisor.dev/gvisor/pkg/sentry/platform"
	"gvisor.dev/gvisor/pkg/sentry/platform/hvf/ipa"
	"gvisor.dev/gvisor/pkg/sync"
)

const (
	// ipaBits is the size of the IPA space.
	ipaBits = 40
	// ipaStart is the start of the IPA that the registry allocates.
	ipaStart = 1 << 30

	// upperBottom is the start of the TTBR1 half of the VA space.
	upperBottom = 0xffff000000000000
	// stubVA is the VA of the stub page in the TTBR1 half.
	stubVA = 0xfffffffffe000000

	// maxUserAddress is the end of the TTBR0 half of the VA space.
	maxUserAddress = 1 << 48
)

// HVF is a platform on Hypervisor.framework.
type HVF struct {
	platform.NoCPUPreemptionDetection
	platform.NoCPUNumbers
	platform.UseHostGlobalMemoryBarrier

	registry *ipa.Registry
	ptMem    *ptMemory

	// kernel has the TTBR1 page tables, which map the stub page.
	kernel *pagetables.PageTables

	// vcpus is the pool. It does not change after New.
	vcpus []*vcpu

	// work is where Switch sends work to the pool.
	work chan *switchRequest
}

var (
	createOnce sync.Once
	created    *HVF
	createErr  error
)

// New returns the HVF platform of the process. A process can have one VM, so
// all calls return the same platform.
func New() (*HVF, error) {
	createOnce.Do(func() {
		created, createErr = newHVF()
	})
	return created, createErr
}

func newHVF() (*HVF, error) {
	if hostarch.PageSize != 16<<10 {
		return nil, fmt.Errorf("hvf needs 16 KiB pages (the pagesize_16k build); hostarch.PageSize is %d", hostarch.PageSize)
	}
	if err := hvf.CreateVM(ipaBits); err != nil {
		return nil, fmt.Errorf("creating VM: %w", err)
	}
	p := &HVF{
		registry: ipa.New(mapper{}, ipaStart, 1<<ipaBits, hostarch.PageSize, hostarch.HugePageSize),
		work:     make(chan *switchRequest),
	}
	memutil.SetMappingObserver(p.registry)
	p.ptMem = newPTMemory(p.registry)
	stubIPA, err := newStubPage(p.registry)
	if err != nil {
		return nil, fmt.Errorf("mapping the stub page: %w", err)
	}
	p.kernel = pagetables.New(newAllocator(p.ptMem))
	p.kernel.Map(stubVA, hostarch.PageSize, pagetables.MapOpts{
		AccessType: hostarch.ReadExecute,
		Global:     true,
	}, uintptr(stubIPA))
	p.kernel.MarkReadOnlyShared()

	n := runtime.NumCPU()
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		v := &vcpu{p: p}
		p.vcpus = append(p.vcpus, v)
		go v.loop(errs)
	}
	for i := 0; i < n; i++ {
		if err := <-errs; err != nil {
			return nil, err
		}
	}
	return p, nil
}

// mapper is an ipa.Mapper on Hypervisor.framework.
type mapper struct{}

func (mapper) Map(host uintptr, ipa uint64, size uint64, flags ipa.Flags) error {
	return hvf.Map(host, ipa, size, hvf.MemoryFlags(flags))
}

func (mapper) Unmap(ipa, size uint64) error {
	return hvf.Unmap(ipa, size)
}

func (mapper) Protect(ipa, size uint64, flags ipa.Flags) error {
	return hvf.Protect(ipa, size, hvf.MemoryFlags(flags))
}

// SupportsAddressSpaceIO implements platform.Platform.SupportsAddressSpaceIO.
func (*HVF) SupportsAddressSpaceIO() bool {
	return false
}

// MapUnit implements platform.Platform.MapUnit. The registry maps memory
// lazily, so the cost of MapFile does not depend on the size.
func (*HVF) MapUnit() uint64 {
	return 0
}

// MinUserAddress implements platform.Platform.MinUserAddress.
func (*HVF) MinUserAddress() hostarch.Addr {
	return hostarch.PageSize
}

// MaxUserAddress implements platform.Platform.MaxUserAddress.
func (*HVF) MaxUserAddress() hostarch.Addr {
	return maxUserAddress
}

// NewAddressSpace implements platform.Platform.NewAddressSpace.
func (p *HVF) NewAddressSpace(platform.AddressSpaceOptions) (platform.AddressSpace, error) {
	return newAddressSpace(p), nil
}

// NewContext implements platform.Platform.NewContext.
func (p *HVF) NewContext(pkgcontext.Context) platform.Context {
	c := &context{p: p}
	c.interrupt.Dst = c
	c.req.done = make(chan struct{}, 1)
	return c
}

// SeccompInfo implements platform.Platform.SeccompInfo. There is no seccomp
// on darwin.
func (*HVF) SeccompInfo() platform.SeccompInfo {
	return platform.StaticSeccompInfo{PlatformName: "hvf"}
}

// ConcurrencyCount implements platform.Platform.ConcurrencyCount.
func (p *HVF) ConcurrencyCount() int {
	return len(p.vcpus)
}

// signalInfo returns a SignalInfo for signal sig with code and addr.
func signalInfo(sig linux.Signal, code int32, addr uint64) linux.SignalInfo {
	info := linux.SignalInfo{Signo: int32(sig), Code: code}
	info.SetAddr(addr)
	return info
}

type constructor struct{}

// New implements platform.Constructor.New.
func (*constructor) New(platform.Options) (platform.Platform, error) {
	return New()
}

// PrecompiledSeccompInfo implements
// platform.Constructor.PrecompiledSeccompInfo.
func (*constructor) PrecompiledSeccompInfo() []platform.SeccompInfo {
	return nil
}

func init() {
	platform.Register("hvf", &constructor{})
}
