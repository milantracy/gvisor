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

// vm_map: Phase 0 probe 5. hv_vm_map with different host backings.
//
// Modes (each in its own process):
//   main      Anonymous private/shared, file MAP_SHARED RW and RO, shm_open,
//             aliases (one host page at two IPAs), alignment and overlap
//             rules, hv_vm_protect, partial unmap, and costs of map, unmap,
//             protect and first guest touch.
//   eof       File mapping that extends past EOF, mapped into the guest.
//   truncate  File shrunk with ftruncate while it is mapped into the guest.
//   munmap    Host munmap of memory that is still mapped into the guest.
//   reserve   hv_vm_map of host VA that is a PROT_NONE reservation or has no
//             host mapping, and of host memory that appears there later.

#include <errno.h>
#include <fcntl.h>
#include <mach/mach_time.h>
#include <signal.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/mman.h>
#include <sys/stat.h>
#include <unistd.h>

#include "probe.h"

#define PAGE 16384ULL
#define REGION (4 * PAGE)
#define TEST_IPA 0x20000000ULL  // Inside the stage-1 identity block.

static hv_vcpu_t vcpu;
static hv_vcpu_exit_t* vexit;
static uint64_t entry;

static const char* tmpdir(void) {
  const char* d = getenv("TMPDIR");
  return d && *d ? d : "/tmp";
}

static int make_file(size_t size) {
  char path[1024];
  snprintf(path, sizeof(path), "%s/hvf-probe-XXXXXX", tmpdir());
  int fd = mkstemp(path);
  if (fd < 0) {
    perror("mkstemp");
    exit(1);
  }
  unlink(path);
  if (ftruncate(fd, (off_t)size) != 0) {
    perror("ftruncate");
    exit(1);
  }
  return fd;
}

static void* map_or_die(size_t size, int prot, int flags, int fd) {
  void* p = mmap(NULL, size, prot, flags, fd, 0);
  if (p == MAP_FAILED) {
    perror("mmap");
    exit(1);
  }
  return p;
}

static const char* access_str(enum access_result r, uint64_t esr) {
  static char buf[96];
  switch (r) {
    case ACCESS_OK: return "ok";
    case ACCESS_FAULT:
      snprintf(buf, sizeof(buf), "stage-2 fault (DFSC 0x%llx, WnR %llu)",
               esr & 0x3f, (esr >> 6) & 1);
      return buf;
    default:
      snprintf(buf, sizeof(buf), "other exit (EC 0x%llx)", ESR_EC(esr));
      return buf;
  }
}

static enum access_result gload(uint64_t ipa, uint64_t* v, uint64_t* esr) {
  return probe_guest_access(vcpu, vexit, entry, 0, ipa, 0, v, esr, NULL);
}

static enum access_result gstore(uint64_t ipa, uint64_t v, uint64_t* esr) {
  return probe_guest_access(vcpu, vexit, entry, 1, ipa, v, NULL, esr, NULL);
}

// Maps [host, host+REGION) at ipa and checks host->guest and guest->host
// visibility. fd >= 0 also checks visibility through pread/pwrite.
static void test_backing(const char* name, uint8_t* host, int fd,
                         int host_writable, hv_memory_flags_t flags,
                         uint64_t ipa) {
  printf("\n### %s\n", name);
  hv_return_t r = hv_vm_map(host, ipa, REGION, flags);
  printf("hv_vm_map: %s\n", probe_strerror(r));
  if (r != HV_SUCCESS) return;

  uint64_t v = 0, esr = 0, hv = 0x1111;
  if (host_writable) {
    memcpy(host, &hv, 8);
  } else if (fd >= 0) {
    if (pwrite(fd, &hv, 8, 0) != 8) perror("pwrite");
  }
  enum access_result a = gload(ipa, &v, &esr);
  printf("host write 0x1111, guest load: %s, value 0x%llx\n",
         access_str(a, esr), v);

  a = gstore(ipa + 8, 0x2222, &esr);
  uint64_t hv8 = 0, pv8 = 0;
  memcpy(&hv8, host + 8, 8);
  if (fd >= 0 && pread(fd, &pv8, 8, 8) != 8) perror("pread");
  printf("guest store 0x2222: %s; host mapping sees 0x%llx", access_str(a, esr),
         hv8);
  if (fd >= 0) printf(", pread sees 0x%llx", pv8);
  printf("\n");

  if (fd >= 0) {
    uint64_t w = 0x3333;
    if (pwrite(fd, &w, 8, 16) != 8) perror("pwrite");
    a = gload(ipa + 16, &v, &esr);
    printf("pwrite 0x3333 after hv_vm_map, guest load: %s, value 0x%llx\n",
           access_str(a, esr), v);
  }
  CHECK(hv_vm_unmap(ipa, REGION));
}

