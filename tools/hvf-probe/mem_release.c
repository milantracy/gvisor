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

// mem_release: Phase 0 probe 6. Releasing host memory that is mapped into
// the guest:
//
//   anon   MADV_FREE_REUSABLE and MADV_DONTNEED on anonymous memory, while it
//          is mapped into the guest and after hv_vm_unmap. Measured with
//          TASK_VM_INFO phys_footprint and with guest/host loads.
//   file   F_PUNCHHOLE on an APFS file while it is mapped (MAP_SHARED) into
//          the guest and after hv_vm_unmap. Measured with st_blocks and with
//          guest/host loads.
//   shm    MADV_FREE_REUSABLE and F_PUNCHHOLE on a shm_open object.
//   acct   Task memory accounting when one page is mapped by both the host
//          process and the guest, against the system free page count.
//   cost   Cost of hv_vm_unmap + MADV_FREE_REUSABLE, and of the remap on the
//          next guest fault (exit, MADV_FREE_REUSE, hv_vm_map, re-run).

#include <errno.h>
#include <fcntl.h>
#include <mach/mach.h>
#include <mach/mach_time.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/mman.h>
#include <sys/stat.h>
#include <unistd.h>

#include "probe.h"

#define PAGE 16384ULL
#define SIZE (64ULL << 20)
#define NPAGES (SIZE / PAGE)
#define IPA 0x20000000ULL

static hv_vcpu_t vcpu;
static hv_vcpu_exit_t* vexit;
static uint64_t entry;

static double mib(uint64_t b) { return (double)b / (1 << 20); }

static void* map_or_die(size_t size, int prot, int flags, int fd) {
  void* p = mmap(NULL, size, prot, flags, fd, 0);
  if (p == MAP_FAILED) {
    perror("mmap");
    exit(1);
  }
  return p;
}

static void warm_up(int ms) {
  uint64_t v, esr, t0 = mach_absolute_time();
  while (probe_ticks_to_ns(mach_absolute_time() - t0) < ms * 1e6) {
    probe_guest_access(vcpu, vexit, entry, 0, GUEST_BASE + GUEST_SIZE / 2, 0,
                       &v, &esr, NULL);
  }
}

// Guest stores a per-page pattern to every page of the mapping at IPA.
static void guest_fill(uint64_t tag) {
  for (uint64_t i = 0; i < NPAGES; i++) {
    uint64_t esr;
    if (probe_guest_access(vcpu, vexit, entry, 1, IPA + i * PAGE, tag + i,
                           NULL, &esr, NULL) != ACCESS_OK) {
      printf("guest_fill: page %llu faulted (esr 0x%llx)\n", i, esr);
      exit(1);
    }
  }
}

// Guest loads every page. Counts pages that still hold tag+i, are zero, or
// fault.
static void guest_check(const char* what, uint64_t tag) {
  uint64_t old = 0, zero = 0, other = 0, fault = 0;
  for (uint64_t i = 0; i < NPAGES; i++) {
    uint64_t v = 0, esr;
    enum access_result r = probe_guest_access(vcpu, vexit, entry, 0,
                                              IPA + i * PAGE, 0, &v, &esr,
                                              NULL);
    if (r != ACCESS_OK) {
      fault++;
    } else if (v == tag + i) {
      old++;
    } else if (v == 0) {
      zero++;
    } else {
      other++;
    }
  }
  printf("  %-44s guest sees: old %llu, zero %llu, other %llu, fault %llu\n",
         what, old, zero, other, fault);
}

static void host_check(const char* what, const uint8_t* p, uint64_t tag) {
  uint64_t old = 0, zero = 0, other = 0;
  for (uint64_t i = 0; i < NPAGES; i++) {
    uint64_t v;
    memcpy(&v, p + i * PAGE, 8);
    if (v == tag + i) {
      old++;
    } else if (v == 0) {
      zero++;
    } else {
      other++;
    }
  }
  printf("  %-44s host sees:  old %llu, zero %llu, other %llu\n", what, old,
         zero, other);
}

static void footprint(const char* what) {
  printf("  %-44s phys_footprint %.1f MiB\n", what,
         mib(probe_phys_footprint()));
}

static void blocks(const char* what, int fd) {
  struct stat st;
  fstat(fd, &st);
  printf("  %-44s st_blocks %.1f MiB, st_size %.1f MiB\n", what,
         mib((uint64_t)st.st_blocks * 512), mib((uint64_t)st.st_size));
}

static int madv(uint8_t* p, int advice, const char* name) {
  int r = madvise(p, SIZE, advice);
  printf("  madvise(%s): %s\n", name, r == 0 ? "ok" : strerror(errno));
  return r;
}

