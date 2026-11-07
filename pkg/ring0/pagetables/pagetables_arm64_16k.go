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

//go:build arm64 && pagesize_16k

package pagetables

// Page table constants for 16K page size (4-level page tables).
//
// Each page table page has 2048 entries (16K / 8 bytes), so each level uses
// 11 bits, except the top level, which uses 1 bit for a 48-bit VA:
//
//	[47]    - PGD index (2 entries)
//	[46:36] - PUD index
//	[35:25] - PMD index
//	[24:14] - PTE index
//	[13:0]  - Page offset
//
// The 16K granule has no block descriptors at level 1 (PUD) without
// FEAT_LPA2, so the walker uses PMD blocks (32M) and pages only.
const (
	pteShift = 14
	pmdShift = 25
	pudShift = 36
	pgdShift = 47

	pteMask = 0x7ff << pteShift
	pmdMask = 0x7ff << pmdShift
	pudMask = 0x7ff << pudShift
	pgdMask = 0x1 << pgdShift

	pteSize = 1 << pteShift
	pmdSize = 1 << pmdShift
	pudSize = 1 << pudShift
	pgdSize = 1 << pgdShift

	entriesPerPage = 2048

	// pudSect is true if PUD entries can be blocks.
	pudSect = false
)

// PTEs is a collection of entries.
type PTEs [entriesPerPage]PTE