static void test_aliases(void) {
  printf("\n### Aliases\n");
  uint8_t* a = map_or_die(REGION, PROT_READ | PROT_WRITE,
                          MAP_ANON | MAP_SHARED, -1);
  uint64_t x = TEST_IPA + 0x1000000, y = TEST_IPA + 0x1100000;
  CHECK(hv_vm_map(a, x, REGION, HV_MEMORY_READ | HV_MEMORY_WRITE));
  hv_return_t r = hv_vm_map(a, y, REGION, HV_MEMORY_READ | HV_MEMORY_WRITE);
  printf("same host VA at a second IPA: %s\n", probe_strerror(r));
  if (r == HV_SUCCESS) {
    uint64_t v = 0, esr;
    gstore(x, 0xa1a1, &esr);
    enum access_result ar = gload(y, &v, &esr);
    printf("  store via IPA 1, load via IPA 2: %s, value 0x%llx\n",
           access_str(ar, esr), v);
    CHECK(hv_vm_unmap(y, REGION));
  }
  CHECK(hv_vm_unmap(x, REGION));

  int fd = make_file(REGION);
  uint8_t* m1 = map_or_die(REGION, PROT_READ | PROT_WRITE, MAP_SHARED, fd);
  uint8_t* m2 = map_or_die(REGION, PROT_READ | PROT_WRITE, MAP_SHARED, fd);
  CHECK(hv_vm_map(m1, x, REGION, HV_MEMORY_READ | HV_MEMORY_WRITE));
  r = hv_vm_map(m2, y, REGION, HV_MEMORY_READ | HV_MEMORY_WRITE);
  printf("two host mappings of one file range at two IPAs: %s\n",
         probe_strerror(r));
  if (r == HV_SUCCESS) {
    uint64_t v = 0, esr, h2 = 0;
    gstore(x + 24, 0xb2b2, &esr);
    enum access_result ar = gload(y + 24, &v, &esr);
    memcpy(&h2, m2 + 24, 8);
    printf("  store via IPA 1, load via IPA 2: %s, value 0x%llx; host "
           "mapping 2 sees 0x%llx\n",
           access_str(ar, esr), v, h2);
    CHECK(hv_vm_unmap(y, REGION));
  }
  CHECK(hv_vm_unmap(x, REGION));
  munmap(m1, REGION);
  munmap(m2, REGION);
  close(fd);
}

