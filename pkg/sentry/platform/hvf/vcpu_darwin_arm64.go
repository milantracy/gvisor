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
	"sync/atomic"

	"gvisor.dev/gvisor/pkg/abi/linux"
	"gvisor.dev/gvisor/pkg/atomicbitops"
	"gvisor.dev/gvisor/pkg/hostarch"
	"gvisor.dev/gvisor/pkg/hvf"
	"gvisor.dev/gvisor/pkg/sentry/arch"
	"gvisor.dev/gvisor/pkg/sentry/platform"
)

// vcpu is a vCPU and the pool thread that owns it.
type vcpu struct {
	p  *HVF
	v  *hvf.VCPU
	id uint64

	// curAS is the address space that the vCPU runs, or nil.
	curAS atomic.Pointer[addressSpace]
	// inGuest is true from just before hv_vcpu_run until it returns.
	inGuest atomicbitops.Bool
	// runs is incremented each time hv_vcpu_run returns.
	runs atomicbitops.Uint64

	// busy is 1 from when a Switch claims the vCPU until the vCPU thread
	// releases it. mbox is 1 while req has a request that the vCPU thread
	// has not taken. sleeping is 1 while the vCPU thread blocks on mbox.
	// They are accessed atomically.
	busy, mbox, sleeping uint32
	// req is the request in mbox.
	req *switchRequest

	// The following fields are used only by the pool thread.

	// ttbr0 is the value of TTBR0_EL1.
	ttbr0 uint64
	// lastASID and lastGen are the address space and generation that the
	// TLB of the vCPU is valid for.
	lastASID, lastGen uint64
}

// loop creates the vCPU on a locked thread, sends the result to errs, and
// then runs switch requests.
func (v *vcpu) loop(errs chan<- error) {
	runtime.LockOSThread()
	var err error
	v.v, err = hvf.NewVCPU()
	if err == nil {
		v.id = v.v.ID()
		err = v.init()
	}
	errs <- err
	if err != nil {
		return
	}
	for {
		r := v.next()
		r.c.running.Store(v)
		v.curAS.Store(r.as)
		r.at, r.err = v.switchTo(r.c, r.as, r.ac)
		v.curAS.Store(nil)
		r.c.running.Store(nil)
		// Free the vCPU first, so that the next Switch of the same task
		// can take it again.
		v.p.release(v)
		r.complete()
	}
}

// init sets the system registers that do not change.
func (v *vcpu) init() error {
	if hostarch.NumMemoryTypes != 3 {
		panic("additional memory types must be configured in MAIR")
	}
	mair := uint64(mairNormal)<<(8*hostarch.MemoryTypeWriteBack) |
		uint64(mairNormalNC)<<(8*hostarch.MemoryTypeWriteCombine) |
		uint64(mairDeviceNGnRnE)<<(8*hostarch.MemoryTypeUncached)
	regs := []struct {
		r   hvf.SysReg
		val uint64
	}{
		{hvf.SysRegMAIREL1, mair},
		{hvf.SysRegTCREL1, tcrEL1},
		{hvf.SysRegTTBR1EL1, v.p.kernel.TTBR1_EL1(false, 0)},
		{hvf.SysRegTTBR0EL1, 0},
		{hvf.SysRegVBAREL1, stubVA},
		{hvf.SysRegCPACREL1, cpacrEL1},
		{hvf.SysRegCNTKCTLEL1, cntkctlEL1},
		{hvf.SysRegCNTVCTLEL0, 0},
		{hvf.SysRegMDSCREL1, 0},
		{hvf.SysRegTPIDRROEL0, 0},
		{hvf.SysRegSPEL1, 0},
		// The MMU goes on last, when the tables are set.
		{hvf.SysRegSCTLREL1, sctlrEL1},
	}
	for _, r := range regs {
		if err := v.v.SetSysReg(r.r, r.val); err != nil {
			return fmt.Errorf("setting system register %#x: %w", r.r, err)
		}
	}
	if err := v.v.SetVTimerMask(true); err != nil {
		return fmt.Errorf("masking the vtimer: %w", err)
	}
	// Force a TLB flush on the first entry.
	v.lastASID = 0
	return nil
}

// must panics if err is not nil. Register access fails only for an invalid
// register or a vCPU on another thread, which are bugs.
func must(err error) {
	if err != nil {
		panic(fmt.Sprintf("hvf: vCPU register access: %v", err))
	}
}

func (v *vcpu) reg(r hvf.Reg) uint64 {
	val, err := v.v.Reg(r)
	must(err)
	return val
}

func (v *vcpu) sysReg(r hvf.SysReg) uint64 {
	val, err := v.v.SysReg(r)
	must(err)
	return val
}

