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

import "fmt"

// Error is an hv_return_t that is not HV_SUCCESS.
type Error uint32

// Errors from <Hypervisor/hv_error.h>.
const (
	ErrError        Error = 0xfae94001
	ErrBusy         Error = 0xfae94002
	ErrBadArgument  Error = 0xfae94003
	ErrNoResources  Error = 0xfae94005
	ErrNoDevice     Error = 0xfae94006
	ErrDenied       Error = 0xfae94007
	ErrUnsupported  Error = 0xfae9400f
	ErrIllegalGuest Error = 0xfae94004
)

var errorNames = map[Error]string{
	ErrError:        "HV_ERROR",
	ErrBusy:         "HV_BUSY",
	ErrBadArgument:  "HV_BAD_ARGUMENT",
	ErrIllegalGuest: "HV_ILLEGAL_GUEST_STATE",
	ErrNoResources:  "HV_NO_RESOURCES",
	ErrNoDevice:     "HV_NO_DEVICE",
	ErrDenied:       "HV_DENIED",
	ErrUnsupported:  "HV_UNSUPPORTED",
}

// Error implements error.Error.
func (e Error) Error() string {
	if s, ok := errorNames[e]; ok {
		return s
	}
	return fmt.Sprintf("hv_return_t %#x", uint32(e))
}

// result converts an hv_return_t to an error.
func result(r uintptr) error {
	if uint32(r) == 0 {
		return nil
	}
	return Error(uint32(r))
}

// MemoryFlags are hv_memory_flags_t.
type MemoryFlags uint64

// Memory permissions.
const (
	MemoryRead  MemoryFlags = 1 << 0
	MemoryWrite MemoryFlags = 1 << 1
	MemoryExec  MemoryFlags = 1 << 2
)

// Reg is an hv_reg_t: a general purpose register or PC, FPCR, FPSR or CPSR.
type Reg uint32

// Registers. X0 to X30 are RegX0 + n.
const (
	RegX0   Reg = 0
	RegFP   Reg = 29
	RegLR   Reg = 30
	RegPC   Reg = 31
	RegFPCR Reg = 32
	RegFPSR Reg = 33
	RegCPSR Reg = 34
)

// SysReg is an hv_sys_reg_t.
type SysReg uint16

// System registers from <Hypervisor/hv_vcpu_types.h>.
const (
	SysRegMDSCREL1      SysReg = 0x8012
	SysRegSCTLREL1      SysReg = 0xc080
	SysRegCPACREL1      SysReg = 0xc082
	SysRegTTBR0EL1      SysReg = 0xc100
	SysRegTTBR1EL1      SysReg = 0xc101
	SysRegTCREL1        SysReg = 0xc102
	SysRegSPSREL1       SysReg = 0xc200
	SysRegELREL1        SysReg = 0xc201
	SysRegSPEL0         SysReg = 0xc208
	SysRegAFSR0EL1      SysReg = 0xc288
	SysRegESREL1        SysReg = 0xc290
	SysRegFAREL1        SysReg = 0xc300
	SysRegPAREL1        SysReg = 0xc3a0
	SysRegMAIREL1       SysReg = 0xc510
	SysRegAMAIREL1      SysReg = 0xc518
	SysRegVBAREL1       SysReg = 0xc600
	SysRegCONTEXTIDREL1 SysReg = 0xc681
	SysRegTPIDREL1      SysReg = 0xc684
	SysRegCNTKCTLEL1    SysReg = 0xc708
	SysRegTPIDREL0      SysReg = 0xde82
	SysRegTPIDRROEL0    SysReg = 0xde83
	SysRegCNTVCTLEL0    SysReg = 0xdf19
	SysRegCNTVCVALEL0   SysReg = 0xdf1a
	SysRegSPEL1         SysReg = 0xe208
)

// ExitReason is an hv_exit_reason_t.
type ExitReason uint32

// Exit reasons.
const (
	ExitCanceled        ExitReason = 0
	ExitException       ExitReason = 1
	ExitVTimerActivated ExitReason = 2
	ExitUnknown         ExitReason = 3
)

// Exit is hv_vcpu_exit_t. HVF writes it when Run returns.
type Exit struct {
	Reason ExitReason
	_      uint32
	// Syndrome is ESR_EL2 for an exception exit.
	Syndrome uint64
	// VirtualAddress is FAR_EL2 for an exception exit.
	VirtualAddress uint64
	// PhysicalAddress is the faulting IPA for a stage-2 fault.
	PhysicalAddress uint64
}