static void test_rules(void) {
  printf("\n### Alignment, overlap, partial unmap, protect\n");
  uint8_t* a = map_or_die(2 * REGION, PROT_READ | PROT_WRITE,
                          MAP_ANON | MAP_PRIVATE, -1);
  memset(a, 0, 2 * REGION);
  hv_memory_flags_t rw = HV_MEMORY_READ | HV_MEMORY_WRITE;
  uint64_t x = TEST_IPA + 0x2000000;
  printf("host VA +4 KiB:       %s\n",
         probe_strerror(hv_vm_map(a + 4096, x, PAGE, rw)));
  printf("IPA +4 KiB:           %s\n",
         probe_strerror(hv_vm_map(a, x + 4096, PAGE, rw)));
  printf("size 4 KiB:           %s\n",
         probe_strerror(hv_vm_map(a, x, 4096, rw)));
  printf("flags 0 (no access):  %s\n",
         probe_strerror(hv_vm_map(a, x, PAGE, 0)));
  hv_vm_unmap(x, PAGE);

  CHECK(hv_vm_map(a, x, REGION, rw));
  printf("map over a mapped IPA range: %s\n",
         probe_strerror(hv_vm_map(a + REGION, x + PAGE, PAGE, rw)));
  printf("unmap page 1 of 4: %s\n",
         probe_strerror(hv_vm_unmap(x + PAGE, PAGE)));
  uint64_t v, esr, ipa;
  enum access_result r0 = gload(x, &v, &esr);
  enum access_result r1 = probe_guest_access(vcpu, vexit, entry, 0, x + PAGE,
                                             0, &v, &esr, &ipa);
  printf("after partial unmap: page 0 %s; page 1 ", access_str(r0, 0));
  printf("%s, exit IPA 0x%llx\n", access_str(r1, esr), ipa);
  printf("remap page 1 from another host page: %s\n",
         probe_strerror(hv_vm_map(a + REGION, x + PAGE, PAGE, rw)));
  r1 = gload(x + PAGE, &v, &esr);
  printf("  guest load page 1: %s\n", access_str(r1, esr));
  printf("unmap a range with a hole (pages 0-3, page 1 was remapped): %s\n",
         probe_strerror(hv_vm_unmap(x, REGION)));
  hv_vm_unmap(x, PAGE);
  hv_vm_unmap(x + PAGE, PAGE);
  hv_vm_unmap(x + 2 * PAGE, 2 * PAGE);

  CHECK(hv_vm_map(a, x, REGION, rw));
  CHECK(hv_vm_protect(x, PAGE, HV_MEMORY_READ));
  uint64_t esr_w, esr_l;
  enum access_result w = gstore(x, 1, &esr_w);
  enum access_result l = gload(x, &v, &esr_l);
  printf("hv_vm_protect READ: guest store %s; ", access_str(w, esr_w));
  printf("guest load %s\n", access_str(l, esr_l));
  CHECK(hv_vm_protect(x, PAGE, 0));
  l = gload(x, &v, &esr);
  printf("hv_vm_protect none: guest load %s\n", access_str(l, esr));
  CHECK(hv_vm_protect(x, PAGE, HV_MEMORY_READ | HV_MEMORY_WRITE));
  w = gstore(x, 1, &esr);
  printf("hv_vm_protect back to RW: guest store %s\n", access_str(w, esr));
  CHECK(hv_vm_unmap(x, REGION));
  munmap(a, 2 * REGION);
}

// Runs guest loads for ms milliseconds so that later timings are not taken
// while the CPU clock ramps up.
static void warm_up(int ms) {
  uint64_t v, esr, t0 = mach_absolute_time();
  while (probe_ticks_to_ns(mach_absolute_time() - t0) < ms * 1e6) {
    gload(GUEST_BASE + GUEST_SIZE / 2, &v, &esr);
  }
}

