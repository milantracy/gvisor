// Copyright 2020 The gVisor Authors.
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

import "testing"

func TestDarwinHostHWCap(t *testing.T) {
	// All Apple Silicon CPUs have these.
	want := uint64(HWCAP_FP | HWCAP_ASIMD | HWCAP_ATOMICS | HWCAP_CRC32)
	if got := HostFeatureSet().hwCap.hwCap1; got&want != want {
		t.Errorf("hwCap1 = %#x, want bits %#x set", got, want)
	}
	if got := HostFeatureSet().CPUImplementer(); got != cpuImplApple {
		t.Errorf("implementer = %#x, want %#x", got, cpuImplApple)
	}
}
