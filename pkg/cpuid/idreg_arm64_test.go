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

import "testing"

func TestIDRegister(t *testing.T) {
	fs := FeatureSet{
		hwCap: hwCap{hwCap1: HWCAP_FP | HWCAP_ASIMD | HWCAP_FPHP | HWCAP_ASIMDHP |
			HWCAP_AES | HWCAP_PMULL | HWCAP_SHA2 | HWCAP_ATOMICS |
			HWCAP_LRCPC | HWCAP_USCAT |
			// Not allowed: must not show in the ID registers.
			HWCAP_SVE | HWCAP_PACA},
		cpuImplHex: 0x61,
		cpuVarHex:  0x2,
		cpuPartHex: 0x32,
		cpuRevDec:  0x1,
	}
	for _, tc := range []struct {
		name string
		reg  SysReg
		want uint64
		ok   bool
	}{
		{"MIDR_EL1", sysRegMIDR, 0x612f0321, true},
		{"MPIDR_EL1", sysRegMPIDR, 1 << 31, true},
		{"REVIDR_EL1", sysRegREVIDR, 0, true},
		{"AIDR-like CRm=0 register", SysReg{3, 0, 0, 0, 1}, 0, false},
		// AES=2, SHA2=1, Atomic=2.
		{"ID_AA64ISAR0_EL1", sysRegISAR0, 0x0000000000201020, true},
		// LRCPC=1.
		{"ID_AA64ISAR1_EL1", sysRegISAR1, 0x0000000000100000, true},
		// EL0=1, EL1=1, FP=1, AdvSIMD=1, SVE=0.
		{"ID_AA64PFR0_EL1", sysRegPFR0, 0x0000000000110011, true},
		// AT=1.
		{"ID_AA64MMFR2_EL1", sysRegMMFR2, 0x0000000100000000, true},
		{"ID_AA64ZFR0_EL1 (not modeled)", SysReg{3, 0, 0, 4, 4}, 0, true},
		{"CRm=1 (AArch32 ID registers)", SysReg{3, 0, 0, 1, 0}, 0, false},
		{"CRn=1", SysReg{3, 0, 1, 0, 0}, 0, false},
		{"op1=3 (CTR_EL0)", SysReg{3, 3, 0, 0, 1}, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := fs.IDRegister(tc.reg)
			if got != tc.want || ok != tc.ok {
				t.Errorf("IDRegister(%+v) = %#x, %t; want %#x, %t", tc.reg, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestIDRegisterNoFP(t *testing.T) {
	got, ok := FeatureSet{}.IDRegister(sysRegPFR0)
	// EL0=1, EL1=1, FP=0xf, AdvSIMD=0xf.
	if want := uint64(0x0000000000ff0011); got != want || !ok {
		t.Errorf("IDRegister(ID_AA64PFR0_EL1) = %#x, %t; want %#x, true", got, ok, want)
	}
}