static void test_costs(void) {
  warm_up(300);
  printf("\n### Costs (host memory touched before mapping)\n");
  const size_t sizes[] = {PAGE, 2 << 20, 64 << 20};
  const int reps[] = {2000, 500, 50};
  size_t max = 64 << 20;
  uint8_t* a = map_or_die(max, PROT_READ | PROT_WRITE, MAP_ANON | MAP_PRIVATE,
                          -1);
  memset(a, 1, max);
  uint64_t x = TEST_IPA + 0x8000000;
  for (int s = 0; s < 3; s++) {
    uint64_t tm = 0, tu = 0, tp = 0;
    for (int i = 0; i < reps[s]; i++) {
      uint64_t t0 = mach_absolute_time();
      CHECK(hv_vm_map(a, x, sizes[s], HV_MEMORY_READ | HV_MEMORY_WRITE));
      uint64_t t1 = mach_absolute_time();
      CHECK(hv_vm_protect(x, sizes[s], HV_MEMORY_READ));
      uint64_t t2 = mach_absolute_time();
      CHECK(hv_vm_unmap(x, sizes[s]));
      uint64_t t3 = mach_absolute_time();
      tm += t1 - t0;
      tp += t2 - t1;
      tu += t3 - t2;
    }
    printf("size %8zu KiB: hv_vm_map %9.1f ns, hv_vm_protect %9.1f ns, "
           "hv_vm_unmap %9.1f ns (mean of %d)\n",
           sizes[s] >> 10, probe_ticks_to_ns(tm) / reps[s],
           probe_ticks_to_ns(tp) / reps[s], probe_ticks_to_ns(tu) / reps[s],
           reps[s]);
  }
  munmap(a, max);

  printf("\n### Guest access cost per 16 KiB page, 64 MiB region (one "
         "access + HVC exit each, mean over 4096 pages)\n");
  size_t sz = 64 << 20, n = sz / PAGE;
  const struct {
    const char* label;
    int host_touch, store;
  } cases[] = {
      {"host-touched, guest load", 1, 0},
      {"untouched, guest load", 0, 0},
      {"untouched, guest store", 0, 1},
  };
  for (int c = 0; c < 3; c++) {
    a = map_or_die(sz, PROT_READ | PROT_WRITE, MAP_ANON | MAP_PRIVATE, -1);
    if (cases[c].host_touch) memset(a, 1, sz);
    CHECK(hv_vm_map(a, x, sz, HV_MEMORY_READ | HV_MEMORY_WRITE));
    for (int pass = 0; pass < 2; pass++) {
      uint64_t t0 = mach_absolute_time();
      for (size_t i = 0; i < n; i++) {
        uint64_t v, esr;
        enum access_result r = probe_guest_access(
            vcpu, vexit, entry, cases[c].store, x + i * PAGE, 7, &v, &esr,
            NULL);
        if (r != ACCESS_OK) {
          printf("access %zu: %s\n", i, access_str(r, esr));
          break;
        }
      }
      printf("%-28s %s: %7.1f ns/access\n", cases[c].label,
             pass == 0 ? "first touch" : "second pass",
             probe_ticks_to_ns(mach_absolute_time() - t0) / n);
    }
    uint64_t v, esr, t0 = mach_absolute_time();
    for (int i = 0; i < 4096; i++) gload(x, &v, &esr);
    printf("%-28s one page x4096: %7.1f ns/access\n", cases[c].label,
           probe_ticks_to_ns(mach_absolute_time() - t0) / 4096);
    CHECK(hv_vm_unmap(x, sz));
    munmap(a, sz);
  }
}

