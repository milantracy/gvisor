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

// kvm_vcpu_run: the KVM (Linux arm64) counterpart of Phase 0 probe 1
// (../vcpu_run.c). It measures a guest EL1 -> host user space -> guest round
// trip through KVM_RUN with the same guest setup as the HVF probe: EL1h, MMU
// and caches on, 4 KiB identity map of the first 1 GiB, guest RAM at IPA
// 0x10000000.
//
// KVM handles HVC in the kernel (as SMCCC), so the user-space exits are:
//
//   mmio          guest store to an IPA with no memory slot (KVM_EXIT_MMIO).
//                 This is the exit gVisor's KVM platform uses.
//   mmio-fp       as mmio, guest writes d0/d1 before every exit.
//   mmio-host-fp  as mmio-fp, host writes v0-v31 between runs.
//   syscall-shape as mmio, host reads x0-x8 and writes x0 per exit with
//                 KVM_GET_ONE_REG/KVM_SET_ONE_REG.
//   hvc           "hvc #0" forwarded to user space by the SMCCC filter
//                 (KVM_EXIT_HYPERCALL, Linux 6.4+). Skipped if unsupported.
//
// Usage: kvm_vcpu_run [cpu]   (optional CPU to pin the thread to)

#define _GNU_SOURCE
#include <errno.h>
#include <fcntl.h>
#include <linux/kvm.h>
#include <sched.h>
#include <stddef.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/ioctl.h>
#include <sys/mman.h>
#include <sys/utsname.h>
#include <time.h>
#include <unistd.h>

#define GUEST_BASE 0x10000000ULL
#define GUEST_SIZE (1ULL << 20)
#define PT_OFF 0x0000
#define VEC_OFF 0x4000
#define CODE_OFF 0x8000
#define MMIO_ADDR 0x30000000ULL
#define VEC_REPORT_ADDR 0x31000000ULL
#define SMCCC_FN 0x82000000ULL  // SMC32 fast call, SiP service range.

#define TRIALS 5
#define ITERS 1000000
#define SAMPLES 200000

extern const uint8_t kvm_guest_mmio[], kvm_guest_mmio_end[];
extern const uint8_t kvm_guest_mmio_fp[], kvm_guest_mmio_fp_end[];
extern const uint8_t kvm_guest_hvc[], kvm_guest_hvc_end[];
extern const uint8_t kvm_guest_vectors[], kvm_guest_vectors_end[];

static int kvm, vm, vcpu;
static struct kvm_run* run;
static uint8_t* mem;
static int have_hvc_exit;

#define CORE_REG(x)                                   \
  (KVM_REG_ARM64 | KVM_REG_SIZE_U64 | KVM_REG_ARM_CORE | \
   KVM_REG_ARM_CORE_REG(x))
#define FP_REG(i)                                      \
  (KVM_REG_ARM64 | KVM_REG_SIZE_U128 | KVM_REG_ARM_CORE | \
   KVM_REG_ARM_CORE_REG(fp_regs.vregs[i]))
#define SYS_REG(op0, op1, crn, crm, op2) ARM64_SYS_REG(op0, op1, crn, crm, op2)

static void die(const char* what) {
  perror(what);
  exit(1);
}

static void set_reg(uint64_t id, uint64_t v) {
  struct kvm_one_reg r = {.id = id, .addr = (uint64_t)(uintptr_t)&v};
  if (ioctl(vcpu, KVM_SET_ONE_REG, &r) != 0) die("KVM_SET_ONE_REG");
}

static uint64_t get_reg(uint64_t id) {
  uint64_t v = 0;
  struct kvm_one_reg r = {.id = id, .addr = (uint64_t)(uintptr_t)&v};
  if (ioctl(vcpu, KVM_GET_ONE_REG, &r) != 0) die("KVM_GET_ONE_REG");
  return v;
}

static uint64_t now_ns(void) {
  struct timespec ts;
  clock_gettime(CLOCK_MONOTONIC_RAW, &ts);
  return (uint64_t)ts.tv_sec * 1000000000ULL + (uint64_t)ts.tv_nsec;
}

