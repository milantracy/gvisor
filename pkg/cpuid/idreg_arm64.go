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

//go:build arm64
// +build arm64

package cpuid

// SysReg identifies an AArch64 system register by the fields of its MRS
// encoding.
type SysReg struct {
	Op0, Op1, CRn, CRm, Op2 uint8
}

// ID registers that IDRegister models. The other registers of the ID space
// read as zero.
var (
	sysRegMIDR    = SysReg{3, 0, 0, 0, 0}
	sysRegMPIDR   = SysReg{3, 0, 0, 0, 5}
	sysRegREVIDR  = SysReg{3, 0, 0, 0, 6}
	sysRegPFR0    = SysReg{3, 0, 0, 4, 0}
	sysRegISAR0   = SysReg{3, 0, 0, 6, 0}
	sysRegISAR1   = SysReg{3, 0, 0, 6, 1}
	sysRegMMFR2   = SysReg{3, 0, 0, 7, 2}
	mpidrSafeVal  = uint64(1) << 31 // MPIDR_EL1.RES1; Linux SYS_MPIDR_SAFE_VAL.
	midrArchByIDs = uint64(0xf)     // MIDR_EL1.Architecture: "defined by the ID registers".
)

// idField is a 4-bit field of an ID register, and the value that the field
// has when all of the HWCAP1 bits in hwCap1 are allowed.
type idField struct {
	reg    SysReg
	shift  uint
	value  uint64
	hwCap1 uint64
}

// idFields lists the ID register fields that Linux uses to set the HWCAP1
// bits that AllowedHWCap1 can expose (arch/arm64/kernel/cpufeature.c,
// arm64_elf_hwcaps). A field that more than one HWCAP sets is listed once for
// each value, in increasing order; the last value whose HWCAPs are all
// allowed is used.
var idFields = []idField{
	{sysRegISAR0, 4, 1, HWCAP_AES},
	{sysRegISAR0, 4, 2, HWCAP_AES | HWCAP_PMULL},
	{sysRegISAR0, 8, 1, HWCAP_SHA1},
	{sysRegISAR0, 12, 1, HWCAP_SHA2},
	{sysRegISAR0, 12, 2, HWCAP_SHA2 | HWCAP_SHA512},
	{sysRegISAR0, 16, 1, HWCAP_CRC32},
	{sysRegISAR0, 20, 2, HWCAP_ATOMICS},
	{sysRegISAR0, 28, 1, HWCAP_ASIMDRDM},
	{sysRegISAR0, 32, 1, HWCAP_SHA3},
	{sysRegISAR0, 36, 1, HWCAP_SM3},
	{sysRegISAR0, 40, 1, HWCAP_SM4},
	{sysRegISAR0, 44, 1, HWCAP_ASIMDDP},
	{sysRegISAR0, 48, 1, HWCAP_ASIMDFHM},
	{sysRegISAR0, 52, 1, HWCAP_FLAGM},
	{sysRegISAR1, 0, 1, HWCAP_DCPOP},
	{sysRegISAR1, 12, 1, HWCAP_JSCVT},
	{sysRegISAR1, 16, 1, HWCAP_FCMA},
	{sysRegISAR1, 20, 1, HWCAP_LRCPC},
	{sysRegISAR1, 20, 2, HWCAP_LRCPC | HWCAP_ILRCPC},
	// EL0 and EL1: AArch64 only. Linux hides these fields, and shows
	// their safe value.
	{sysRegPFR0, 0, 1, 0},
	{sysRegPFR0, 4, 1, 0},
	// FP and AdvSIMD: 0xf is "not implemented", 0 is "implemented", 1 is
	// "implemented, with half precision".
	{sysRegPFR0, 16, 0xf, 0},
	{sysRegPFR0, 16, 0, HWCAP_FP},
	{sysRegPFR0, 16, 1, HWCAP_FP | HWCAP_FPHP},
	{sysRegPFR0, 20, 0xf, 0},
	{sysRegPFR0, 20, 0, HWCAP_ASIMD},
	{sysRegPFR0, 20, 1, HWCAP_ASIMD | HWCAP_ASIMDHP},
	{sysRegPFR0, 48, 1, HWCAP_DIT},
	{sysRegMMFR2, 32, 1, HWCAP_USCAT},
}

// IsIDRegister returns true if r is in the ID register space that Linux
// emulates for MRS instructions at EL0: op0 3, op1 0, CRn 0, and CRm 0 or 2
// to 7 (arch/arm64/kernel/cpufeature.c, is_emulated).
func IsIDRegister(r SysReg) bool {
	return r.Op0 == 3 && r.Op1 == 0 && r.CRn == 0 && (r.CRm == 0 || (r.CRm >= 2 && r.CRm <= 7))
}

// IDRegister returns the value that an MRS instruction at EL0 reads from the
// ID register r, as Linux emulates it. The value shows only the features of
// AllowedHWCap1, so that it agrees with the auxiliary vector. ok is false if
// Linux does not emulate a read of r; the instruction is then undefined.
func (fs FeatureSet) IDRegister(r SysReg) (val uint64, ok bool) {
	if !IsIDRegister(r) {
		return 0, false
	}
	switch r {
	case sysRegMIDR:
		return uint64(fs.CPUImplementer())<<24 |
			uint64(fs.CPUVariant()&0xf)<<20 |
			midrArchByIDs<<16 |
			uint64(fs.CPUPartnum()&0xfff)<<4 |
			uint64(fs.CPURevision()&0xf), true
	case sysRegMPIDR:
		return mpidrSafeVal, true
	case sysRegREVIDR:
		return 0, true
	}
	if r.CRm == 0 {
		// Linux emulates only MIDR_EL1, MPIDR_EL1 and REVIDR_EL1 in
		// this group.
		return 0, false
	}
	allowed := fs.AllowedHWCap1()
	for _, f := range idFields {
		if f.reg == r && allowed&f.hwCap1 == f.hwCap1 {
			val = val&^(0xf<<f.shift) | f.value<<f.shift
		}
	}
	return val, true
}