// loadState loads the application state from ac into the vCPU, except PC
// and PSTATE.
func (v *vcpu) loadState(ac *arch.Context64) {
	regs := &ac.StateData().Regs
	for i, val := range regs.Regs {
		must(v.v.SetReg(hvf.RegX0+hvf.Reg(i), val))
	}
	must(v.v.SetSysReg(hvf.SysRegSPEL0, regs.Sp))
	must(v.v.SetSysReg(hvf.SysRegTPIDREL0, regs.TPIDR_EL0))
	fpsr, fpcr, q := fpState(*ac.FloatingPointData())
	must(v.v.SetReg(hvf.RegFPSR, *fpsr))
	must(v.v.SetReg(hvf.RegFPCR, *fpcr))
	for i := range q {
		must(v.v.SetSIMDFPReg(i, q[i]))
	}
}

// saveState saves the application state from the vCPU into ac, with PC pc
// and PSTATE pstate.
func (v *vcpu) saveState(ac *arch.Context64, pc, pstate uint64) {
	regs := &ac.StateData().Regs
	for i := range regs.Regs {
		regs.Regs[i] = v.reg(hvf.RegX0 + hvf.Reg(i))
	}
	regs.Sp = v.sysReg(hvf.SysRegSPEL0)
	regs.TPIDR_EL0 = v.sysReg(hvf.SysRegTPIDREL0)
	regs.Pc = pc
	regs.Pstate = pstate
	fpsr, fpcr, q := fpState(*ac.FloatingPointData())
	*fpsr = v.reg(hvf.RegFPSR)
	*fpcr = v.reg(hvf.RegFPCR)
	for i := range q {
		val, err := v.v.SIMDFPReg(i)
		must(err)
		q[i] = val
	}
}

// inVectors returns the vector of a PC in the vector table, or -1.
func inVectors(pc uint64) int {
	if pc >= stubVA && pc < stubVA+flushOffset {
		return int(pc-stubVA) / vectorSize
	}
	return -1
}

// switchTo runs the application state ac in as until an exception that the
// Sentry must handle.
func (v *vcpu) switchTo(c *context, as *addressSpace, ac *arch.Context64) (hostarch.AccessType, error) {
	v.loadState(ac)
	pc := ac.StateData().Regs.Pc
	pstate := ac.StateData().Regs.Pstate &^ (pstateModeMask | pstateDAIF)
	if ttbr0 := as.pt.TTBR0_EL1(false, 0); ttbr0 != v.ttbr0 {
		must(v.v.SetSysReg(hvf.SysRegTTBR0EL1, ttbr0))
		v.ttbr0 = ttbr0
	}
	for {
		if c.kicked.Swap(false) {
			v.saveState(ac, pc, pstate)
			return hostarch.NoAccess, platform.ErrContextInterrupt
		}
		v.inGuest.Store(true)
		gen := as.gen.Load()
		flush := v.lastASID != as.id || v.lastGen != gen
		if flush {
			must(v.v.SetSysReg(hvf.SysRegELREL1, pc))
			must(v.v.SetSysReg(hvf.SysRegSPSREL1, pstate))
			must(v.v.SetReg(hvf.RegPC, stubVA+flushOffset))
			must(v.v.SetReg(hvf.RegCPSR, pstateEL1h))
		} else {
			must(v.v.SetReg(hvf.RegPC, pc))
			must(v.v.SetReg(hvf.RegCPSR, pstate))
		}
		err := v.v.Run()
		v.inGuest.Store(false)
		v.runs.Add(1)
		if err != nil {
			return hostarch.NoAccess, fmt.Errorf("hv_vcpu_run: %w", err)
		}
		if flush {
			v.lastASID, v.lastGen = as.id, gen
		}

		e := v.v.Exit()
		switch e.Reason {
		case hvf.ExitCanceled, hvf.ExitVTimerActivated:
			pc, pstate = v.reg(hvf.RegPC), v.reg(hvf.RegCPSR)
			if pstate&pstateModeMask == 0 {
				// At EL0.
				continue
			}
			// At EL1: in a vector before its HVC, or in the flush stub
			// before its ERET.
			if vec := inVectors(pc); vec >= 0 {
				return v.vector(c, ac, vec)
			}
			pc, pstate = v.sysReg(hvf.SysRegELREL1), v.sysReg(hvf.SysRegSPSREL1)
			v.lastASID = 0
		case hvf.ExitException:
			switch e.EC() {
			case hvf.ECHVC64:
				return v.vector(c, ac, int(e.ISS()&0xffff)-hvcVectorBase)
			case hvf.ECDataAbortLower, hvf.ECInstAbortLower:
				// A stage-2 fault.
				ok, err := v.p.registry.Refault(e.PhysicalAddress)
				if err != nil {
					return hostarch.NoAccess, err
				}
				pc, pstate = v.reg(hvf.RegPC), v.reg(hvf.RegCPSR)
				if !ok {
					if pstate&pstateModeMask != 0 {
						panic(fmt.Sprintf("hvf: stage-2 fault at EL1: PC %#x, IPA %#x, syndrome %#x", pc, e.PhysicalAddress, e.Syndrome))
					}
					// The application touched memory whose host mapping
					// is gone.
					v.saveState(ac, pc, pstate)
					c.info = signalInfo(linux.SIGBUS, busAdrErr, uint64(hostarch.UntaggedUserAddr(hostarch.Addr(e.VirtualAddress))))
					return hostarch.NoAccess, platform.ErrContextSignal
				}
			default:
				return hostarch.NoAccess, fmt.Errorf("hvf: unexpected exit: syndrome %#x (EC %#x), VA %#x, IPA %#x", e.Syndrome, e.EC(), e.VirtualAddress, e.PhysicalAddress)
			}
		default:
			return hostarch.NoAccess, fmt.Errorf("hvf: unexpected exit reason %d", e.Reason)
		}
	}
}

