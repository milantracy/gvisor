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

// Package hvf implements a platform on Apple's Hypervisor.framework for
// darwin/arm64.
//
// The Sentry runs on the host. The guest has the application at EL0 and a
// small EL1 stub: each exception vector executes "hvc #(0x100+n)", so every
// application exception, including a system call, exits to the host with the
// application registers still in the vCPU. The host reads ESR_EL1, ELR_EL1
// and FAR_EL1 to decode the exception, and enters EL0 directly to resume.
//
// vCPUs belong to a fixed pool of threads that are locked to their vCPU.
// Context.Switch hands the switch to a pool thread.
//
// Application memory is mapped into the IPA space on demand by an
// ipa.Registry. Stage-1 page tables are in registry-mapped memory. A change
// to the page tables of an address space bumps its generation and stops
// vCPUs that run it; a vCPU enters through a stub that invalidates the TLB
// and the instruction cache when the generation changed since it last ran
// the address space.
//
// The package has code only on darwin/arm64.
package hvf
