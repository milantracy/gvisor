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

// vcpu_lifecycle: Phase 0 probe 2. Two modes, each run in its own process:
//
//   cost  Thread-affinity errors (second vCPU on one thread, vCPU calls from
//         a non-owner thread), hv_vcpu_create/destroy cost, cost of the
//         first run after create, the real vCPU limit, and create/destroy
//         cost with the other vCPU slots in use.
//   leak  Threads that create a vCPU and exit without hv_vcpu_destroy. Is the
//         vCPU slot reclaimed? What do other threads see for the leaked ID?
//         Do hv_vm_destroy and a second hv_vm_create still work, and how
//         many vCPUs does the second VM get?
//   leak-one  As leak, with a single leaking thread.

#include <mach/mach_time.h>
#include <pthread.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "probe.h"

extern const uint8_t guest_clean[], guest_clean_end[];

#define COST_ITERS 2000
#define MAX_THREADS 256
#define LEAK_THREADS 200

static uint64_t entry;

// A thread that owns one vCPU and parks until released.
struct owner {
  pthread_t thread;
  hv_vcpu_t vcpu;
  hv_vcpu_exit_t* ex;
  hv_return_t create_ret;
  hv_return_t destroy_ret;
  int ready;            // Set by owner after create (and reset).
  int release;          // Set by main to let the owner continue.
  int run_after_park;   // Owner runs the vCPU once after release.
  int destroy;          // Owner destroys the vCPU before exit.
  hv_exit_reason_t run_reason;
  hv_return_t run_ret;
};

static pthread_mutex_t mu = PTHREAD_MUTEX_INITIALIZER;
static pthread_cond_t cv = PTHREAD_COND_INITIALIZER;

static void* owner_main(void* p) {
  struct owner* o = p;
  o->create_ret = hv_vcpu_create(&o->vcpu, &o->ex, NULL);
  if (o->create_ret == HV_SUCCESS) probe_vcpu_reset(o->vcpu, entry);
  pthread_mutex_lock(&mu);
  o->ready = 1;
  pthread_cond_broadcast(&cv);
  while (!o->release) pthread_cond_wait(&cv, &mu);
  pthread_mutex_unlock(&mu);
  if (o->create_ret != HV_SUCCESS) return NULL;
  if (o->run_after_park) {
    o->run_ret = hv_vcpu_run(o->vcpu);
    o->run_reason = o->ex->reason;
  }
  if (o->destroy) o->destroy_ret = hv_vcpu_destroy(o->vcpu);
  return NULL;
}

static void owner_start(struct owner* o, int destroy) {
  memset(o, 0, sizeof(*o));
  o->destroy = destroy;
  o->thread = probe_spawn(owner_main, o);
  pthread_mutex_lock(&mu);
  while (!o->ready) pthread_cond_wait(&cv, &mu);
  pthread_mutex_unlock(&mu);
}

static void owner_finish(struct owner* o) {
  pthread_mutex_lock(&mu);
  o->release = 1;
  pthread_cond_broadcast(&cv);
  pthread_mutex_unlock(&mu);
  pthread_join(o->thread, NULL);
}

// Starts parked owners until hv_vcpu_create fails or MAX_THREADS is reached.
// Returns the number that succeeded; owners[n] holds the failed attempt.
static int fill_vcpus(struct owner* owners, hv_return_t* fail) {
  int n = 0;
  *fail = HV_SUCCESS;
  for (; n < MAX_THREADS; n++) {
    owner_start(&owners[n], 1);
    if (owners[n].create_ret != HV_SUCCESS) {
      *fail = owners[n].create_ret;
      owner_finish(&owners[n]);
      break;
    }
  }
  return n;
}

static void release_all(struct owner* owners, int n) {
  for (int i = 0; i < n; i++) owner_finish(&owners[i]);
}

static void time_create_destroy(const char* label) {
  static uint64_t c[COST_ITERS], d[COST_ITERS];
  hv_vcpu_t v;
  hv_vcpu_exit_t* ex;
  for (int i = 0; i < 100; i++) {  // Warm up.
    CHECK(hv_vcpu_create(&v, &ex, NULL));
    CHECK(hv_vcpu_destroy(v));
  }
  for (int i = 0; i < COST_ITERS; i++) {
    uint64_t t0 = mach_absolute_time();
    CHECK(hv_vcpu_create(&v, &ex, NULL));
    uint64_t t1 = mach_absolute_time();
    CHECK(hv_vcpu_destroy(v));
    uint64_t t2 = mach_absolute_time();
    c[i] = t1 - t0;
    d[i] = t2 - t1;
  }
  char buf[64];
  snprintf(buf, sizeof(buf), "create (%s)", label);
  probe_print_percentiles(buf, c, COST_ITERS);
  snprintf(buf, sizeof(buf), "destroy (%s)", label);
  probe_print_percentiles(buf, d, COST_ITERS);
}