// Signal codes, from <asm-generic/siginfo.h>.
const (
	segvMapErr = 1
	segvAccErr = 2
	busAdrAln  = 1
	busAdrErr  = 2
	busObjErr  = 3
	illIllOpc  = 1
)

// ESR_EL1 values.
const (
	esrECShift = 26
	esrECMask  = 0x3f
	esrFSCMask = 0x3f

	ecUnknown    = 0x00
	ecSVC64      = 0x15
	ecSysReg     = 0x18
	ecIABTLower  = 0x20
	ecPCAlign    = 0x22
	ecDABTLower  = 0x24
	ecSPAlign    = 0x26
	ecFPExc64    = 0x2c
	ecBRK64      = 0x3c
	ecSoftStep   = 0x32
	ecBreakpoint = 0x30
	ecWatchpoint = 0x34
)

// vector handles an exception from EL0 that the stub took at vector vec. The
// application state is in the vCPU, with PC and PSTATE in ELR_EL1 and
// SPSR_EL1.
func (v *vcpu) vector(c *context, ac *arch.Context64, vec int) (hostarch.AccessType, error) {
	esr := v.sysReg(hvf.SysRegESREL1)
	elr := v.sysReg(hvf.SysRegELREL1)
	far := v.sysReg(hvf.SysRegFAREL1)
	spsr := v.sysReg(hvf.SysRegSPSREL1)
	switch vec {
	case vectorEL0Sync:
	case vectorEL0IRQ, vectorEL0FIQ:
		v.saveState(ac, elr, spsr)
		return hostarch.NoAccess, platform.ErrContextInterrupt
	case vectorEL0SError:
		v.saveState(ac, elr, spsr)
		c.info = signalInfo(linux.SIGBUS, busObjErr, 0)
		return hostarch.NoAccess, platform.ErrContextSignal
	default:
		panic(fmt.Sprintf("hvf: guest exception at EL1: vector %d, ESR %#x, ELR %#x, FAR %#x, SPSR %#x", vec, esr, elr, far, spsr))
	}
	v.saveState(ac, elr, spsr)
	addr := uint64(hostarch.UntaggedUserAddr(hostarch.Addr(far)))
	switch ec := (esr >> esrECShift) & esrECMask; ec {
	case ecSVC64:
		return hostarch.NoAccess, nil
	case ecDABTLower, ecIABTLower:
		code := int32(segvAccErr)
		if fsc := esr & esrFSCMask; fsc >= 0x4 && fsc <= 0x7 {
			code = segvMapErr
		}
		c.info = signalInfo(linux.SIGSEGV, code, addr)
		return hostarch.ESRAccessType(esr), platform.ErrContextSignal
	case ecPCAlign, ecSPAlign:
		c.info = signalInfo(linux.SIGBUS, busAdrAln, addr)
	case ecFPExc64:
		c.info = signalInfo(linux.SIGFPE, 0, elr)
	case ecBRK64, ecBreakpoint, ecSoftStep, ecWatchpoint:
		c.info = signalInfo(linux.SIGTRAP, linux.TRAP_BRKPT, elr)
	default:
		// Including ecUnknown (an undefined instruction) and ecSysReg
		// (EL0 access to a system register).
		c.info = signalInfo(linux.SIGILL, illIllOpc, elr)
	}
	return hostarch.NoAccess, platform.ErrContextSignal
}
