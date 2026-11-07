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

// Guest EL1 code. The stub page has the exception vector table at offset 0
// and the flush stub at offset flushOffset.
const (
	// vectorCount is the number of exception vectors.
	vectorCount = 16
	// vectorSize is the size of each vector slot.
	vectorSize = 0x80
	// hvcVectorBase is the HVC immediate of vector 0.
	hvcVectorBase = 0x100
	// flushOffset is the offset of the flush stub.
	flushOffset = vectorCount * vectorSize

	// vectorEL0Sync is the vector of synchronous exceptions from EL0
	// (AArch64).
	vectorEL0Sync = 8
	// vectorEL0IRQ, vectorEL0FIQ and vectorEL0SError are the other EL0
	// vectors.
	vectorEL0IRQ    = 9
	vectorEL0FIQ    = 10
	vectorEL0SError = 11
)

// Instructions.
const (
	insnHVC           = 0xd4000002 // hvc #0; the immediate is bits [20:5].
	insnTLBIVMALLE1IS = 0xd508831f
	insnICIALLUIS     = 0xd508711f
	insnDSBISH        = 0xd5033b9f
	insnISB           = 0xd5033fdf
	insnERET          = 0xd69f03e0
)

// stubCode returns the instructions of the stub page.
func stubCode() []uint32 {
	code := make([]uint32, (flushOffset+6*4)/4)
	for n := 0; n < vectorCount; n++ {
		code[n*vectorSize/4] = insnHVC | uint32(hvcVectorBase+n)<<5
	}
	copy(code[flushOffset/4:], []uint32{
		// The host sets ELR_EL1 and SPSR_EL1 to the application state.
		insnTLBIVMALLE1IS,
		insnICIALLUIS,
		insnDSBISH,
		insnISB,
		insnERET,
	})
	return code
}

// Processor state.
const (
	// pstateEL1h is EL1 with SP_EL1 and DAIF masked.
	pstateEL1h = 0x3c5
	// pstateModeMask is the mode (M[4:0]) of PSTATE.
	pstateModeMask = 0x1f
	// pstateDAIF is the interrupt mask bits of PSTATE.
	pstateDAIF = 0xf << 6
)

// System register values.
const (
	// sctlrEL1: MMU, caches, EL0 access to DC ZVA, CTR_EL0 and cache
	// maintenance, no trap of WFI/WFE, EL0 stack alignment check, and the
	// RES1 bits of ARMv8.0.
	sctlrEL1 = 0x30d00800 |
		1<<0 | // M
		1<<2 | // C
		1<<4 | // SA0
		1<<12 | // I
		1<<14 | // DZE
		1<<15 | // UCT
		1<<16 | // nTWI
		1<<18 | // nTWE
		1<<26 // UCI

	// tcrEL1: 48-bit VA in both halves, 16K granules, write-back
	// inner-shareable table walks, 40-bit IPA, 8-bit ASID, top byte
	// ignored for TTBR0 (as on Linux).
	tcrEL1 = 16<<0 | // T0SZ
		1<<8 | 1<<10 | 3<<12 | // IRGN0, ORGN0, SH0
		2<<14 | // TG0 = 16K
		16<<16 | // T1SZ
		1<<24 | 1<<26 | 3<<28 | // IRGN1, ORGN1, SH1
		1<<30 | // TG1 = 16K
		2<<32 | // IPS = 40 bits
		1<<37 // TBI0

	// cpacrEL1 does not trap FP and SIMD.
	cpacrEL1 = 3 << 20

	// cntkctlEL1 lets EL0 read the virtual counter (vDSO).
	cntkctlEL1 = 1 << 1
)

// MAIR attributes, by hostarch.MemoryType.
const (
	mairNormal       = 0xff
	mairNormalNC     = 0x44
	mairDeviceNGnRnE = 0x00
)
