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

// vcpu_signal: Phase 0 probe 4. Signals on a vCPU thread:
//
//   1. A blocked, pending SIGURG on the vCPU thread: does hv_vcpu_run fail,
//      exit spuriously, or slow down?
//   2. An unblocked SIGURG sent to the vCPU thread while the guest spins: does
//      the handler run, does hv_vcpu_run return, with which exit reason, and
//      is guest state intact? Run with Go's handler flags
//      (SA_ONSTACK|SA_RESTART|SA_SIGINFO) and without SA_RESTART.
//   3. Signal delivery latency to a thread in hv_vcpu_run against a thread
//      spinning in the host.

#define _DARWIN_C_SOURCE
#include <dlfcn.h>
#include <mach/mach_time.h>
#include <signal.h>
#include <stdatomic.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/mman.h>
#include <sys/ucontext.h>

#include "probe.h"

#define BLOCKED_ITERS 1000000
#define SIGNALS 2000

static hv_vcpu_t vcpu;
static hv_vcpu_exit_t* vexit;
static uint64_t entry_clean, entry_spin;
static pthread_t runner;

static _Atomic uint64_t handler_count;
static _Atomic uint64_t handler_time;
static _Atomic uint64_t handler_pc;  // Interrupted user PC, last signal.
static _Atomic int stop;
static _Atomic int done;

static void on_sigurg(int sig, siginfo_t* info, void* uc) {
  (void)sig;
  (void)info;
  atomic_store(&handler_time, mach_absolute_time());
  atomic_store(&handler_pc, ((ucontext_t*)uc)->uc_mcontext->__ss.__pc);
  atomic_fetch_add(&handler_count, 1);
}

static void print_pc(void) {
  uint64_t pc = atomic_load(&handler_pc);
  Dl_info info;
  if (dladdr((void*)(uintptr_t)pc, &info) && info.dli_sname) {
    printf("interrupted PC in last handler call: %s+0x%llx (%s)\n",
           info.dli_sname, pc - (uint64_t)(uintptr_t)info.dli_saddr,
           info.dli_fname);
  } else {
    printf("interrupted PC in last handler call: 0x%llx (no symbol)\n", pc);
  }
}

static void install(int flags) {
  struct sigaction sa;
  memset(&sa, 0, sizeof(sa));
  sa.sa_sigaction = on_sigurg;
  sa.sa_flags = flags;
  sigemptyset(&sa.sa_mask);
  if (sigaction(SIGURG, &sa, NULL) != 0) {
    perror("sigaction");
    exit(1);
  }
}

static void set_blocked(int blocked) {
  sigset_t s;
  sigemptyset(&s);
  sigaddset(&s, SIGURG);
  pthread_sigmask(blocked ? SIG_BLOCK : SIG_UNBLOCK, &s, NULL);
}

static uint64_t us_to_ticks(double us) {
  mach_timebase_info_data_t tb;
  mach_timebase_info(&tb);
  return (uint64_t)(us * 1000.0 * tb.denom / tb.numer);
}

static double hvc_loop_ns(int iters, uint64_t* failures, uint64_t* other) {
  uint64_t t0 = mach_absolute_time();
  for (int i = 0; i < iters; i++) {
    hv_return_t r = hv_vcpu_run(vcpu);
    if (r != HV_SUCCESS) {
      (*failures)++;
      continue;
    }
    if (vexit->reason != HV_EXIT_REASON_EXCEPTION ||
        ESR_EC(vexit->exception.syndrome) != EC_HVC64) {
      (*other)++;
    }
  }
  return probe_ticks_to_ns(mach_absolute_time() - t0) / iters;
}

static void blocked_pending(void) {
  printf("\n## Blocked pending SIGURG on the vCPU thread\n");
  install(SA_ONSTACK | SA_RESTART | SA_SIGINFO);
  probe_vcpu_reset(vcpu, entry_clean);
  uint64_t f = 0, o = 0;
  hvc_loop_ns(100000, &f, &o);
  double base = hvc_loop_ns(BLOCKED_ITERS, &f, &o);
  printf("no signal pending:     %.1f ns/round trip, run failures %llu, "
         "non-HVC exits %llu\n",
         base, f, o);

  set_blocked(1);
  pthread_kill(pthread_self(), SIGURG);
  sigset_t pend;
  sigpending(&pend);
  printf("SIGURG pending after pthread_kill while blocked: %s\n",
         sigismember(&pend, SIGURG) ? "yes" : "no");
  f = o = 0;
  double blocked = hvc_loop_ns(BLOCKED_ITERS, &f, &o);
  sigpending(&pend);
  printf("SIGURG blocked+pending: %.1f ns/round trip, run failures %llu, "
         "non-HVC exits %llu, still pending %s, handler calls %llu\n",
         blocked, f, o, sigismember(&pend, SIGURG) ? "yes" : "no",
         atomic_load(&handler_count));
  set_blocked(0);
  printf("handler calls after unblock: %llu\n", atomic_load(&handler_count));
}

// Sender thread for sections 2 and 3: sends SIGURG to the runner, waits for
// the handler, records latency.
struct sender_args {
  uint64_t* latency;
  uint64_t lost;
  int stop_with_cancel;
};