static void print_env(void) {
  struct utsname u;
  uname(&u);
  printf("# probe: kvm_vcpu_run\n# kernel: %s %s %s\n", u.sysname, u.release,
         u.machine);
  FILE* f = fopen("/proc/cpuinfo", "r");
  if (f) {
    char line[256];
    int printed = 0;
    while (fgets(line, sizeof(line), f) && printed < 4) {
      if (!strncmp(line, "CPU implementer", 15) ||
          !strncmp(line, "CPU part", 8) || !strncmp(line, "CPU variant", 11) ||
          !strncmp(line, "model name", 10)) {
        printf("# %s", line);
        printed++;
      }
    }
    fclose(f);
  }
  f = fopen("/sys/firmware/devicetree/base/model", "r");
  if (f) {
    char model[128] = {0};
    if (fread(model, 1, sizeof(model) - 1, f) > 0) printf("# model: %s\n", model);
    fclose(f);
  }
  printf("# KVM API version %d, KVM_CAP_ARM_VM_IPA_SIZE %d\n",
         ioctl(kvm, KVM_GET_API_VERSION, 0),
         ioctl(kvm, KVM_CHECK_EXTENSION, KVM_CAP_ARM_VM_IPA_SIZE));
}

static void setup_vm(void) {
  kvm = open("/dev/kvm", O_RDWR | O_CLOEXEC);
  if (kvm < 0) die("open /dev/kvm");
  print_env();
  vm = ioctl(kvm, KVM_CREATE_VM, 0);
  if (vm < 0) die("KVM_CREATE_VM");

  mem = mmap(NULL, GUEST_SIZE, PROT_READ | PROT_WRITE,
             MAP_ANONYMOUS | MAP_PRIVATE, -1, 0);
  if (mem == MAP_FAILED) die("mmap");
  struct kvm_userspace_memory_region region = {
      .slot = 0,
      .guest_phys_addr = GUEST_BASE,
      .memory_size = GUEST_SIZE,
      .userspace_addr = (uint64_t)(uintptr_t)mem,
  };
  if (ioctl(vm, KVM_SET_USER_MEMORY_REGION, &region) != 0) {
    die("KVM_SET_USER_MEMORY_REGION");
  }

  // Same tables as the HVF probe: one level-1 1 GiB block, identity.
  uint64_t* l1 = (uint64_t*)(mem + PT_OFF);
  l1[0] = (1ULL << 10) | (3ULL << 8) | 0x1;
  memcpy(mem + VEC_OFF, kvm_guest_vectors,
         (size_t)(kvm_guest_vectors_end - kvm_guest_vectors));

#ifdef KVM_ARM_VM_SMCCC_CTRL
  // Forward one SMCCC function ID to user space (must be set before the
  // first KVM_RUN).
  struct kvm_smccc_filter filter = {
      .base = (uint32_t)SMCCC_FN,
      .nr_functions = 1,
      .action = KVM_SMCCC_FILTER_FWD_TO_USER,
  };
  struct kvm_device_attr attr = {
      .group = KVM_ARM_VM_SMCCC_CTRL,
      .attr = KVM_ARM_VM_SMCCC_FILTER,
      .addr = (uint64_t)(uintptr_t)&filter,
  };
  have_hvc_exit = ioctl(vm, KVM_SET_DEVICE_ATTR, &attr) == 0;
  printf("# SMCCC filter (HVC to user space): %s\n",
         have_hvc_exit ? "yes" : strerror(errno));
#else
  printf("# SMCCC filter (HVC to user space): not in these kernel headers\n");
#endif

  vcpu = ioctl(vm, KVM_CREATE_VCPU, 0);
  if (vcpu < 0) die("KVM_CREATE_VCPU");
  int run_size = ioctl(kvm, KVM_GET_VCPU_MMAP_SIZE, 0);
  run = mmap(NULL, (size_t)run_size, PROT_READ | PROT_WRITE, MAP_SHARED, vcpu,
             0);
  if (run == MAP_FAILED) die("mmap kvm_run");
  struct kvm_vcpu_init init;
  if (ioctl(vm, KVM_ARM_PREFERRED_TARGET, &init) != 0) {
    die("KVM_ARM_PREFERRED_TARGET");
  }
  if (ioctl(vcpu, KVM_ARM_VCPU_INIT, &init) != 0) die("KVM_ARM_VCPU_INIT");
}

static uint64_t load_code(const uint8_t* start, const uint8_t* end,
                          size_t off) {
  memcpy(mem + CODE_OFF + off, start, (size_t)(end - start));
  return GUEST_BASE + CODE_OFF + off;
}

