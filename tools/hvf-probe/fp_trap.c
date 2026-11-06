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

// fp_trap: Phase 0 probe 9. Cost of a lazy-FP trap. With CPACR_EL1.FPEN = 0
// a guest FP instruction traps to the guest EL1 vector (not to the host), so
// a host-side FP switch needs the EL1 stub to forward the trap. Variants,
// each one iteration = "set CPACR, FP instruction, hvc #1":
//
//   no trap        FPEN = 0b11; no trap. Baseline.
//   in guest       The EL1 vector enables FP and returns; no host exit.
//   forward        The EL1 vector does "hvc #2; eret"; the host writes
//                  CPACR_EL1 and resumes.
//   forward+FP     As forward, and the host also writes Q0-Q31 (restoring a
//                  task's FP state on first use).
//   host does all  The default vector exits with "hvc #0x104"; the host reads
//                  ESR/ELR/SPSR_EL1, writes CPACR_EL1, PC and CPSR.
//
// Also the cost of reading and writing all 32 Q registers through HVF.

#include <mach/mach_time.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "probe.h"

extern const uint8_t guest_fp_notrap[], guest_fp_notrap_end[];
extern const uint8_t guest_fp_trap[], guest_fp_trap_end[];
extern const uint8_t guest_vectors_fp_inguest[], guest_vectors_fp_inguest_end[];
extern const uint8_t guest_vectors_fp_forward[], guest_vectors_fp_forward_end[];

#define ITERS 200000
#define TRIALS 5
#define FPEN (3ULL << 20)

enum host_mode { HOST_NONE, HOST_FORWARD, HOST_FORWARD_FP, HOST_ALL };

static hv_vcpu_t vcpu;
static hv_vcpu_exit_t* vexit;
static uint64_t traps;

static void restore_fp(void) {
  hv_simd_fp_uchar16_t q;
  memset(&q, 0x11, sizeof(q));
  for (int i = 0; i < 32; i++) {
    CHECK(hv_vcpu_set_simd_fp_reg(vcpu, (hv_simd_fp_reg_t)(HV_SIMD_FP_REG_Q0 + i),
                                  q));
  }
}

// Runs until the guest exits with "hvc #1", handling FP trap exits.
static inline void iteration(enum host_mode mode) {
  for (;;) {
    CHECK(hv_vcpu_run(vcpu));
    uint64_t esr = vexit->exception.syndrome;
    if (vexit->reason != HV_EXIT_REASON_EXCEPTION || ESR_EC(esr) != EC_HVC64) {
      probe_expect_hvc(vcpu, vexit, 1);  // Prints and exits.
    }
    uint16_t imm = esr & 0xffff;
    if (imm == 1) return;
    traps++;
    if (imm == 2 && (mode == HOST_FORWARD || mode == HOST_FORWARD_FP)) {
      CHECK(hv_vcpu_set_sys_reg(vcpu, HV_SYS_REG_CPACR_EL1, FPEN));
      if (mode == HOST_FORWARD_FP) restore_fp();
      continue;
    }
    if (imm == 0x104 && mode == HOST_ALL) {
      uint64_t esr1, elr, spsr;
      CHECK(hv_vcpu_get_sys_reg(vcpu, HV_SYS_REG_ESR_EL1, &esr1));
      CHECK(hv_vcpu_get_sys_reg(vcpu, HV_SYS_REG_ELR_EL1, &elr));
      CHECK(hv_vcpu_get_sys_reg(vcpu, HV_SYS_REG_SPSR_EL1, &spsr));
      if (ESR_EC(esr1) != EC_FP) {
        fprintf(stderr, "vector 4 with ESR_EL1 0x%llx\n", esr1);
        exit(1);
      }
      CHECK(hv_vcpu_set_sys_reg(vcpu, HV_SYS_REG_CPACR_EL1, FPEN));
      CHECK(hv_vcpu_set_reg(vcpu, HV_REG_PC, elr));
      CHECK(hv_vcpu_set_reg(vcpu, HV_REG_CPSR, spsr));
      continue;
    }
    probe_expect_hvc(vcpu, vexit, 1);  // Unexpected immediate.
  }
}

