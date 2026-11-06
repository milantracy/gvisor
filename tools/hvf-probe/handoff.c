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

// handoff: Phase 0 probe 8. Hand-off latency between two host threads with
// spin-then-wait on os_sync_wait_on_address (macOS 14.4+), the primitive for
// a systrap-style hand-off between a task goroutine and a vCPU thread.
//
//   1. Ping-pong round trip with spin budgets: spin only, 10 us, 2 us, and
//      0 (always wait). pthread mutex/condvar ping-pong for comparison.
//   2. Wake latency of a thread blocked in os_sync_wait_on_address for 20 us
//      to 20 ms, with the waker asleep (both cores idle) or busy (only the
//      waiter's core idle).
//   3. Cost of os_sync_wake_by_address_any when nobody waits.
//
// No HVF calls; the binary is signed like the other probes.

#include <errno.h>
#include <mach/mach_time.h>
#include <os/os_sync_wait_on_address.h>
#include <pthread.h>
#include <stdatomic.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>

#include "probe.h"

#define PINGS 200000
#define PINGS_WAIT 50000

static uint64_t us_to_ticks(double us) {
  mach_timebase_info_data_t tb;
  mach_timebase_info(&tb);
  return (uint64_t)(us * 1000.0 * tb.denom / tb.numer);
}

// A word one thread waits on, plus a flag that says the waiter may be blocked
// in the kernel, so that the signaller only calls wake when needed.
struct slot {
  _Atomic uint64_t word;
  _Atomic uint32_t sleeping;
  uint64_t wakes;  // Number of wake calls made by the signaller.
  char pad[64];
};

static struct slot slots[2];
static uint64_t spin_ticks;  // Spin budget; UINT64_MAX = spin only.

// Waits until slots[me].word == want: spins for spin_ticks, then blocks.
static void wait_for(int me, uint64_t want) {
  struct slot* s = &slots[me];
  if (atomic_load_explicit(&s->word, memory_order_acquire) == want) return;
  if (spin_ticks) {
    uint64_t end = spin_ticks == UINT64_MAX ? UINT64_MAX
                                            : mach_absolute_time() + spin_ticks;
    while (mach_absolute_time() < end) {
      for (int i = 0; i < 64; i++) {
        if (atomic_load_explicit(&s->word, memory_order_acquire) == want) {
          return;
        }
      }
    }
  }
  atomic_store(&s->sleeping, 1);
  for (;;) {
    uint64_t cur = atomic_load(&s->word);
    if (cur == want) break;
    int r = os_sync_wait_on_address(&s->word, cur, 8,
                                    OS_SYNC_WAIT_ON_ADDRESS_NONE);
    if (r < 0 && errno != EINTR && errno != EFAULT) {
      perror("os_sync_wait_on_address");
      exit(1);
    }
  }
  atomic_store(&s->sleeping, 0);
}

static void signal_to(int other, uint64_t v) {
  struct slot* s = &slots[other];
  atomic_store(&s->word, v);
  if (atomic_load(&s->sleeping)) {
    s->wakes++;
    os_sync_wake_by_address_any(&s->word, 8, OS_SYNC_WAKE_BY_ADDRESS_NONE);
  }
}

static int iters;

static void* pong(void* arg) {
  (void)arg;
  for (int i = 1; i <= iters; i++) {
    wait_for(1, (uint64_t)i);
    signal_to(0, (uint64_t)i);
  }
  return NULL;
}

static void pingpong(const char* label, double spin_us, int n) {
  spin_ticks = spin_us < 0 ? UINT64_MAX : us_to_ticks(spin_us);
  memset(slots, 0, sizeof(slots));
  iters = n;
  uint64_t* rt = malloc(n * sizeof(uint64_t));
  pthread_t t = probe_spawn(pong, NULL);
  uint64_t t0 = mach_absolute_time();
  for (int i = 1; i <= n; i++) {
    uint64_t a = mach_absolute_time();
    signal_to(1, (uint64_t)i);
    wait_for(0, (uint64_t)i);
    rt[i - 1] = mach_absolute_time() - a;
  }
  double mean = probe_ticks_to_ns(mach_absolute_time() - t0) / n;
  pthread_join(t, NULL);
  printf("%-34s mean round trip %8.1f ns (one way %7.1f ns); wake calls "
         "%llu + %llu of %d\n",
         label, mean, mean / 2, slots[1].wakes, slots[0].wakes, n);
  probe_print_percentiles("  round trip", rt, n);
  free(rt);
}

// pthread mutex + condvar ping-pong.
static pthread_mutex_t mu = PTHREAD_MUTEX_INITIALIZER;
static pthread_cond_t cv[2] = {PTHREAD_COND_INITIALIZER,
                               PTHREAD_COND_INITIALIZER};
static uint64_t cv_word[2];

