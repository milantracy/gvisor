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

// vcpu_cancel: Phase 0 probe 3. hv_vcpus_exit semantics:
//
//   1. Is a cancel issued while the vCPU is not running kept until the next
//      hv_vcpu_run (sticky)? Do two cancels give one or two CANCELED exits?
//      Does a register write clear a pending cancel?
//   2. hv_vcpus_exit with a bad ID or a count of 0.
//   3. Cancel of a spinning guest: is the stop point an instruction boundary,
//      are GPR, FP and full 128-bit SIMD state intact, and does the guest
//      resume correctly? Latency from hv_vcpus_exit to hv_vcpu_run return.
//   4. Cancel of a guest that exits with HVC every ~0.7 us: is any cancel
//      lost, or delivered twice?
//   5. Cost of hv_vcpus_exit on a vCPU that is not running.

#include <mach/mach_time.h>
#include <stdatomic.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "probe.h"

extern const uint8_t guest_clean[], guest_clean_end[];
extern const uint8_t guest_spin[], guest_spin_end[];

#define SPIN_CANCELS 5000
#define HVC_KICKS 5000
#define IDLE_ITERS 100000

static hv_vcpu_t vcpu;
static hv_vcpu_exit_t* vexit;
static uint64_t entry_clean, entry_spin;

static const char* reason_name(hv_exit_reason_t r) {
  switch (r) {
    case HV_EXIT_REASON_CANCELED: return "CANCELED";
    case HV_EXIT_REASON_EXCEPTION: return "EXCEPTION";
    case HV_EXIT_REASON_VTIMER_ACTIVATED: return "VTIMER_ACTIVATED";
    default: return "UNKNOWN";
  }
}

static const char* run_once(void) {
  CHECK(hv_vcpu_run(vcpu));
  return reason_name(vexit->reason);
}

static void* kick_thread(void* arg) {
  (void)arg;
  CHECK(hv_vcpus_exit(&vcpu, 1));
  return NULL;
}

static void pending_semantics(void) {
  printf("\n## Pending cancel semantics (guest: hvc loop)\n");
  uint64_t pc;

  probe_vcpu_reset(vcpu, entry_clean);
  CHECK(hv_vcpus_exit(&vcpu, 1));
  const char* r1 = run_once();
  CHECK(hv_vcpu_get_reg(vcpu, HV_REG_PC, &pc));
  printf("cancel before first run: run1 %s (PC entry%+lld, syndrome 0x%llx), ",
         r1, (long long)(pc - entry_clean), vexit->exception.syndrome);
  printf("run2 %s\n", run_once());

  probe_vcpu_reset(vcpu, entry_clean);
  run_once();
  CHECK(hv_vcpus_exit(&vcpu, 1));
  r1 = run_once();
  printf("cancel after an HVC exit: run1 %s, ", r1);
  printf("run2 %s\n", run_once());

  probe_vcpu_reset(vcpu, entry_clean);
  CHECK(hv_vcpus_exit(&vcpu, 1));
  CHECK(hv_vcpus_exit(&vcpu, 1));
  r1 = run_once();
  const char* r2 = run_once();
  printf("two cancels: run1 %s, run2 %s, ", r1, r2);
  printf("run3 %s\n", run_once());

  probe_vcpu_reset(vcpu, entry_clean);
  pthread_join(probe_spawn(kick_thread, NULL), NULL);
  r1 = run_once();
  printf("cancel from another (exited) thread: run1 %s, ", r1);
  printf("run2 %s\n", run_once());

  probe_vcpu_reset(vcpu, entry_clean);
  CHECK(hv_vcpus_exit(&vcpu, 1));
  probe_vcpu_reset(vcpu, entry_clean);  // ~170 register writes.
  r1 = run_once();
  printf("cancel, then reprogram all registers: run1 %s, ", r1);
  printf("run2 %s\n", run_once());
}