static int punch(int fd, off_t off, off_t len) {
  struct fpunchhole ph = {.fp_flags = 0, .fp_offset = off, .fp_length = len};
  int r = fcntl(fd, F_PUNCHHOLE, &ph);
  printf("  F_PUNCHHOLE: %s\n", r == 0 ? "ok" : strerror(errno));
  return r;
}

static void anon_case(int advice, const char* name, int unmap_first) {
  printf("\n### anonymous MAP_PRIVATE, %s, %s\n", name,
         unmap_first ? "after hv_vm_unmap" : "while mapped into the guest");
  uint8_t* p = map_or_die(SIZE, PROT_READ | PROT_WRITE, MAP_ANON | MAP_PRIVATE,
                          -1);
  footprint("before hv_vm_map");
  CHECK(hv_vm_map(p, IPA, SIZE, HV_MEMORY_READ | HV_MEMORY_WRITE));
  guest_fill(0x1000);
  footprint("after guest wrote 64 MiB");
  if (unmap_first) CHECK(hv_vm_unmap(IPA, SIZE));
  madv(p, advice, name);
  footprint("after madvise");
  host_check("after madvise", p, 0x1000);
  footprint("after host read");
  if (unmap_first) {
    if (advice == MADV_FREE_REUSABLE) madv(p, MADV_FREE_REUSE, "REUSE");
    CHECK(hv_vm_map(p, IPA, SIZE, HV_MEMORY_READ | HV_MEMORY_WRITE));
  }
  guest_check("after madvise", 0x1000);
  footprint("after guest read");
  guest_fill(0x2000);
  footprint("after guest wrote 64 MiB again");
  host_check("after guest rewrite", p, 0x2000);
  CHECK(hv_vm_unmap(IPA, SIZE));
  munmap(p, SIZE);
  footprint("after hv_vm_unmap + munmap");
}

static void mode_anon(void) {
  anon_case(MADV_FREE_REUSABLE, "MADV_FREE_REUSABLE", 0);
  anon_case(MADV_FREE_REUSABLE, "MADV_FREE_REUSABLE", 1);
  anon_case(MADV_DONTNEED, "MADV_DONTNEED", 0);
  anon_case(MADV_DONTNEED, "MADV_DONTNEED", 1);
}

static int make_file(void) {
  const char* d = getenv("TMPDIR");
  char path[1024];
  snprintf(path, sizeof(path), "%s/hvf-probe-XXXXXX", d && *d ? d : "/tmp");
  int fd = mkstemp(path);
  if (fd < 0) {
    perror("mkstemp");
    exit(1);
  }
  unlink(path);
  if (ftruncate(fd, SIZE) != 0) {
    perror("ftruncate");
    exit(1);
  }
  return fd;
}

static void file_case(int unmap_first) {
  printf("\n### file MAP_SHARED, F_PUNCHHOLE of the whole file, %s\n",
         unmap_first ? "after hv_vm_unmap" : "while mapped into the guest");
  int fd = make_file();
  uint8_t* p = map_or_die(SIZE, PROT_READ | PROT_WRITE, MAP_SHARED, fd);
  CHECK(hv_vm_map(p, IPA, SIZE, HV_MEMORY_READ | HV_MEMORY_WRITE));
  guest_fill(0x1000);
  if (msync(p, SIZE, MS_SYNC) != 0) perror("msync");
  blocks("after guest wrote 64 MiB + msync", fd);
  footprint("after guest wrote 64 MiB");
  if (unmap_first) CHECK(hv_vm_unmap(IPA, SIZE));
  punch(fd, 0, SIZE);
  blocks("after F_PUNCHHOLE", fd);
  host_check("after F_PUNCHHOLE", p, 0x1000);
  uint64_t v = 1;
  if (pread(fd, &v, 8, 0) != 8) perror("pread");
  printf("  %-44s pread page 0: 0x%llx\n", "after F_PUNCHHOLE", v);
  if (unmap_first) {
    CHECK(hv_vm_map(p, IPA, SIZE, HV_MEMORY_READ | HV_MEMORY_WRITE));
  }
  guest_check("after F_PUNCHHOLE", 0x1000);
  if (msync(p, SIZE, MS_SYNC) != 0) perror("msync");
  blocks("after guest read + msync", fd);
  guest_fill(0x2000);
  if (msync(p, SIZE, MS_SYNC) != 0) perror("msync");
  blocks("after guest rewrite + msync", fd);
  host_check("after guest rewrite", p, 0x2000);
  CHECK(hv_vm_unmap(IPA, SIZE));
  munmap(p, SIZE);
  close(fd);
}

