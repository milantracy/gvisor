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

// tlb_bounce: Phase 0 probe 7. A host-side Sentry cannot run TLBI, so it
// asks each vCPU to run one on its next guest entry (TLBI mailbox) and kicks
// the vCPUs with hv_vcpus_exit. This probe measures:
//
//   A. The cost of one TLBI (vmalle1, vmalle1is, vae1) inside the guest, as
//      the extra time of an HVC round trip that runs it.
//   B. The time from hv_vcpus_exit on N running vCPUs until all N have
//      re-entered the guest, run the TLBI and acknowledged it.
//   C. The cost of vmalle1is while other vCPUs are running.

#include <mach/mach_time.h>
#include <stdatomic.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/sysctl.h>

#include "probe.h"

extern const uint8_t guest_tlbi_mailbox[], guest_tlbi_mailbox_end[];
extern const uint8_t guest_hvc_vmalle1[], guest_hvc_vmalle1_end[];
extern const uint8_t guest_hvc_vmalle1is[], guest_hvc_vmalle1is_end[];
extern const uint8_t guest_hvc_vae1[], guest_hvc_vae1_end[];
extern const uint8_t guest_hvc_dsb[], guest_hvc_dsb_end[];

#define MAX_WORKERS 16
#define BOUNCES 2000
#define WARM_BOUNCES 200
#define MAILBOX_OFF 0x60000ULL  // Requested generation.
#define ACK_OFF 0x61000ULL      // Per-vCPU acknowledged generation, 64 B apart.

static uint64_t entry_mailbox, entry_dsb, entry_vmalle1, entry_vmalle1is,
    entry_vae1;

static const char* kind_name[] = {"no TLBI", "tlbi vmalle1", "tlbi vmalle1is",
                                  "tlbi vae1"};

static _Atomic uint64_t* mailbox(void) {
  return (_Atomic uint64_t*)(guest_mem + MAILBOX_OFF);
}

static _Atomic uint64_t* ack(int i) {
  return (_Atomic uint64_t*)(guest_mem + ACK_OFF + 64 * (uint64_t)i);
}

struct worker {
  pthread_t thread;
  int idx;
  int kind;
  hv_vcpu_t vcpu;
  _Atomic int ready;
  uint64_t canceled;
};

static _Atomic int stop_workers;

static void* worker_main(void* p) {
  struct worker* w = p;
  hv_vcpu_exit_t* ex;
  CHECK(hv_vcpu_create(&w->vcpu, &ex, NULL));
  probe_vcpu_reset(w->vcpu, entry_mailbox);
  CHECK(hv_vcpu_set_reg(w->vcpu, HV_REG_X20, GUEST_BASE + MAILBOX_OFF));
  CHECK(hv_vcpu_set_reg(w->vcpu, HV_REG_X21,
                        GUEST_BASE + ACK_OFF + 64 * (uint64_t)w->idx));
  CHECK(hv_vcpu_set_reg(w->vcpu, HV_REG_X22, (uint64_t)w->kind));
  CHECK(hv_vcpu_set_reg(w->vcpu, HV_REG_X23, (GUEST_BASE + 0x80000) >> 12));
  atomic_store(&w->ready, 1);
  for (;;) {
    CHECK(hv_vcpu_set_reg(w->vcpu, HV_REG_PC, entry_mailbox));
    CHECK(hv_vcpu_run(w->vcpu));
    if (ex->reason != HV_EXIT_REASON_CANCELED) {
      fprintf(stderr, "worker %d: ", w->idx);
      probe_expect_hvc(w->vcpu, ex, 0xffff);  // Prints the exit and exits.
    }
    w->canceled++;
    if (atomic_load(&stop_workers)) break;
  }
  CHECK(hv_vcpu_destroy(w->vcpu));
  return NULL;
}

static struct worker workers[MAX_WORKERS];
static hv_vcpu_t worker_vcpus[MAX_WORKERS];