static void mode_main(void) {
  uint64_t ipa = TEST_IPA;
  hv_memory_flags_t rw = HV_MEMORY_READ | HV_MEMORY_WRITE;

  uint8_t* p = map_or_die(REGION, PROT_READ | PROT_WRITE,
                          MAP_ANON | MAP_PRIVATE, -1);
  test_backing("anonymous MAP_PRIVATE, RW", p, -1, 1, rw, ipa);
  munmap(p, REGION);

  p = map_or_die(REGION, PROT_READ | PROT_WRITE, MAP_ANON | MAP_SHARED, -1);
  test_backing("anonymous MAP_SHARED, RW", p, -1, 1, rw, ipa);
  munmap(p, REGION);

  int fd = make_file(REGION);
  p = map_or_die(REGION, PROT_READ | PROT_WRITE, MAP_SHARED, fd);
  test_backing("file MAP_SHARED, host RW, guest RW", p, fd, 1, rw, ipa);
  munmap(p, REGION);
  close(fd);

  fd = make_file(REGION);
  p = map_or_die(REGION, PROT_READ, MAP_SHARED, fd);
  test_backing("file MAP_SHARED, host PROT_READ, guest READ", p, fd, 0,
               HV_MEMORY_READ, ipa);
  test_backing("file MAP_SHARED, host PROT_READ, guest READ|WRITE", p, fd, 0,
               rw, ipa);
  munmap(p, REGION);
  close(fd);

  // A file the process can only read: O_RDONLY descriptor, mode 0444.
  char path[1024];
  snprintf(path, sizeof(path), "%s/hvf-probe-ro-XXXXXX", tmpdir());
  fd = mkstemp(path);
  if (fd < 0 || ftruncate(fd, REGION) != 0) {
    perror("ro file");
    exit(1);
  }
  close(fd);
  chmod(path, 0444);
  fd = open(path, O_RDONLY);
  unlink(path);
  p = map_or_die(REGION, PROT_READ, MAP_SHARED, fd);
  test_backing("file O_RDONLY (mode 0444) MAP_SHARED, guest READ", p, -1, 0,
               HV_MEMORY_READ, ipa);
  test_backing("file O_RDONLY (mode 0444) MAP_SHARED, guest READ|WRITE", p,
               -1, 0, rw, ipa);
  munmap(p, REGION);
  p = map_or_die(REGION, PROT_READ, MAP_PRIVATE, fd);
  test_backing("file O_RDONLY (mode 0444) MAP_PRIVATE, guest READ|WRITE", p,
               -1, 0, rw, ipa);
  uint64_t fv = 1;
  if (pread(fd, &fv, 8, 8) != 8) perror("pread");
  printf("file at offset 8 after guest store: 0x%llx\n", fv);
  munmap(p, REGION);
  close(fd);

  fd = make_file(REGION);
  p = map_or_die(REGION, PROT_READ | PROT_WRITE, MAP_PRIVATE, fd);
  test_backing("file MAP_PRIVATE, host RW, guest RW", p, fd, 1, rw, ipa);
  munmap(p, REGION);
  close(fd);

  char name[64];
  snprintf(name, sizeof(name), "/hvfprobe.%d", getpid());
  fd = shm_open(name, O_RDWR | O_CREAT | O_EXCL, 0600);
  if (fd < 0) {
    perror("shm_open");
  } else {
    shm_unlink(name);
    if (ftruncate(fd, REGION) != 0) perror("ftruncate shm");
    p = map_or_die(REGION, PROT_READ | PROT_WRITE, MAP_SHARED, fd);
    test_backing("shm_open MAP_SHARED, RW", p, -1, 1, rw, ipa);
    munmap(p, REGION);
    close(fd);
  }

  test_aliases();
  test_rules();
  test_costs();
}

// The edge-case modes may hang the guest; give up after 10 s.
static void on_alarm(int sig) {
  (void)sig;
  static const char msg[] = "TIMEOUT: no exit within 10 s\n";
  write(1, msg, sizeof(msg) - 1);
  _exit(3);
}

static void report_access(const char* what, enum access_result r,
                          uint64_t esr, uint64_t v) {
  printf("%s: %s", what, access_str(r, esr));
  if (r == ACCESS_OK) printf(", value 0x%llx", v);
  printf("\n");
  fflush(stdout);
}

static void mode_eof(void) {
  int fd = make_file(PAGE);  // One page of file, four pages of mapping.
  uint8_t* p = map_or_die(REGION, PROT_READ | PROT_WRITE, MAP_SHARED, fd);
  hv_return_t r = hv_vm_map(p, TEST_IPA, REGION,
                            HV_MEMORY_READ | HV_MEMORY_WRITE);
  printf("hv_vm_map of 4 pages, file is 1 page: %s\n", probe_strerror(r));
  fflush(stdout);
  if (r != HV_SUCCESS) return;
  uint64_t v = 0, esr = 0;
  report_access("guest load page 0 (inside file)", gload(TEST_IPA, &v, &esr),
                esr, v);
  report_access("guest load page 2 (past EOF)",
                gload(TEST_IPA + 2 * PAGE, &v, &esr), esr, v);
  report_access("guest store page 2 (past EOF)",
                gstore(TEST_IPA + 2 * PAGE, 5, &esr), esr, 0);
  struct stat st;
  fstat(fd, &st);
  printf("file size afterwards: %lld\n", (long long)st.st_size);
}