static void mode_cost(void) {
  hv_vcpu_t v, v2;
  hv_vcpu_exit_t *ex, *ex2;

  printf("\n## Thread affinity\n");
  CHECK(hv_vcpu_create(&v, &ex, NULL));
  printf("second hv_vcpu_create on same thread: %s\n",
         probe_strerror(hv_vcpu_create(&v2, &ex2, NULL)));
  CHECK(hv_vcpu_destroy(v));

  struct owner a;
  owner_start(&a, 1);
  CHECK(a.create_ret);
  uint64_t x;
  printf("non-owner hv_vcpu_get_reg:  %s\n",
         probe_strerror(hv_vcpu_get_reg(a.vcpu, HV_REG_X0, &x)));
  printf("non-owner hv_vcpu_set_reg:  %s\n",
         probe_strerror(hv_vcpu_set_reg(a.vcpu, HV_REG_X0, 1)));
  printf("non-owner hv_vcpu_run:      %s\n",
         probe_strerror(hv_vcpu_run(a.vcpu)));
  printf("non-owner hv_vcpu_destroy:  %s\n",
         probe_strerror(hv_vcpu_destroy(a.vcpu)));
  a.run_after_park = 1;
  owner_finish(&a);
  printf("owner run after those calls: %s, reason %u; owner destroy: %s\n",
         probe_strerror(a.run_ret), a.run_reason,
         probe_strerror(a.destroy_ret));

  printf("\n## Create/destroy cost, %d iterations, no other vCPUs\n",
         COST_ITERS);
  time_create_destroy("idle VM");

  // Full cycle: create, program registers, first run, second run, destroy.
  static uint64_t tc[COST_ITERS], tr[COST_ITERS], t1st[COST_ITERS],
      t2nd[COST_ITERS], td[COST_ITERS];
  for (int i = 0; i < COST_ITERS; i++) {
    uint64_t t0 = mach_absolute_time();
    CHECK(hv_vcpu_create(&v, &ex, NULL));
    uint64_t t1 = mach_absolute_time();
    probe_vcpu_reset(v, entry);
    uint64_t t2 = mach_absolute_time();
    CHECK(hv_vcpu_run(v));
    uint64_t t3 = mach_absolute_time();
    probe_expect_hvc(v, ex, PROBE_HVC_IMM);
    uint64_t t4 = mach_absolute_time();
    CHECK(hv_vcpu_run(v));
    uint64_t t5 = mach_absolute_time();
    probe_expect_hvc(v, ex, PROBE_HVC_IMM);
    uint64_t t6 = mach_absolute_time();
    CHECK(hv_vcpu_destroy(v));
    uint64_t t7 = mach_absolute_time();
    tc[i] = t1 - t0;
    tr[i] = t2 - t1;
    t1st[i] = t3 - t2;
    t2nd[i] = t5 - t4;
    td[i] = t7 - t6;
  }
  printf("\n## Full cycle, %d iterations\n", COST_ITERS);
  probe_print_percentiles("create", tc, COST_ITERS);
  probe_print_percentiles("program all registers", tr, COST_ITERS);
  probe_print_percentiles("first hv_vcpu_run", t1st, COST_ITERS);
  probe_print_percentiles("second hv_vcpu_run", t2nd, COST_ITERS);
  probe_print_percentiles("destroy (after runs)", td, COST_ITERS);

  printf("\n## vCPU limit\n");
  static struct owner owners[MAX_THREADS + 1];
  hv_return_t fail;
  uint64_t t0 = mach_absolute_time();
  int n = fill_vcpus(owners, &fail);
  uint64_t t1 = mach_absolute_time();
  hv_vcpu_t lo = owners[0].vcpu, hi = owners[0].vcpu;
  for (int i = 0; i < n; i++) {
    if (owners[i].vcpu < lo) lo = owners[i].vcpu;
    if (owners[i].vcpu > hi) hi = owners[i].vcpu;
  }
  printf("concurrent vCPUs created: %d; next create: %s; IDs %llu..%llu; "
         "%.1f us per thread start+create\n",
         n, probe_strerror(fail), lo, hi,
         probe_ticks_to_ns(t1 - t0) / 1000.0 / (n + 1));

  if (n > 0) {
    // Free exactly one slot and time create/destroy on the main thread.
    owner_finish(&owners[n - 1]);
    printf("\n## Create/destroy cost, %d other vCPUs live\n", n - 1);
    time_create_destroy("busy VM");
    release_all(owners, n - 1);
  }
}

