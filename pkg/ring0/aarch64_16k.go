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

package ring0

// Page table layout constants for 16K page size.
const (
	_PGD_PGT_BASE = 0x4000
	_PGD_PGT_SIZE = 0x4000
	_PUD_PGT_BASE = 0x8000
	_PUD_PGT_SIZE = 0x4000
	_PMD_PGT_BASE = 0xc000
	_PMD_PGT_SIZE = 0x10000
	_PTE_PGT_BASE = 0x1c000
	_PTE_PGT_SIZE = 0x4000
)