// Same register state as probe_vcpu_reset in ../probe.c, on top of KVM's own
// reset state.
static void reset(uint64_t entry) {
  const uint64_t sctlr = 0x30D00800ULL | (1 << 0) | (1 << 2) | (1 << 12);
  const uint64_t tcr = 32ULL | (1ULL << 8) | (1ULL << 10) | (3ULL << 12) |
                       (1ULL << 23) | (2ULL << 30);
  set_reg(SYS_REG(3, 0, 1, 0, 0), sctlr);                   // SCTLR_EL1
  set_reg(SYS_REG(3, 0, 1, 0, 2), 3ULL << 20);              // CPACR_EL1
  set_reg(SYS_REG(3, 0, 2, 0, 0), GUEST_BASE + PT_OFF);     // TTBR0_EL1
  set_reg(SYS_REG(3, 0, 2, 0, 1), 0);                       // TTBR1_EL1
  set_reg(SYS_REG(3, 0, 2, 0, 2), tcr);                     // TCR_EL1
  set_reg(SYS_REG(3, 0, 10, 2, 0), 0xff);                   // MAIR_EL1
  set_reg(SYS_REG(3, 0, 12, 0, 0), GUEST_BASE + VEC_OFF);   // VBAR_EL1
  for (int i = 0; i < 31; i++) set_reg(CORE_REG(regs.regs[i]), 0);
  __uint128_t zero = 0;
  for (int i = 0; i < 32; i++) {
    struct kvm_one_reg r = {.id = FP_REG(i), .addr = (uint64_t)(uintptr_t)&zero};
    if (ioctl(vcpu, KVM_SET_ONE_REG, &r) != 0) die("KVM_SET_ONE_REG fp");
  }
  set_reg(CORE_REG(sp_el1), GUEST_BASE + GUEST_SIZE - 0x10);
  set_reg(CORE_REG(regs.pstate), 0x3c5);  // EL1h, DAIF masked.
  set_reg(CORE_REG(regs.pc), entry);
  set_reg(CORE_REG(regs.regs[1]), MMIO_ADDR);
  set_reg(CORE_REG(regs.regs[2]), SMCCC_FN);
  set_reg(CORE_REG(regs.regs[10]), VEC_REPORT_ADDR);
}

static void bad_exit(void) {
  if (run->exit_reason == KVM_EXIT_MMIO &&
      run->mmio.phys_addr == VEC_REPORT_ADDR) {
    uint64_t slot;
    memcpy(&slot, run->mmio.data, 8);
    fprintf(stderr,
            "guest took vector slot %llu: ESR_EL1 0x%llx ELR_EL1 0x%llx "
            "FAR_EL1 0x%llx\n",
            (unsigned long long)slot,
            (unsigned long long)get_reg(SYS_REG(3, 0, 5, 2, 0)),
            (unsigned long long)get_reg(SYS_REG(3, 0, 4, 0, 1)),
            (unsigned long long)get_reg(SYS_REG(3, 0, 6, 0, 0)));
  } else {
    fprintf(stderr, "unexpected exit reason %u (pc 0x%llx)\n",
            run->exit_reason,
            (unsigned long long)get_reg(CORE_REG(regs.pc)));
  }
  exit(1);
}

enum variant { MMIO, MMIO_FP, MMIO_HOST_FP, SYSCALL_SHAPE, HVC, NVARIANTS };
static const char* variant_name[] = {"mmio", "mmio-fp", "mmio-host-fp",
                                     "syscall-shape", "hvc"};

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

static uint64_t entry_mmio, entry_mmio_fp, entry_hvc;

static inline __attribute__((always_inline)) void round_trip(enum variant v) {
  if (v == MMIO_HOST_FP) host_dirty_fp();
  if (ioctl(vcpu, KVM_RUN, 0) != 0) die("KVM_RUN");
  if (v == HVC) {
#ifdef KVM_EXIT_HYPERCALL
    if (__builtin_expect(run->exit_reason != KVM_EXIT_HYPERCALL ||
                             run->hypercall.nr != SMCCC_FN,
                         0)) {
      bad_exit();
    }
#endif
    return;
  }
  if (__builtin_expect(run->exit_reason != KVM_EXIT_MMIO ||
                           run->mmio.phys_addr != MMIO_ADDR,
                       0)) {
    bad_exit();
  }
  if (v == SYSCALL_SHAPE) {
    uint64_t r[9];
    for (int i = 0; i < 9; i++) r[i] = get_reg(CORE_REG(regs.regs[i]));
    set_reg(CORE_REG(regs.regs[0]), r[8] + 1);
  }
}

static int cmp_u64(const void* a, const void* b) {
  uint64_t x = *(const uint64_t*)a, y = *(const uint64_t*)b;
  return (x > y) - (x < y);
}

static int cmp_double(const void* a, const void* b) {
  double x = *(const double*)a, y = *(const double*)b;
  return (x > y) - (x < y);
}

