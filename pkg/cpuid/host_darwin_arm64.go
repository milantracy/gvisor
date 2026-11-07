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

package cpuid

import (
	"golang.org/x/sys/unix"
	"gvisor.dev/gvisor/pkg/log"
)

// darwinHWCap1 maps HWCAP bits to the macOS sysctl that reports the feature.
// The application runs on the host CPU (in a Hypervisor.framework VM), so the
// host features are the application features.
var darwinHWCap1 = []struct {
	bit    uint64
	sysctl string
}{
	{HWCAP_FP, "hw.optional.floatingpoint"},
	{HWCAP_ASIMD, "hw.optional.arm.AdvSIMD"},
	{HWCAP_AES, "hw.optional.arm.FEAT_AES"},
	{HWCAP_PMULL, "hw.optional.arm.FEAT_PMULL"},
	{HWCAP_SHA1, "hw.optional.arm.FEAT_SHA1"},
	{HWCAP_SHA2, "hw.optional.arm.FEAT_SHA256"},
	{HWCAP_CRC32, "hw.optional.arm.FEAT_CRC32"},
	{HWCAP_ATOMICS, "hw.optional.arm.FEAT_LSE"},
	{HWCAP_FPHP, "hw.optional.arm.FEAT_FP16"},
	{HWCAP_ASIMDHP, "hw.optional.arm.FEAT_FP16"},
	{HWCAP_ASIMDRDM, "hw.optional.arm.FEAT_RDM"},
	{HWCAP_JSCVT, "hw.optional.arm.FEAT_JSCVT"},
	{HWCAP_FCMA, "hw.optional.arm.FEAT_FCMA"},
	{HWCAP_LRCPC, "hw.optional.arm.FEAT_LRCPC"},
	{HWCAP_DCPOP, "hw.optional.arm.FEAT_DPB"},
	{HWCAP_SHA3, "hw.optional.arm.FEAT_SHA3"},
	{HWCAP_ASIMDDP, "hw.optional.arm.FEAT_DotProd"},
	{HWCAP_SHA512, "hw.optional.arm.FEAT_SHA512"},
	{HWCAP_ASIMDFHM, "hw.optional.arm.FEAT_FHM"},
	{HWCAP_DIT, "hw.optional.arm.FEAT_DIT"},
	{HWCAP_USCAT, "hw.optional.arm.FEAT_LSE2"},
	{HWCAP_ILRCPC, "hw.optional.arm.FEAT_LRCPC2"},
	{HWCAP_FLAGM, "hw.optional.arm.FEAT_FlagM"},
}

// cpuImplApple is the MIDR_EL1 implementer code of Apple.
const cpuImplApple = 0x61

// initHost sets the HWCAPs from the macOS sysctls, and the CPU information
// that /proc/cpuinfo shows.
func initHost() {
	for _, f := range darwinHWCap1 {
		v, err := unix.SysctlUint32(f.sysctl)
		if err != nil {
			log.Warningf("cpuid: sysctl %s: %v", f.sysctl, err)
			continue
		}
		if v != 0 {
			hostFeatureSet.hwCap.hwCap1 |= f.bit
		}
	}
	hostFeatureSet.cpuImplHex = cpuImplApple
	hostFeatureSet.cpuArchDec = 8
	// Linux shows twice the generic timer frequency as BogoMIPS on arm64.
	if tb, err := unix.SysctlUint64("hw.tbfrequency"); err == nil {
		hostFeatureSet.cpuFreqMHz = float64(2*tb) / 1e6
	}
}