static void mode_file(void) {
  file_case(0);
  file_case(1);
}

static void mode_shm(void) {
  printf("\n### shm_open MAP_SHARED, while mapped into the guest\n");
  char name[64];
  snprintf(name, sizeof(name), "/hvfprobe.%d", getpid());
  int fd = shm_open(name, O_RDWR | O_CREAT | O_EXCL, 0600);
  if (fd < 0) {
    perror("shm_open");
    return;
  }
  shm_unlink(name);
  if (ftruncate(fd, SIZE) != 0) perror("ftruncate");
  uint8_t* p = map_or_die(SIZE, PROT_READ | PROT_WRITE, MAP_SHARED, fd);
  footprint("before hv_vm_map");
  CHECK(hv_vm_map(p, IPA, SIZE, HV_MEMORY_READ | HV_MEMORY_WRITE));
  guest_fill(0x1000);
  footprint("after guest wrote 64 MiB");
  punch(fd, 0, SIZE);
  madv(p, MADV_FREE_REUSABLE, "MADV_FREE_REUSABLE");
  footprint("after madvise");
  guest_check("after madvise", 0x1000);
  CHECK(hv_vm_unmap(IPA, SIZE));
  madv(p, MADV_FREE_REUSABLE, "MADV_FREE_REUSABLE after hv_vm_unmap");
  footprint("after hv_vm_unmap + madvise");
  host_check("after hv_vm_unmap + madvise", p, 0x1000);
  munmap(p, SIZE);
  close(fd);
  footprint("after munmap + close");
}

static void print_acct(const char* what) {
  task_vm_info_data_t i;
  mach_msg_type_number_t c = TASK_VM_INFO_COUNT;
  task_info(mach_task_self(), TASK_VM_INFO, (task_info_t)&i, &c);
  vm_statistics64_data_t vs;
  mach_msg_type_number_t vc = HOST_VM_INFO64_COUNT;
  host_statistics64(mach_host_self(), HOST_VM_INFO64, (host_info64_t)&vs, &vc);
  printf("  %-36s phys_footprint %6.1f  resident %6.1f  internal %6.1f  "
         "| system free %.0f MiB\n",
         what, mib(i.phys_footprint), mib(i.resident_size), mib(i.internal),
         mib((uint64_t)vs.free_count * vm_page_size));
}

static void guest_touch_all(int store) {
  for (uint64_t i = 0; i < NPAGES; i++) {
    uint64_t v, esr;
    probe_guest_access(vcpu, vexit, entry, store, IPA + i * PAGE, 9, &v, &esr,
                       NULL);
  }
}

static void mode_acct(void) {
  printf("\n### A: host touches 64 MiB first, then the guest\n");
  print_acct("start");
  uint8_t* a = map_or_die(SIZE, PROT_READ | PROT_WRITE, MAP_ANON | MAP_PRIVATE,
                          -1);
  memset(a, 1, SIZE);
  print_acct("host wrote 64 MiB");
  CHECK(hv_vm_map(a, IPA, SIZE, HV_MEMORY_READ | HV_MEMORY_WRITE));
  print_acct("hv_vm_map");
  guest_touch_all(0);
  print_acct("guest read all pages");
  CHECK(hv_vm_unmap(IPA, SIZE));
  print_acct("hv_vm_unmap");
  munmap(a, SIZE);
  print_acct("munmap");

  printf("\n### B: guest touches 64 MiB first, then the host\n");
  uint8_t* b = map_or_die(SIZE, PROT_READ | PROT_WRITE, MAP_ANON | MAP_PRIVATE,
                          -1);
  CHECK(hv_vm_map(b, IPA, SIZE, HV_MEMORY_READ | HV_MEMORY_WRITE));
  guest_touch_all(1);
  print_acct("guest wrote all pages");
  volatile uint64_t sum = 0;
  for (uint64_t o = 0; o < SIZE; o += PAGE) sum += *(volatile uint64_t*)(b + o);
  print_acct("host read all pages");
  CHECK(hv_vm_unmap(IPA, SIZE));
  print_acct("hv_vm_unmap");
  munmap(b, SIZE);
  print_acct("munmap");
}

