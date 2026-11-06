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

// Shared setup for the Hypervisor.framework Phase 0 probes.
//
// Guest memory layout (identity mapped, VA == IPA):
//
//   GUEST_BASE + 0x0000  level-1 page table (4 KiB granule, T0SZ=32)
//   GUEST_BASE + 0x4000  vector table (VBAR_EL1)
//   GUEST_BASE + 0x8000  guest code
//   GUEST_BASE + GUEST_SIZE - 0x10  initial SP_EL1
//
// Every vector slot executes "hvc #(0x100 + slot)", so an unexpected guest
// exception exits to the host with an identifiable immediate.

#ifndef TOOLS_HVF_PROBE_PROBE_H_
#define TOOLS_HVF_PROBE_PROBE_H_

#include <Hypervisor/Hypervisor.h>
#include <pthread.h>
#include <stddef.h>
#include <stdint.h>

#define GUEST_BASE 0x10000000ULL
#define GUEST_SIZE (1ULL << 20)
#define GUEST_PT_OFF 0x0000
#define GUEST_VEC_OFF 0x4000
#define GUEST_CODE_OFF 0x8000

// ESR_ELx exception classes.
#define EC_HVC64 0x16
#define EC_SYSREG 0x18
#define EC_FP 0x07
#define EC_DABT_LOWER 0x24

#define ESR_EC(esr) (((esr) >> 26) & 0x3f)
#define ESR_ISS(esr) ((esr) & 0x1ffffff)

// HVC immediate used by the probes for a deliberate guest-to-host exit.
// Immediate 0 is avoided: HVF may consume it as an SMCCC call.
#define PROBE_HVC_IMM 1

#define CHECK(expr)                                                       \
  do {                                                                    \
    hv_return_t _r = (expr);                                              \
    if (_r != HV_SUCCESS) probe_die(#expr, _r, __FILE__, __LINE__);       \
  } while (0)

// Returns the HV_* name of an hv_return_t, or "0x..." if unknown.
const char* probe_strerror(hv_return_t r);

void probe_die(const char* what, hv_return_t r, const char* file, int line)
    __attribute__((noreturn));

// Runs fn on a new thread with QoS USER_INTERACTIVE and returns it.
pthread_t probe_spawn(void* (*fn)(void*), void* arg);

// Host memory backing the guest window.
extern uint8_t* guest_mem;

// Prints chip, OS version, and HVF limits. Call once at start of each probe.
void probe_print_env(const char* probe_name);

// Creates the VM, maps GUEST_SIZE of anonymous memory at GUEST_BASE, and
// writes the page table and vector table.
void probe_vm_init(void);

// Copies [start, end) into guest code memory at GUEST_CODE_OFF + off and
// returns the guest address.
uint64_t probe_load_code(const void* start, const void* end, size_t off);

// Programs every writable register of the calling thread's vCPU to a known
// state: EL1h with DAIF masked, MMU and caches on, FP/SIMD enabled, VTimer
// masked, PC = entry.
void probe_vcpu_reset(hv_vcpu_t vcpu, uint64_t entry);

// Pins the calling thread to the USER_INTERACTIVE QoS class so it runs on a
// performance core.
void probe_set_qos(void);

// Converts mach_absolute_time ticks to nanoseconds.
double probe_ticks_to_ns(uint64_t ticks);

// Sorts samples and prints min/p50/p90/p99/p99.9/max in nanoseconds.
void probe_print_percentiles(const char* label, uint64_t* ticks, size_t n);

// Fails unless the last exit is an HVC with the given immediate. Prints the
// syndrome and guest exception state on failure.
void probe_expect_hvc(hv_vcpu_t vcpu, const hv_vcpu_exit_t* ex,
                      uint16_t imm);

// Guest loops in guest_loops.S.
extern const uint8_t guest_clean[], guest_clean_end[];
extern const uint8_t guest_fpdirty[], guest_fpdirty_end[];
extern const uint8_t guest_spin[], guest_spin_end[];
extern const uint8_t guest_memop[], guest_memop_end[];

// Result of probe_guest_access.
enum access_result {
  ACCESS_OK,     // The access completed and the guest exited with HVC.
  ACCESS_FAULT,  // The guest exited with a data abort (EC 0x24).
  ACCESS_OTHER,  // Any other exit; *esr holds the syndrome.
};

// Runs one guest load (store == 0) or store at guest address addr using the
// guest_memop stub loaded at entry. A load stores the value in *out. *esr
// gets the exit syndrome and *ipa the faulting IPA, if not NULL.
enum access_result probe_guest_access(hv_vcpu_t vcpu, const hv_vcpu_exit_t* ex,
                                      uint64_t entry, int store, uint64_t addr,
                                      uint64_t val, uint64_t* out,
                                      uint64_t* esr, uint64_t* ipa);

// Returns the task's physical footprint (TASK_VM_INFO phys_footprint), in
// bytes.
uint64_t probe_phys_footprint(void);

// Resets the vCPU to run guest_spin at entry_spin and sets the constants it
// needs (d2 = 1.0, v4 = {1, 1}).
void probe_spin_reset(hv_vcpu_t vcpu, uint64_t entry_spin);

// Checks the guest_spin register invariants at the current PC. Returns 0 if
// they hold and stores x0 and the PC offset in the loop; otherwise prints the
// mismatch and returns 1.
int probe_check_spin(hv_vcpu_t vcpu, uint64_t entry_spin, uint64_t* x0_out,
                     uint64_t* off_out);

#endif  // TOOLS_HVF_PROBE_PROBE_H_