static void cv_wait(int me, uint64_t want) {
  pthread_mutex_lock(&mu);
  while (cv_word[me] != want) pthread_cond_wait(&cv[me], &mu);
  pthread_mutex_unlock(&mu);
}

static void cv_signal(int other, uint64_t v) {
  pthread_mutex_lock(&mu);
  cv_word[other] = v;
  pthread_cond_signal(&cv[other]);
  pthread_mutex_unlock(&mu);
}

static void* cv_pong(void* arg) {
  (void)arg;
  for (int i = 1; i <= iters; i++) {
    cv_wait(1, (uint64_t)i);
    cv_signal(0, (uint64_t)i);
  }
  return NULL;
}

static void cv_pingpong(int n) {
  cv_word[0] = cv_word[1] = 0;
  iters = n;
  pthread_t t = probe_spawn(cv_pong, NULL);
  uint64_t t0 = mach_absolute_time();
  for (int i = 1; i <= n; i++) {
    cv_signal(1, (uint64_t)i);
    cv_wait(0, (uint64_t)i);
  }
  double mean = probe_ticks_to_ns(mach_absolute_time() - t0) / n;
  pthread_join(t, NULL);
  printf("%-34s mean round trip %8.1f ns (one way %7.1f ns)\n",
         "pthread mutex+condvar", mean, mean / 2);
}

// Wake latency after idle: the waiter blocks (no spin); the waker sleeps for
// idle_us, then stamps the time and wakes it; the waiter stamps on return.
static _Atomic uint64_t t_woken;
static _Atomic int woken_ack;

static void* sleeper(void* arg) {
  (void)arg;
  for (int i = 1; i <= iters; i++) {
    wait_for(1, (uint64_t)i);
    atomic_store(&t_woken, mach_absolute_time());
    atomic_store(&woken_ack, i);
  }
  return NULL;
}

static void wake_after_idle(double idle_us, int n, int waker_busy) {
  spin_ticks = 0;
  memset(slots, 0, sizeof(slots));
  atomic_store(&woken_ack, 0);
  iters = n;
  uint64_t* lat = malloc(n * sizeof(uint64_t));
  pthread_t t = probe_spawn(sleeper, NULL);
  struct timespec ts = {(time_t)(idle_us / 1e6),
                        (long)((idle_us - (time_t)(idle_us / 1e6) * 1e6) *
                               1000)};
  int blocked = 0;
  for (int i = 1; i <= n; i++) {
    if (waker_busy) {
      uint64_t end = mach_absolute_time() + us_to_ticks(idle_us);
      while (mach_absolute_time() < end) {
      }
    } else {
      nanosleep(&ts, NULL);
    }
    blocked += atomic_load(&slots[1].sleeping) ? 1 : 0;
    uint64_t t0 = mach_absolute_time();
    signal_to(1, (uint64_t)i);
    while (atomic_load(&woken_ack) != i) {
    }
    lat[i - 1] = atomic_load(&t_woken) - t0;
  }
  pthread_join(t, NULL);
  char label[64];
  snprintf(label, sizeof(label), "%s %g us (%d/%d blkd)",
           waker_busy ? "busy" : "sleep", idle_us, blocked, n);
  probe_print_percentiles(label, lat, n);
  free(lat);
}

int main(void) {
  probe_set_qos();
  probe_print_env("handoff");
  // Warm up the CPU clock.
  uint64_t t0 = mach_absolute_time();
  while (probe_ticks_to_ns(mach_absolute_time() - t0) < 300e6) {
  }

  printf("\n## 1. Ping-pong between two threads\n");
  pingpong("spin only", -1, PINGS);
  pingpong("spin 10 us, then wait", 10, PINGS);
  pingpong("spin 2 us, then wait", 2, PINGS);
  pingpong("no spin (always wait)", 0, PINGS_WAIT);
  cv_pingpong(PINGS_WAIT);

  printf("\n## 2. Wake latency of a blocked thread "
         "(os_sync_wake_by_address_any -> waiter runs); waker asleep or busy "
         "for the idle period\n");
  for (int busy = 0; busy <= 1; busy++) {
    wake_after_idle(20, 2000, busy);
    wake_after_idle(200, 2000, busy);
    wake_after_idle(2000, 500, busy);
    wake_after_idle(20000, 100, busy);
  }

  printf("\n## 3. os_sync_wake_by_address_any with no waiter\n");
  _Atomic uint64_t w = 0;
  int n = 1000000;
  t0 = mach_absolute_time();
  int r = 0;
  for (int i = 0; i < n; i++) {
    r = os_sync_wake_by_address_any((void*)&w, 8, OS_SYNC_WAKE_BY_ADDRESS_NONE);
  }
  printf("%.1f ns/call (returns %d, errno %s)\n",
         probe_ticks_to_ns(mach_absolute_time() - t0) / n, r,
         r < 0 ? strerror(errno) : "-");
  return 0;
}