// Release and remap-on-fault cost, per chunk of chunk bytes.
static void cost_case(size_t chunk) {
  size_t n = SIZE / chunk;
  uint8_t* p = map_or_die(SIZE, PROT_READ | PROT_WRITE, MAP_ANON | MAP_PRIVATE,
                          -1);
  CHECK(hv_vm_map(p, IPA, SIZE, HV_MEMORY_READ | HV_MEMORY_WRITE));
  guest_fill(0x1000);
  uint64_t* t_unmap = malloc(n * 8);
  uint64_t* t_madv = malloc(n * 8);
  uint64_t* t_fault = malloc(n * 8);
  uint64_t* t_reuse = malloc(n * 8);
  uint64_t* t_map = malloc(n * 8);
  uint64_t* t_rerun = malloc(n * 8);
  uint64_t* t_total = malloc(n * 8);
  for (size_t i = 0; i < n; i++) {
    uint64_t ipa = IPA + i * chunk;
    uint8_t* h = p + i * chunk;
    uint64_t t0 = mach_absolute_time();
    CHECK(hv_vm_unmap(ipa, chunk));
    uint64_t t1 = mach_absolute_time();
    if (madvise(h, chunk, MADV_FREE_REUSABLE) != 0) perror("madvise");
    uint64_t t2 = mach_absolute_time();
    t_unmap[i] = t1 - t0;
    t_madv[i] = t2 - t1;
  }
  for (size_t i = 0; i < n; i++) {
    uint64_t ipa = IPA + i * chunk, v, esr, fipa;
    uint8_t* h = p + i * chunk;
    uint64_t t0 = mach_absolute_time();
    enum access_result r = probe_guest_access(vcpu, vexit, entry, 1, ipa, 7,
                                              &v, &esr, &fipa);
    uint64_t t1 = mach_absolute_time();
    if (r != ACCESS_FAULT || fipa != ipa) {
      printf("expected a fault at 0x%llx, got %d at 0x%llx\n", ipa, r, fipa);
      exit(1);
    }
    if (madvise(h, chunk, MADV_FREE_REUSE) != 0) perror("madvise REUSE");
    uint64_t t2 = mach_absolute_time();
    CHECK(hv_vm_map(h, ipa, chunk, HV_MEMORY_READ | HV_MEMORY_WRITE));
    uint64_t t3 = mach_absolute_time();
    // Resume at the faulting str; it re-executes and exits with HVC.
    CHECK(hv_vcpu_run(vcpu));
    uint64_t t4 = mach_absolute_time();
    probe_expect_hvc(vcpu, vexit, PROBE_HVC_IMM);
    t_fault[i] = t1 - t0;
    t_reuse[i] = t2 - t1;
    t_map[i] = t3 - t2;
    t_rerun[i] = t4 - t3;
    t_total[i] = t4 - t0;
  }
  printf("\n### chunk %zu KiB, %zu chunks\n", chunk >> 10, n);
  probe_print_percentiles("release: hv_vm_unmap", t_unmap, n);
  probe_print_percentiles("release: MADV_FREE_REUSABLE", t_madv, n);
  probe_print_percentiles("refault: run -> fault exit", t_fault, n);
  probe_print_percentiles("refault: MADV_FREE_REUSE", t_reuse, n);
  probe_print_percentiles("refault: hv_vm_map", t_map, n);
  probe_print_percentiles("refault: re-run, store, HVC", t_rerun, n);
  probe_print_percentiles("refault: total", t_total, n);
  CHECK(hv_vm_unmap(IPA, SIZE));
  munmap(p, SIZE);
  free(t_unmap);
  free(t_madv);
  free(t_fault);
  free(t_reuse);
  free(t_map);
  free(t_rerun);
  free(t_total);
}

static void mode_cost(void) {
  warm_up(300);
  cost_case(PAGE);
  cost_case(2 << 20);
}

int main(int argc, char** argv) {
  const char* mode = argc > 1 ? argv[1] : "";
  void (*fn)(void) = NULL;
  if (strcmp(mode, "anon") == 0) fn = mode_anon;
  if (strcmp(mode, "file") == 0) fn = mode_file;
  if (strcmp(mode, "shm") == 0) fn = mode_shm;
  if (strcmp(mode, "acct") == 0) fn = mode_acct;
  if (strcmp(mode, "cost") == 0) fn = mode_cost;
  if (!fn) {
    fprintf(stderr, "usage: %s anon|file|shm|acct|cost\n", argv[0]);
    return 2;
  }
  probe_set_qos();
  char name[64];
  snprintf(name, sizeof(name), "mem_release %s", mode);
  probe_print_env(name);
  probe_vm_init();
  entry = probe_load_code(guest_memop, guest_memop_end, 0);
  CHECK(hv_vcpu_create(&vcpu, &vexit, NULL));
  probe_vcpu_reset(vcpu, entry);
  fn();
  return 0;
}