static void bad_args(void) {
  printf("\n## Bad arguments\n");
  hv_vcpu_t bogus = 1000;
  printf("hv_vcpus_exit(ID 1000): %s\n",
         probe_strerror(hv_vcpus_exit(&bogus, 1)));
  printf("hv_vcpus_exit(count 0): %s\n",
         probe_strerror(hv_vcpus_exit(&vcpu, 0)));
  // A failed or empty call must not leave a pending cancel.
  probe_vcpu_reset(vcpu, entry_clean);
  printf("next run after those: %s\n", run_once());
}

// Kicker/runner hand-off for sections 3 and 4.
static _Atomic int go;         // Runner -> kicker: start the next kick.
static _Atomic int kicked;     // Kicker -> runner: the kick was issued.
static _Atomic int stop;
static _Atomic uint64_t canceled_count;
static uint64_t kick_delay[SPIN_CANCELS > HVC_KICKS ? SPIN_CANCELS : HVC_KICKS];
static uint64_t t_kick[SPIN_CANCELS > HVC_KICKS ? SPIN_CANCELS : HVC_KICKS];
static uint64_t t_kick_cost[SPIN_CANCELS > HVC_KICKS ? SPIN_CANCELS : HVC_KICKS];

static uint64_t us_to_ticks(double us) {
  mach_timebase_info_data_t tb;
  mach_timebase_info(&tb);
  return (uint64_t)(us * 1000.0 * tb.denom / tb.numer);
}

static void spin_until(uint64_t t) {
  while (mach_absolute_time() < t) {
  }
}

static void* spin_kicker(void* arg) {
  (void)arg;
  for (int i = 0; i < SPIN_CANCELS; i++) {
    while (atomic_load(&go) != i + 1) {
    }
    spin_until(mach_absolute_time() + kick_delay[i]);
    uint64_t t0 = mach_absolute_time();
    CHECK(hv_vcpus_exit(&vcpu, 1));
    t_kick[i] = t0;
    t_kick_cost[i] = mach_absolute_time() - t0;
    atomic_store(&kicked, i + 1);
  }
  return NULL;
}

static double q_lo(hv_simd_fp_uchar16_t q) {
  double d;
  memcpy(&d, &q, 8);
  return d;
}

static uint64_t q_u64(hv_simd_fp_uchar16_t q, int lane) {
  uint64_t v;
  memcpy(&v, (uint8_t*)&q + 8 * lane, 8);
  return v;
}

// Checks the guest_spin invariants. Returns 0 if they hold.
static int check_spin_state(uint64_t* x0_out, uint64_t* off_out) {
  uint64_t pc, x0, x1;
  hv_simd_fp_uchar16_t q1, q3;
  CHECK(hv_vcpu_get_reg(vcpu, HV_REG_PC, &pc));
  CHECK(hv_vcpu_get_reg(vcpu, HV_REG_X0, &x0));
  CHECK(hv_vcpu_get_reg(vcpu, HV_REG_X1, &x1));
  CHECK(hv_vcpu_get_simd_fp_reg(vcpu, HV_SIMD_FP_REG_Q1, &q1));
  CHECK(hv_vcpu_get_simd_fp_reg(vcpu, HV_SIMD_FP_REG_Q3, &q3));
  uint64_t off = pc - entry_spin;
  uint64_t ex1, ed1, ev3;
  switch (off) {
    case 0: ex1 = 2 * x0; ed1 = x0; ev3 = x0; break;
    case 4: ex1 = 2 * x0 - 2; ed1 = x0 - 1; ev3 = x0 - 1; break;
    case 8: ex1 = 2 * x0; ed1 = x0 - 1; ev3 = x0 - 1; break;
    case 12: ex1 = 2 * x0; ed1 = x0; ev3 = x0 - 1; break;
    case 16: ex1 = 2 * x0; ed1 = x0; ev3 = x0; break;
    default:
      printf("PC outside loop: entry%+lld\n", (long long)off);
      return 1;
  }
  *x0_out = x0;
  *off_out = off;
  if (x1 != ex1 || q_lo(q1) != (double)ed1 || q_u64(q3, 0) != ev3 ||
      q_u64(q3, 1) != ev3) {
    printf("state mismatch at offset %llu: x0 %llu x1 %llu (want %llu) d1 %.0f "
           "(want %llu) v3 {%llu,%llu} (want %llu)\n",
           off, x0, x1, ex1, q_lo(q1), ed1, q_u64(q3, 0), q_u64(q3, 1), ev3);
    return 1;
  }
  return 0;
}