static int cmp_double(const void* a, const void* b) {
  double x = *(const double*)a, y = *(const double*)b;
  return (x > y) - (x < y);
}

static double measure(const char* label, uint64_t entry, uint64_t vbar,
                      enum host_mode mode, double base) {
  probe_vcpu_reset(vcpu, entry);
  if (vbar) CHECK(hv_vcpu_set_sys_reg(vcpu, HV_SYS_REG_VBAR_EL1, vbar));
  for (int i = 0; i < ITERS / 10; i++) iteration(mode);
  double t[TRIALS];
  traps = 0;
  for (int k = 0; k < TRIALS; k++) {
    uint64_t t0 = mach_absolute_time();
    for (int i = 0; i < ITERS; i++) iteration(mode);
    t[k] = probe_ticks_to_ns(mach_absolute_time() - t0) / ITERS;
  }
  qsort(t, TRIALS, sizeof(double), cmp_double);
  printf("%-16s %7.1f ns/iteration (median of %d x %d), +%6.1f ns, host "
         "trap exits per iteration %.2f\n",
         label, t[TRIALS / 2], TRIALS, ITERS, base ? t[TRIALS / 2] - base : 0,
         (double)traps / (TRIALS * ITERS));
  return t[TRIALS / 2];
}

int main(void) {
  probe_set_qos();
  probe_print_env("fp_trap");
  probe_vm_init();
  uint64_t notrap = probe_load_code(guest_fp_notrap, guest_fp_notrap_end, 0);
  uint64_t trap = probe_load_code(guest_fp_trap, guest_fp_trap_end, 0x100);
  // Vector tables need 2 KiB alignment.
  uint64_t vec_inguest = probe_load_code(
      guest_vectors_fp_inguest, guest_vectors_fp_inguest_end, 0x2000);
  uint64_t vec_forward = probe_load_code(
      guest_vectors_fp_forward, guest_vectors_fp_forward_end, 0x2800);
  printf("# vector table sizes: %td, %td bytes\n",
         guest_vectors_fp_inguest_end - guest_vectors_fp_inguest,
         guest_vectors_fp_forward_end - guest_vectors_fp_forward);
  CHECK(hv_vcpu_create(&vcpu, &vexit, NULL));

  // Warm up the CPU clock.
  probe_vcpu_reset(vcpu, notrap);
  uint64_t t0 = mach_absolute_time();
  while (probe_ticks_to_ns(mach_absolute_time() - t0) < 300e6) {
    iteration(HOST_NONE);
  }

  printf("\n## FP trap variants\n");
  double base = measure("no trap", notrap, 0, HOST_NONE, 0);
  measure("in guest", trap, vec_inguest, HOST_NONE, base);
  measure("forward", trap, vec_forward, HOST_FORWARD, base);
  measure("forward+FP", trap, vec_forward, HOST_FORWARD_FP, base);
  measure("host does all", trap, 0, HOST_ALL, base);

  printf("\n## FP state access through HVF\n");
  hv_simd_fp_uchar16_t q[32];
  t0 = mach_absolute_time();
  for (int i = 0; i < ITERS; i++) {
    for (int r = 0; r < 32; r++) {
      CHECK(hv_vcpu_get_simd_fp_reg(
          vcpu, (hv_simd_fp_reg_t)(HV_SIMD_FP_REG_Q0 + r), &q[r]));
    }
  }
  printf("get Q0-Q31: %.1f ns\n",
         probe_ticks_to_ns(mach_absolute_time() - t0) / ITERS);
  t0 = mach_absolute_time();
  for (int i = 0; i < ITERS; i++) {
    for (int r = 0; r < 32; r++) {
      CHECK(hv_vcpu_set_simd_fp_reg(
          vcpu, (hv_simd_fp_reg_t)(HV_SIMD_FP_REG_Q0 + r), q[r]));
    }
  }
  printf("set Q0-Q31: %.1f ns\n",
         probe_ticks_to_ns(mach_absolute_time() - t0) / ITERS);

  CHECK(hv_vcpu_destroy(vcpu));
  CHECK(hv_vm_destroy());
  return 0;
}