static void measure(enum variant v, uint64_t* samples) {
  uint64_t entry = v == HVC                         ? entry_hvc
                   : (v == MMIO_FP || v == MMIO_HOST_FP) ? entry_mmio_fp
                                                         : entry_mmio;
  reset(entry);
  for (int i = 0; i < 100000; i++) round_trip(v);
  double means[TRIALS];
  for (int t = 0; t < TRIALS; t++) {
    uint64_t t0 = now_ns();
    for (int i = 0; i < ITERS; i++) round_trip(v);
    means[t] = (double)(now_ns() - t0) / ITERS;
  }
  printf("%-14s mean ns/round trip over %d x %d:", variant_name[v], TRIALS,
         ITERS);
  for (int t = 0; t < TRIALS; t++) printf(" %.1f", means[t]);
  qsort(means, TRIALS, sizeof(double), cmp_double);
  printf("  | median %.1f\n", means[TRIALS / 2]);
  for (int i = 0; i < SAMPLES; i++) {
    uint64_t a = now_ns();
    round_trip(v);
    samples[i] = now_ns() - a;
  }
  qsort(samples, SAMPLES, sizeof(uint64_t), cmp_u64);
#define PCT(p) (double)samples[(size_t)((SAMPLES - 1) * (p))]
  printf("%-14s min %7.1f  p50 %7.1f  p90 %7.1f  p99 %7.1f  p99.9 %8.1f  "
         "max %9.1f ns\n",
         variant_name[v], PCT(0.0), PCT(0.5), PCT(0.9), PCT(0.99), PCT(0.999),
         PCT(1.0));
#undef PCT
}

int main(int argc, char** argv) {
  if (argc > 1) {
    cpu_set_t set;
    CPU_ZERO(&set);
    CPU_SET(atoi(argv[1]), &set);
    if (sched_setaffinity(0, sizeof(set), &set) != 0) die("sched_setaffinity");
    printf("# pinned to CPU %s\n", argv[1]);
  }
  setup_vm();
  entry_mmio = load_code(kvm_guest_mmio, kvm_guest_mmio_end, 0);
  entry_mmio_fp = load_code(kvm_guest_mmio_fp, kvm_guest_mmio_fp_end, 0x100);
  entry_hvc = load_code(kvm_guest_hvc, kvm_guest_hvc_end, 0x200);

  // Where does PC point after an MMIO exit and re-entry?
  reset(entry_mmio);
  if (ioctl(vcpu, KVM_RUN, 0) != 0) die("KVM_RUN");
  if (run->exit_reason != KVM_EXIT_MMIO) bad_exit();
  printf("first exit: KVM_EXIT_MMIO at 0x%llx, %s, len %u; PC entry%+lld\n",
         (unsigned long long)run->mmio.phys_addr,
         run->mmio.is_write ? "write" : "read", run->mmio.len,
         (long long)(get_reg(CORE_REG(regs.pc)) - entry_mmio));

  uint64_t* samples = malloc(SAMPLES * sizeof(uint64_t));
  for (int v = 0; v < NVARIANTS; v++) {
    if (v == HVC && !have_hvc_exit) {
      printf("hvc            skipped: no SMCCC filter\n");
      continue;
    }
    measure((enum variant)v, samples);
  }

  // Guest FP state survives host clobbering of v0-v31.
  reset(entry_mmio_fp);
  for (int i = 0; i < 1000; i++) round_trip(MMIO_HOST_FP);
  __uint128_t q1 = 0;
  struct kvm_one_reg r = {.id = FP_REG(1), .addr = (uint64_t)(uintptr_t)&q1};
  if (ioctl(vcpu, KVM_GET_ONE_REG, &r) != 0) die("KVM_GET_ONE_REG");
  double d1;
  memcpy(&d1, &q1, sizeof(d1));
  printf("guest d1 after 1000 mmio-host-fp iterations: %.0f (expect %.0f)\n",
         d1, 1000.0 * 1001 / 2);

  uint64_t t0 = now_ns();
  for (int i = 0; i < ITERS; i++) (void)get_reg(CORE_REG(regs.regs[0]));
  printf("KVM_GET_ONE_REG: %.1f ns/call\n", (double)(now_ns() - t0) / ITERS);
  t0 = now_ns();
  for (int i = 0; i < ITERS; i++) set_reg(CORE_REG(regs.regs[0]), (uint64_t)i);
  printf("KVM_SET_ONE_REG: %.1f ns/call\n", (double)(now_ns() - t0) / ITERS);
  t0 = now_ns();
  for (int i = 0; i < ITERS; i++) (void)getppid();
  printf("host getppid(): %.1f ns/call\n", (double)(now_ns() - t0) / ITERS);
  free(samples);
  return 0;
}