static void cancel_spinning(void) {
  printf("\n## Cancel of a spinning guest, %d cancels, random delay 5-100 us\n",
         SPIN_CANCELS);
  probe_vcpu_reset(vcpu, entry_spin);
  hv_simd_fp_uchar16_t one = {0}, ones = {0};
  double d = 1.0;
  memcpy(&one, &d, 8);
  uint64_t u = 1;
  memcpy(&ones, &u, 8);
  memcpy((uint8_t*)&ones + 8, &u, 8);
  CHECK(hv_vcpu_set_simd_fp_reg(vcpu, HV_SIMD_FP_REG_Q2, one));
  CHECK(hv_vcpu_set_simd_fp_reg(vcpu, HV_SIMD_FP_REG_Q4, ones));

  srandom(1);
  for (int i = 0; i < SPIN_CANCELS; i++) {
    kick_delay[i] = us_to_ticks(5 + random() % 96);
  }
  static uint64_t latency[SPIN_CANCELS];
  uint64_t offs[5] = {0};
  int bad = 0, not_canceled = 0, no_progress = 0;
  uint64_t prev_x0 = 0;
  atomic_store(&go, 0);
  atomic_store(&kicked, 0);
  pthread_t k = probe_spawn(spin_kicker, NULL);
  for (int i = 0; i < SPIN_CANCELS; i++) {
    atomic_store(&go, i + 1);
    CHECK(hv_vcpu_run(vcpu));
    uint64_t t_ret = mach_absolute_time();
    while (atomic_load(&kicked) != i + 1) {
    }
    latency[i] = t_ret > t_kick[i] ? t_ret - t_kick[i] : 0;
    if (vexit->reason != HV_EXIT_REASON_CANCELED) {
      not_canceled++;
      probe_expect_hvc(vcpu, vexit, 0xffff);  // Prints the exit and exits.
    }
    uint64_t x0, off;
    if (check_spin_state(&x0, &off) != 0) {
      bad++;
      if (bad > 5) break;
      continue;
    }
    offs[off / 4]++;
    if (x0 <= prev_x0) no_progress++;
    prev_x0 = x0;
  }
  pthread_join(k, NULL);
  printf("exits not CANCELED: %d; state mismatches: %d; resumes without "
         "progress: %d; final x0 %llu\n",
         not_canceled, bad, no_progress, prev_x0);
  printf("stop PC offset histogram (0/4/8/12/16): %llu %llu %llu %llu %llu\n",
         offs[0], offs[1], offs[2], offs[3], offs[4]);
  probe_print_percentiles("exit() -> run returns", latency, SPIN_CANCELS);
  probe_print_percentiles("hv_vcpus_exit call (running)", t_kick_cost,
                          SPIN_CANCELS);
}