static void start_workers(int n, int kind) {
  atomic_store(&stop_workers, 0);
  atomic_store(mailbox(), 0);
  for (int i = 0; i < n; i++) {
    atomic_store(ack(i), 0);
    memset(&workers[i], 0, sizeof(workers[i]));
    workers[i].idx = i;
    workers[i].kind = kind;
    workers[i].thread = probe_spawn(worker_main, &workers[i]);
  }
  for (int i = 0; i < n; i++) {
    while (!atomic_load(&workers[i].ready)) {
    }
    worker_vcpus[i] = workers[i].vcpu;
  }
}

// One kick after setting stop_workers is enough: a cancel is sticky (probe 3),
// so a worker that is between runs gets CANCELED on its next run.
static void stop_all(int n) {
  atomic_store(&stop_workers, 1);
  CHECK(hv_vcpus_exit(worker_vcpus, (uint32_t)n));
  for (int i = 0; i < n; i++) pthread_join(workers[i].thread, NULL);
}

static uint64_t us_to_ticks(double us) {
  mach_timebase_info_data_t tb;
  mach_timebase_info(&tb);
  return (uint64_t)(us * 1000.0 * tb.denom / tb.numer);
}

static void spin_ticks(uint64_t t) {
  uint64_t end = mach_absolute_time() + t;
  while (mach_absolute_time() < end) {
  }
}

// One generation: write the mailbox, kick all n vCPUs, wait for n acks.
static uint64_t bounce(int n, uint64_t gen, uint64_t* kick_ticks) {
  atomic_store(mailbox(), gen);
  uint64_t t0 = mach_absolute_time();
  CHECK(hv_vcpus_exit(worker_vcpus, (uint32_t)n));
  uint64_t t1 = mach_absolute_time();
  uint64_t deadline = t0 + us_to_ticks(100000);
  for (int i = 0; i < n; i++) {
    while (atomic_load(ack(i)) != gen) {
      if (mach_absolute_time() > deadline) {
        fprintf(stderr, "vCPU %d did not ack generation %llu\n", i, gen);
        exit(1);
      }
    }
  }
  uint64_t t2 = mach_absolute_time();
  if (kick_ticks) *kick_ticks = t1 - t0;
  return t2 - t0;
}

static void part_b(void) {
  printf("\n## B. Kick N running vCPUs, each runs the TLBI mailbox on "
         "re-entry; time until all N acked (%d bounces, 20-50 us apart)\n",
         BOUNCES);
  static const int ns[] = {1, 2, 4, 8, 11};
  static uint64_t lat[BOUNCES], kick[BOUNCES];
  for (size_t ni = 0; ni < sizeof(ns) / sizeof(ns[0]); ni++) {
    int n = ns[ni];
    for (int kind = 0; kind <= 2; kind++) {
      start_workers(n, kind);
      spin_ticks(us_to_ticks(1000));
      uint64_t gen = 0;
      srandom(7);
      for (int i = 0; i < WARM_BOUNCES; i++) {
        bounce(n, ++gen, NULL);
        spin_ticks(us_to_ticks(20 + random() % 31));
      }
      for (int i = 0; i < BOUNCES; i++) {
        lat[i] = bounce(n, ++gen, &kick[i]);
        spin_ticks(us_to_ticks(20 + random() % 31));
      }
      stop_all(n);
      char label[64];
      snprintf(label, sizeof(label), "N=%-2d %s", n, kind_name[kind]);
      probe_print_percentiles(label, lat, BOUNCES);
      snprintf(label, sizeof(label), "N=%-2d   hv_vcpus_exit call", n);
      probe_print_percentiles(label, kick, BOUNCES);
    }
  }
}