static void mode_truncate(void) {
  int fd = make_file(REGION);
  uint8_t* p = map_or_die(REGION, PROT_READ | PROT_WRITE, MAP_SHARED, fd);
  memset(p, 0x5a, REGION);
  CHECK(hv_vm_map(p, TEST_IPA, REGION, HV_MEMORY_READ | HV_MEMORY_WRITE));
  uint64_t v = 0, esr = 0;
  report_access("before truncate, guest load page 3",
                gload(TEST_IPA + 3 * PAGE, &v, &esr), esr, v);
  printf("ftruncate to 1 page while mapped: %s\n",
         ftruncate(fd, PAGE) == 0 ? "ok" : strerror(errno));
  fflush(stdout);
  report_access("after truncate, guest load page 3",
                gload(TEST_IPA + 3 * PAGE, &v, &esr), esr, v);
  report_access("after truncate, guest store page 3",
                gstore(TEST_IPA + 3 * PAGE, 6, &esr), esr, 0);
  report_access("after truncate, guest load page 0",
                gload(TEST_IPA, &v, &esr), esr, v);
  CHECK(hv_vm_unmap(TEST_IPA, REGION));
  printf("hv_vm_unmap after truncate: ok\n");
}

static void mode_munmap(void) {
  uint8_t* p = map_or_die(REGION, PROT_READ | PROT_WRITE,
                          MAP_ANON | MAP_PRIVATE, -1);
  memset(p, 0x77, REGION);
  CHECK(hv_vm_map(p, TEST_IPA, REGION, HV_MEMORY_READ | HV_MEMORY_WRITE));
  printf("host munmap while mapped into guest: %s\n",
         munmap(p, REGION) == 0 ? "ok" : strerror(errno));
  fflush(stdout);
  uint64_t v = 0, esr = 0;
  report_access("guest load after host munmap", gload(TEST_IPA, &v, &esr), esr,
                v);
  // Reuse the host VA range for new memory: does the guest see it?
  uint8_t* q = mmap(p, REGION, PROT_READ | PROT_WRITE,
                    MAP_ANON | MAP_PRIVATE | MAP_FIXED, -1, 0);
  if (q != MAP_FAILED) {
    memset(q, 0x33, REGION);
    report_access("guest load after host maps new memory at the same VA",
                  gload(TEST_IPA, &v, &esr), esr, v);
  }
  printf("hv_vm_unmap: %s\n", probe_strerror(hv_vm_unmap(TEST_IPA, REGION)));
}

static void reserve_case(const char* what, uint8_t* p, size_t size) {
  hv_return_t r = hv_vm_map(p, TEST_IPA, size, HV_MEMORY_READ | HV_MEMORY_WRITE);
  printf("%s: hv_vm_map %s\n", what, probe_strerror(r));
  fflush(stdout);
  if (r != HV_SUCCESS) return;
  uint64_t v = 0, esr = 0;
  report_access("  guest load", gload(TEST_IPA, &v, &esr), esr, v);
  // Make the host range usable after the stage-2 mapping exists.
  uint8_t* q = mmap(p, size, PROT_READ | PROT_WRITE,
                    MAP_ANON | MAP_PRIVATE | MAP_FIXED, -1, 0);
  if (q == MAP_FAILED) {
    printf("  host mmap MAP_FIXED: %s\n", strerror(errno));
  } else {
    memset(q, 0x44, PAGE);
    report_access("  guest load after host maps memory there",
                  gload(TEST_IPA, &v, &esr), esr, v);
  }
  CHECK(hv_vm_unmap(TEST_IPA, size));
}