// Section 4: the runner loops on the hvc guest and counts CANCELED exits.
// The kicker issues a cancel and waits up to 10 ms for the count to move.
static void* hvc_kicker(void* arg) {
  uint64_t* lost = arg;
  uint64_t timeout = us_to_ticks(10000);
  srandom(2);
  for (int i = 0; i < HVC_KICKS; i++) {
    spin_until(mach_absolute_time() + us_to_ticks(2 + random() % 49));
    uint64_t before = atomic_load(&canceled_count);
    uint64_t t0 = mach_absolute_time();
    CHECK(hv_vcpus_exit(&vcpu, 1));
    uint64_t deadline = t0 + timeout;
    while (atomic_load(&canceled_count) == before) {
      if (mach_absolute_time() > deadline) {
        (*lost)++;
        break;
      }
    }
    t_kick[i] = mach_absolute_time() - t0;  // Kick to observed CANCELED.
  }
  // Let the runner drain anything still pending.
  spin_until(mach_absolute_time() + us_to_ticks(1000));
  atomic_store(&stop, 1);
  return NULL;
}

static void cancel_hvc_loop(void) {
  printf("\n## Cancel of an HVC-exiting guest, %d cancels, random delay "
         "2-50 us\n",
         HVC_KICKS);
  probe_vcpu_reset(vcpu, entry_clean);
  atomic_store(&canceled_count, 0);
  atomic_store(&stop, 0);
  uint64_t lost = 0, runs = 0, hvcs = 0;
  pthread_t k = probe_spawn(hvc_kicker, &lost);
  while (!atomic_load(&stop)) {
    CHECK(hv_vcpu_run(vcpu));
    runs++;
    if (vexit->reason == HV_EXIT_REASON_CANCELED) {
      atomic_fetch_add(&canceled_count, 1);
    } else {
      probe_expect_hvc(vcpu, vexit, PROBE_HVC_IMM);
      hvcs++;
    }
  }
  pthread_join(k, NULL);
  uint64_t c = atomic_load(&canceled_count);
  printf("runs %llu, HVC exits %llu, CANCELED exits %llu for %d cancels; "
         "lost (no CANCELED within 10 ms) %llu; extra CANCELED %lld\n",
         runs, hvcs, c, HVC_KICKS, lost, (long long)c - HVC_KICKS);
  probe_print_percentiles("exit() -> CANCELED seen", t_kick, HVC_KICKS);
}

static void idle_cost(void) {
  printf("\n## hv_vcpus_exit on a vCPU that is not running\n");
  probe_vcpu_reset(vcpu, entry_clean);
  uint64_t t0 = mach_absolute_time();
  for (int i = 0; i < IDLE_ITERS; i++) CHECK(hv_vcpus_exit(&vcpu, 1));
  printf("hv_vcpus_exit (idle): %.1f ns/call\n",
         probe_ticks_to_ns(mach_absolute_time() - t0) / IDLE_ITERS);
  const char* r1 = run_once();
  const char* r2 = run_once();
  printf("after %d idle cancels: run1 %s, run2 %s\n", IDLE_ITERS, r1, r2);

  // A run that is cancelled before entry: cost of the no-op round trip.
  static uint64_t s[IDLE_ITERS / 10];
  for (int i = 0; i < IDLE_ITERS / 10; i++) {
    CHECK(hv_vcpus_exit(&vcpu, 1));
    uint64_t a = mach_absolute_time();
    CHECK(hv_vcpu_run(vcpu));
    s[i] = mach_absolute_time() - a;
    if (vexit->reason != HV_EXIT_REASON_CANCELED) {
      printf("pre-cancelled run %d returned %s\n", i,
             reason_name(vexit->reason));
      break;
    }
  }
  probe_print_percentiles("pre-cancelled hv_vcpu_run", s, IDLE_ITERS / 10);
}

int main(void) {
  probe_set_qos();
  probe_print_env("vcpu_cancel");
  probe_vm_init();
  entry_clean = probe_load_code(guest_clean, guest_clean_end, 0);
  entry_spin = probe_load_code(guest_spin, guest_spin_end, 0x100);
  CHECK(hv_vcpu_create(&vcpu, &vexit, NULL));

  pending_semantics();
  bad_args();
  cancel_spinning();
  cancel_hvc_loop();
  idle_cost();

  CHECK(hv_vcpu_destroy(vcpu));
  CHECK(hv_vm_destroy());
  return 0;
}