// Exception classes of ESR_ELx.
const (
	ECHVC64           = 0x16
	ECSysReg          = 0x18
	ECInstAbortLower  = 0x20
	ECDataAbortLower  = 0x24
	ECDataAbortSameEL = 0x25
)

// EC returns the exception class of the syndrome.
func (e *Exit) EC() uint32 {
	return uint32(e.Syndrome>>26) & 0x3f
}

// ISS returns the instruction specific syndrome.
func (e *Exit) ISS() uint32 {
	return uint32(e.Syndrome) & 0x1ffffff
}

// MaxIPASize returns the largest IPA size in bits that a VM can have.
func MaxIPASize() (uint32, error) {
	return maxIPASize()
}

// CreateVM creates the VM of the process with an IPA space of ipaBits bits.
// If ipaBits is 0, the VM has the default size.
func CreateVM(ipaBits uint32) error {
	config := vmConfigCreate()
	if config == 0 {
		return ErrNoResources
	}
	if ipaBits != 0 {
		if err := vmConfigSetIPASize(config, ipaBits); err != nil {
			return err
		}
	}
	return vmCreate(config)
}

// DestroyVM destroys the VM of the process. All vCPUs must be destroyed
// first.
func DestroyVM() error {
	return vmDestroy()
}

// Map maps size bytes of host memory at host into the VM at ipa. host, ipa
// and size must be aligned to the host page size (16 KiB), and the host range
// must be mapped.
func Map(host uintptr, ipa, size uint64, flags MemoryFlags) error {
	return vmMap(host, ipa, size, flags)
}

// Unmap removes the mapping of [ipa, ipa+size).
func Unmap(ipa, size uint64) error {
	return vmUnmap(ipa, size)
}

// Protect changes the permissions of [ipa, ipa+size).
func Protect(ipa, size uint64, flags MemoryFlags) error {
	return vmProtect(ipa, size, flags)
}

// VCPU is a vCPU. Its methods, except ID, must be called on the thread that
// created it.
type VCPU struct {
	id   uint64
	exit *Exit
}

// NewVCPU creates a vCPU that belongs to the current thread. The caller must
// have locked the goroutine to the thread.
func NewVCPU() (*VCPU, error) {
	v := &VCPU{}
	if err := vcpuCreate(v); err != nil {
		return nil, err
	}
	return v, nil
}

// ID returns the hv_vcpu_t.
func (v *VCPU) ID() uint64 {
	return v.id
}

// Destroy destroys the vCPU.
func (v *VCPU) Destroy() error {
	return vcpuDestroy(v.id)
}

// Run runs the vCPU until the next exit, then returns. Exit describes the
// exit. A signal to the thread does not end Run; ExitVCPUs does.
func (v *VCPU) Run() error {
	return vcpuRun(v.id)
}

// Exit returns the exit information of the last Run. It is valid until the
// next Run.
func (v *VCPU) Exit() *Exit {
	return v.exit
}

// Reg returns the value of r.
func (v *VCPU) Reg(r Reg) (uint64, error) {
	return vcpuGetReg(v.id, r)
}

// SetReg sets r to val.
func (v *VCPU) SetReg(r Reg, val uint64) error {
	return vcpuSetReg(v.id, r, val)
}

// SysReg returns the value of r.
func (v *VCPU) SysReg(r SysReg) (uint64, error) {
	return vcpuGetSysReg(v.id, r)
}

// SetSysReg sets r to val.
func (v *VCPU) SetSysReg(r SysReg, val uint64) error {
	return vcpuSetSysReg(v.id, r, val)
}

// SIMDFPReg returns Qn.
func (v *VCPU) SIMDFPReg(n int) ([16]byte, error) {
	return vcpuGetSIMDFPReg(v.id, n)
}

// SetSIMDFPReg sets Qn to val.
func (v *VCPU) SetSIMDFPReg(n int, val [16]byte) error {
	return vcpuSetSIMDFPReg(v.id, n, &val)
}

// SetVTimerMask masks or unmasks the virtual timer of the vCPU.
func (v *VCPU) SetVTimerMask(masked bool) error {
	return vcpuSetVTimerMask(v.id, masked)
}

// SetTrapDebugExceptions sets whether debug exceptions exit to the host.
func (v *VCPU) SetTrapDebugExceptions(trap bool) error {
	return vcpuSetTrapDebugExceptions(v.id, trap)
}

// ExitVCPUs makes each of vcpus exit from Run with ExitCanceled. It can be
// called from any thread.
func ExitVCPUs(ids ...uint64) error {
	if len(ids) == 0 {
		return nil
	}
	return vcpusExit(ids)
}
