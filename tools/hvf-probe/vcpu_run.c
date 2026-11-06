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

// vcpu_run: Phase 0 probe 1. Measures the round trip
// host hv_vcpu_run -> guest EL1 "hvc #1" -> host, in these variants:
//
//   clean         guest does not use FP/SIMD; host does not touch FP/SIMD.
//   guest-fp      guest writes d0/d1 before every exit.
//   guest+host-fp guest writes d0/d1; host writes v0-v31 between runs.
//   syscall-shape guest clean; host reads x0-x8 and writes x0 per exit, as a
//                 host-side Sentry would for a forwarded syscall.
//
// It also reports the PC after an HVC exit, the cost of one
// hv_vcpu_get_reg call, and a host getppid() for scale.

#include <mach/mach_time.h>
#include <stdio.h>
#include <stdlib.h>
#include <unistd.h>

#include "probe.h"

extern const uint8_t guest_clean[], guest_clean_end[];
extern const uint8_t guest_fpdirty[], guest_fpdirty_end[];

#define TRIALS 5
#define ITERS 1000000
#define SAMPLES 200000

enum variant { CLEAN, GUEST_FP, BOTH_FP, SYSCALL_SHAPE, NVARIANTS };
static const char* variant_name[] = {"clean", "guest-fp", "guest+host-fp",
                                     "syscall-shape"};

static hv_vcpu_t vcpu;
static hv_vcpu_exit_t* vexit;
static uint64_t entry_clean, entry_fpdirty;

static inline void host_dirty_fp(void) {
  __asm__ volatile(
      "movi v0.16b, #1\n movi v1.16b, #2\n movi v2.16b, #3\n movi v3.16b, #4\n"
      "movi v4.16b, #5\n movi v5.16b, #6\n movi v6.16b, #7\n movi v7.16b, #8\n"
      "movi v8.16b, #9\n movi v9.16b, #10\n movi v10.16b, #11\n"
      "movi v11.16b, #12\n movi v12.16b, #13\n movi v13.16b, #14\n"
      "movi v14.16b, #15\n movi v15.16b, #16\n movi v16.16b, #17\n"
      "movi v17.16b, #18\n movi v18.16b, #19\n movi v19.16b, #20\n"
      "movi v20.16b, #21\n movi v21.16b, #22\n movi v22.16b, #23\n"
      "movi v23.16b, #24\n movi v24.16b, #25\n movi v25.16b, #26\n"
      "movi v26.16b, #27\n movi v27.16b, #28\n movi v28.16b, #29\n"
      "movi v29.16b, #30\n movi v30.16b, #31\n movi v31.16b, #32\n" ::
          : "v0", "v1", "v2", "v3", "v4", "v5", "v6", "v7", "v8", "v9", "v10",
            "v11", "v12", "v13", "v14", "v15", "v16", "v17", "v18", "v19",
            "v20", "v21", "v22", "v23", "v24", "v25", "v26", "v27", "v28",
            "v29", "v30", "v31");
}

// One guest round trip plus the host work for the variant.
static inline __attribute__((always_inline)) void round_trip(enum variant v) {
  if (v == BOTH_FP) host_dirty_fp();
  CHECK(hv_vcpu_run(vcpu));
  if (__builtin_expect(vexit->reason != HV_EXIT_REASON_EXCEPTION ||
                           ESR_EC(vexit->exception.syndrome) != EC_HVC64,
                       0)) {
    probe_expect_hvc(vcpu, vexit, PROBE_HVC_IMM);
  }
  if (v == SYSCALL_SHAPE) {
    uint64_t r[9];
    for (int i = 0; i < 9; i++) {
      CHECK(hv_vcpu_get_reg(vcpu, (hv_reg_t)(HV_REG_X0 + i), &r[i]));
    }
    CHECK(hv_vcpu_set_reg(vcpu, HV_REG_X0, r[8] + 1));
  }
}

static void reset_for(enum variant v) {
  probe_vcpu_reset(vcpu,
                   (v == GUEST_FP || v == BOTH_FP) ? entry_fpdirty : entry_clean);
}