static void mode_reserve(void) {
  // PROT_NONE reservation, as KVM's fillAddressSpace makes.
  uint8_t* p = map_or_die(REGION, PROT_NONE, MAP_ANON | MAP_PRIVATE, -1);
  reserve_case("PROT_NONE reservation", p, REGION);
  munmap(p, REGION);

  // PROT_NONE reservation made usable with mprotect after hv_vm_map.
  p = map_or_die(REGION, PROT_NONE, MAP_ANON | MAP_PRIVATE, -1);
  hv_return_t r = hv_vm_map(p, TEST_IPA, REGION, HV_MEMORY_READ | HV_MEMORY_WRITE);
  printf("PROT_NONE then mprotect RW: hv_vm_map %s\n", probe_strerror(r));
  if (r == HV_SUCCESS) {
    CHECK(mprotect(p, REGION, PROT_READ | PROT_WRITE) == 0 ? HV_SUCCESS : HV_ERROR);
    memset(p, 0x55, PAGE);
    uint64_t v = 0, esr = 0;
    report_access("  guest load after mprotect and host store",
                  gload(TEST_IPA, &v, &esr), esr, v);
    CHECK(hv_vm_unmap(TEST_IPA, REGION));
  }
  munmap(p, REGION);

  // No host mapping at all.
  p = map_or_die(REGION, PROT_READ | PROT_WRITE, MAP_ANON | MAP_PRIVATE, -1);
  munmap(p, REGION);
  reserve_case("unmapped host VA", p, REGION);

  // A hole in the middle of an otherwise mapped range.
  p = map_or_die(3 * PAGE, PROT_READ | PROT_WRITE, MAP_ANON | MAP_PRIVATE, -1);
  munmap(p + PAGE, PAGE);
  r = hv_vm_map(p, TEST_IPA, 3 * PAGE, HV_MEMORY_READ | HV_MEMORY_WRITE);
  printf("range with an unmapped middle page: hv_vm_map %s\n",
         probe_strerror(r));
  if (r == HV_SUCCESS) CHECK(hv_vm_unmap(TEST_IPA, 3 * PAGE));
  munmap(p, 3 * PAGE);

  // A large reservation, as one whole-VA mapping would be.
  size_t big = 1ULL << 30;
  p = map_or_die(big, PROT_NONE, MAP_ANON | MAP_PRIVATE, -1);
  r = hv_vm_map(p, TEST_IPA, big, HV_MEMORY_READ | HV_MEMORY_WRITE);
  printf("1 GiB PROT_NONE reservation: hv_vm_map %s\n", probe_strerror(r));
  if (r == HV_SUCCESS) CHECK(hv_vm_unmap(TEST_IPA, big));
  munmap(p, big);
}

int main(int argc, char** argv) {
  const char* mode = argc > 1 ? argv[1] : "";
  void (*fn)(void) = NULL;
  if (strcmp(mode, "main") == 0) fn = mode_main;
  if (strcmp(mode, "eof") == 0) fn = mode_eof;
  if (strcmp(mode, "truncate") == 0) fn = mode_truncate;
  if (strcmp(mode, "munmap") == 0) fn = mode_munmap;
  if (strcmp(mode, "reserve") == 0) fn = mode_reserve;
  if (!fn) {
    fprintf(stderr, "usage: %s main|eof|truncate|munmap|reserve\n", argv[0]);
    return 2;
  }
  probe_set_qos();
  char name[64];
  snprintf(name, sizeof(name), "vm_map %s", mode);
  probe_print_env(name);
  probe_vm_init();
  entry = probe_load_code(guest_memop, guest_memop_end, 0);
  CHECK(hv_vcpu_create(&vcpu, &vexit, NULL));
  probe_vcpu_reset(vcpu, entry);
  if (fn != mode_main) {
    signal(SIGALRM, on_alarm);
    alarm(10);
  }
  fn();
  return 0;
}