static void mode_leak(int nthreads) {
  printf("\n## Leaked vCPUs: %d threads exit without hv_vcpu_destroy\n",
         nthreads);
  static struct owner o[LEAK_THREADS];
  int ok = 0;
  hv_vcpu_t lo = ~0ULL, hi = 0;
  for (int i = 0; i < nthreads; i++) {
    owner_start(&o[i], 0);
    o[i].run_after_park = 1;
    owner_finish(&o[i]);
    if (o[i].create_ret != HV_SUCCESS) {
      printf("leak thread %d: hv_vcpu_create: %s\n", i,
             probe_strerror(o[i].create_ret));
      break;
    }
    if (o[i].run_ret != HV_SUCCESS ||
        o[i].run_reason != HV_EXIT_REASON_EXCEPTION) {
      printf("leak thread %d: run %s reason %u\n", i,
             probe_strerror(o[i].run_ret), o[i].run_reason);
    }
    if (o[i].vcpu < lo) lo = o[i].vcpu;
    if (o[i].vcpu > hi) hi = o[i].vcpu;
    ok++;
  }
  printf("threads that created, ran and exited without destroy: %d of %d; "
         "IDs %llu..%llu\n",
         ok, nthreads, lo, hi);

  if (ok > 0) {
    hv_vcpu_t leaked = o[ok - 1].vcpu;
    uint64_t x;
    printf("main thread on leaked ID %llu: get_reg %s, run %s, "
           "vcpus_exit %s, destroy %s\n",
           leaked, probe_strerror(hv_vcpu_get_reg(leaked, HV_REG_X0, &x)),
           probe_strerror(hv_vcpu_run(leaked)),
           probe_strerror(hv_vcpus_exit(&leaked, 1)),
           probe_strerror(hv_vcpu_destroy(leaked)));
  }

  static struct owner owners[MAX_THREADS + 1];
  hv_return_t fail;
  int n = fill_vcpus(owners, &fail);
  printf("concurrent vCPUs available after leaks: %d; next create: %s\n", n,
         probe_strerror(fail));
  release_all(owners, n);
  n = fill_vcpus(owners, &fail);
  printf("again, after destroying those: %d; next create: %s\n", n,
         probe_strerror(fail));
  release_all(owners, n);

  printf("hv_vm_destroy after leaks: %s\n", probe_strerror(hv_vm_destroy()));
  hv_return_t r = hv_vm_create(NULL);
  printf("second hv_vm_create: %s\n", probe_strerror(r));
  if (r == HV_SUCCESS) {
    n = fill_vcpus(owners, &fail);
    printf("concurrent vCPUs in second VM: %d; next create: %s\n", n,
           probe_strerror(fail));
    release_all(owners, n);
    printf("hv_vm_destroy of second VM: %s\n",
           probe_strerror(hv_vm_destroy()));
  }
}

int main(int argc, char** argv) {
  const char* mode = argc > 1 ? argv[1] : "";
  if (strcmp(mode, "cost") != 0 && strcmp(mode, "leak") != 0 &&
      strcmp(mode, "leak-one") != 0) {
    fprintf(stderr, "usage: %s cost|leak|leak-one\n", argv[0]);
    return 2;
  }
  probe_set_qos();
  char name[64];
  snprintf(name, sizeof(name), "vcpu_lifecycle %s", mode);
  probe_print_env(name);
  probe_vm_init();
  entry = probe_load_code(guest_clean, guest_clean_end, 0);
  if (strcmp(mode, "cost") == 0) {
    mode_cost();
    CHECK(hv_vm_destroy());
  } else {
    mode_leak(strcmp(mode, "leak") == 0 ? LEAK_THREADS : 1);
  }
  return 0;
}