static void* sender(void* p) {
  struct sender_args* a = p;
  srandom(3);
  uint64_t timeout = us_to_ticks(10000);
  for (int i = 0; i < SIGNALS; i++) {
    uint64_t now = mach_absolute_time();
    uint64_t until = now + us_to_ticks(20 + random() % 81);
    while (mach_absolute_time() < until) {
    }
    uint64_t before = atomic_load(&handler_count);
    uint64_t t0 = mach_absolute_time();
    pthread_kill(runner, SIGURG);
    while (atomic_load(&handler_count) == before) {
      if (mach_absolute_time() > t0 + timeout) {
        a->lost++;
        break;
      }
    }
    uint64_t th = atomic_load(&handler_time);
    a->latency[i] = th > t0 ? th - t0 : 0;
  }
  atomic_store(&stop, 1);
  // A signal-caused exit can absorb a single cancel, so keep cancelling
  // until the runner confirms it stopped.
  while (a->stop_with_cancel && !atomic_load(&done)) {
    CHECK(hv_vcpus_exit(&vcpu, 1));
    uint64_t until = mach_absolute_time() + us_to_ticks(100);
    while (mach_absolute_time() < until) {
    }
  }
  return NULL;
}

static void signal_during_run(const char* label, int flags) {
  printf("\n## SIGURG during a spinning guest, %d signals, %s\n", SIGNALS,
         label);
  install(flags);
  probe_spin_reset(vcpu, entry_spin);
  atomic_store(&handler_count, 0);
  atomic_store(&stop, 0);
  atomic_store(&done, 0);
  static uint64_t lat[SIGNALS];
  struct sender_args a = {.latency = lat, .stop_with_cancel = 1};
  uint64_t returns[4] = {0}, failures = 0, bad = 0;
  hv_return_t first_fail = HV_SUCCESS;
  pthread_t s = probe_spawn(sender, &a);
  for (;;) {
    hv_return_t r = hv_vcpu_run(vcpu);
    if (r != HV_SUCCESS) {
      if (failures++ == 0) first_fail = r;
      continue;
    }
    int stopping = atomic_load(&stop);
    if (!stopping) returns[vexit->reason < 4 ? vexit->reason : 3]++;
    uint64_t x0, off;
    if (probe_check_spin(vcpu, entry_spin, &x0, &off) != 0) bad++;
    if (vexit->reason == HV_EXIT_REASON_EXCEPTION) {
      probe_expect_hvc(vcpu, vexit, 0xffff);  // Unexpected; prints, exits.
    }
    if (stopping && vexit->reason == HV_EXIT_REASON_CANCELED) break;
  }
  atomic_store(&done, 1);
  pthread_join(s, NULL);
  // Drain a cancel that may still be pending (cancels coalesce).
  CHECK(hv_vcpus_exit(&vcpu, 1));
  CHECK(hv_vcpu_run(vcpu));
  printf("handler calls %llu for %d signals (lost %llu); hv_vcpu_run "
         "returns before stop: CANCELED %llu, EXCEPTION %llu, VTIMER %llu, "
         "other %llu; run failures %llu%s%s; state "
         "mismatches %llu\n",
         atomic_load(&handler_count), SIGNALS, a.lost, returns[0], returns[1],
         returns[2], returns[3], failures, failures ? " first " : "",
         failures ? probe_strerror(first_fail) : "", bad);
  probe_print_percentiles("pthread_kill -> handler", lat, SIGNALS);
  print_pc();
}

static void signal_host_spin(void) {
  printf("\n## SIGURG to a thread spinning in the host (no vCPU run), %d "
         "signals\n",
         SIGNALS);
  install(SA_ONSTACK | SA_RESTART | SA_SIGINFO);
  atomic_store(&handler_count, 0);
  atomic_store(&stop, 0);
  static uint64_t lat[SIGNALS];
  struct sender_args a = {.latency = lat, .stop_with_cancel = 0};
  pthread_t s = probe_spawn(sender, &a);
  while (!atomic_load(&stop)) {
  }
  pthread_join(s, NULL);
  printf("handler calls %llu (lost %llu)\n", atomic_load(&handler_count),
         a.lost);
  probe_print_percentiles("pthread_kill -> handler", lat, SIGNALS);
}

int main(void) {
  probe_set_qos();
  probe_print_env("vcpu_signal");
  runner = pthread_self();

  // Go runs signal handlers on an alternate stack (SA_ONSTACK).
  stack_t ss = {.ss_size = 1 << 16, .ss_flags = 0};
  ss.ss_sp = mmap(NULL, ss.ss_size, PROT_READ | PROT_WRITE,
                  MAP_ANON | MAP_PRIVATE, -1, 0);
  if (sigaltstack(&ss, NULL) != 0) {
    perror("sigaltstack");
    return 1;
  }

  probe_vm_init();
  entry_clean = probe_load_code(guest_clean, guest_clean_end, 0);
  entry_spin = probe_load_code(guest_spin, guest_spin_end, 0x100);
  CHECK(hv_vcpu_create(&vcpu, &vexit, NULL));

  blocked_pending();
  signal_during_run("SA_ONSTACK|SA_RESTART|SA_SIGINFO (Go)",
                    SA_ONSTACK | SA_RESTART | SA_SIGINFO);
  signal_during_run("SA_ONSTACK|SA_SIGINFO (no SA_RESTART)",
                    SA_ONSTACK | SA_SIGINFO);
  signal_host_spin();

  CHECK(hv_vcpu_destroy(vcpu));
  CHECK(hv_vm_destroy());
  return 0;
}