static double hvc_loop_ns(hv_vcpu_t v, hv_vcpu_exit_t* ex, uint64_t entry,
                          int iters) {
  probe_vcpu_reset(v, entry);
  CHECK(hv_vcpu_set_reg(v, HV_REG_X23, (GUEST_BASE + 0x80000) >> 12));
  CHECK(hv_vcpu_run(v));
  if (ex->reason != HV_EXIT_REASON_EXCEPTION ||
      ESR_EC(ex->exception.syndrome) != EC_HVC64) {
    printf("  first exit is not HVC: reason %u, syndrome 0x%llx (EC 0x%llx)\n",
           ex->reason, ex->exception.syndrome, ESR_EC(ex->exception.syndrome));
    return -1;
  }
  for (int i = 0; i < iters / 10; i++) CHECK(hv_vcpu_run(v));
  double best = 1e18;
  for (int t = 0; t < 5; t++) {
    uint64_t t0 = mach_absolute_time();
    for (int i = 0; i < iters; i++) CHECK(hv_vcpu_run(v));
    double ns = probe_ticks_to_ns(mach_absolute_time() - t0) / iters;
    if (ns < best) best = ns;
  }
  probe_expect_hvc(v, ex, PROBE_HVC_IMM);
  return best;
}

static void part_a_c(hv_vcpu_t v, hv_vcpu_exit_t* ex, int others) {
  const struct {
    const char* name;
    uint64_t entry;
  } loops[] = {
      {"dsb nsh; isb (baseline)", entry_dsb},
      {"tlbi vmalle1; dsb nsh; isb", entry_vmalle1},
      {"tlbi vmalle1is; dsb ish; isb", entry_vmalle1is},
      {"tlbi vae1; dsb nsh; isb", entry_vae1},
  };
  if (others) start_workers(others, 0);
  double base = 0;
  for (int i = 0; i < 4; i++) {
    double ns = hvc_loop_ns(v, ex, loops[i].entry, 200000);
    if (i == 0) base = ns;
    printf("  %-32s %7.1f ns/round trip (best of 5 x 200000), +%.1f ns\n",
           loops[i].name, ns, ns - base);
  }
  if (others) stop_all(others);
}

int main(void) {
  probe_set_qos();
  probe_print_env("tlb_bounce");
  int p0 = 0, p1 = 0;
  size_t len = sizeof(int);
  sysctlbyname("hw.perflevel0.physicalcpu", &p0, &len, NULL, 0);
  len = sizeof(int);
  sysctlbyname("hw.perflevel1.physicalcpu", &p1, &len, NULL, 0);
  printf("# cores: %d performance, %d efficiency\n", p0, p1);

  probe_vm_init();
  entry_mailbox = probe_load_code(guest_tlbi_mailbox, guest_tlbi_mailbox_end,
                                  0x000);
  entry_dsb = probe_load_code(guest_hvc_dsb, guest_hvc_dsb_end, 0x200);
  entry_vmalle1 = probe_load_code(guest_hvc_vmalle1, guest_hvc_vmalle1_end,
                                  0x300);
  entry_vmalle1is = probe_load_code(guest_hvc_vmalle1is,
                                    guest_hvc_vmalle1is_end, 0x400);
  entry_vae1 = probe_load_code(guest_hvc_vae1, guest_hvc_vae1_end, 0x500);

  hv_vcpu_t v;
  hv_vcpu_exit_t* ex;
  CHECK(hv_vcpu_create(&v, &ex, NULL));
  // Warm up the CPU clock.
  probe_vcpu_reset(v, entry_dsb);
  uint64_t t0 = mach_absolute_time();
  while (probe_ticks_to_ns(mach_absolute_time() - t0) < 300e6) {
    CHECK(hv_vcpu_run(v));
  }

  printf("\n## A. TLBI cost in the guest, one vCPU\n");
  part_a_c(v, ex, 0);
  part_b();
  printf("\n## C. TLBI cost in the guest while 8 other vCPUs spin in the "
         "guest\n");
  part_a_c(v, ex, 8);

  CHECK(hv_vcpu_destroy(v));
  CHECK(hv_vm_destroy());
  return 0;
}