static double run_trial(enum variant v) {
  uint64_t t0 = mach_absolute_time();
  for (int i = 0; i < ITERS; i++) round_trip(v);
  uint64_t t1 = mach_absolute_time();
  return probe_ticks_to_ns(t1 - t0) / ITERS;
}

static int cmp_double(const void* a, const void* b) {
  double x = *(const double*)a, y = *(const double*)b;
  return (x > y) - (x < y);
}

static void measure(enum variant v, uint64_t* samples) {
  reset_for(v);
  for (int i = 0; i < 100000; i++) round_trip(v);  // Warm up.

  double means[TRIALS];
  for (int t = 0; t < TRIALS; t++) means[t] = run_trial(v);
  printf("%-16s mean ns/round trip over %d x %d:", variant_name[v], TRIALS,
         ITERS);
  for (int t = 0; t < TRIALS; t++) printf(" %.1f", means[t]);
  qsort(means, TRIALS, sizeof(double), cmp_double);
  printf("  | median %.1f\n", means[TRIALS / 2]);

  for (int i = 0; i < SAMPLES; i++) {
    uint64_t a = mach_absolute_time();
    round_trip(v);
    samples[i] = mach_absolute_time() - a;
  }
  probe_print_percentiles(variant_name[v], samples, SAMPLES);
}

int main(void) {
  probe_set_qos();
  probe_print_env("vcpu_run");
  probe_vm_init();
  entry_clean = probe_load_code(guest_clean, guest_clean_end, 0);
  entry_fpdirty = probe_load_code(guest_fpdirty, guest_fpdirty_end, 0x100);

  CHECK(hv_vcpu_create(&vcpu, &vexit, NULL));

  // Where does PC point after an HVC exit?
  reset_for(CLEAN);
  CHECK(hv_vcpu_run(vcpu));
  probe_expect_hvc(vcpu, vexit, PROBE_HVC_IMM);
  uint64_t pc;
  CHECK(hv_vcpu_get_reg(vcpu, HV_REG_PC, &pc));
  printf("PC after hvc exit: entry%+lld (syndrome 0x%llx)\n",
         (long long)(pc - entry_clean), vexit->exception.syndrome);

  uint64_t* samples = malloc(SAMPLES * sizeof(uint64_t));
  for (int v = 0; v < NVARIANTS; v++) measure((enum variant)v, samples);

  // Guest FP state survives exits: after BOTH_FP the host clobbered v0-v31
  // every iteration, so the guest d1 accumulator must still be the
  // guest's own running sum.
  reset_for(BOTH_FP);
  for (int i = 0; i < 1000; i++) round_trip(BOTH_FP);
  hv_simd_fp_uchar16_t q1;
  CHECK(hv_vcpu_get_simd_fp_reg(vcpu, HV_SIMD_FP_REG_Q1, &q1));
  double d1;
  __builtin_memcpy(&d1, &q1, sizeof(d1));
  printf("guest d1 after 1000 guest+host-fp iterations: %.0f (expect %.0f)\n",
         d1, 1000.0 * 1001 / 2);

  // Cost of one hv_vcpu_get_reg.
  uint64_t x, t0 = mach_absolute_time();
  for (int i = 0; i < ITERS; i++) CHECK(hv_vcpu_get_reg(vcpu, HV_REG_X0, &x));
  printf("hv_vcpu_get_reg: %.1f ns/call\n",
         probe_ticks_to_ns(mach_absolute_time() - t0) / ITERS);
  t0 = mach_absolute_time();
  for (int i = 0; i < ITERS; i++) CHECK(hv_vcpu_set_reg(vcpu, HV_REG_X0, i));
  printf("hv_vcpu_set_reg: %.1f ns/call\n",
         probe_ticks_to_ns(mach_absolute_time() - t0) / ITERS);

  t0 = mach_absolute_time();
  for (int i = 0; i < ITERS; i++) (void)getppid();
  printf("host getppid(): %.1f ns/call\n",
         probe_ticks_to_ns(mach_absolute_time() - t0) / ITERS);

  CHECK(hv_vcpu_destroy(vcpu));
  CHECK(hv_vm_destroy());
  free(samples);
  return 0;
}
