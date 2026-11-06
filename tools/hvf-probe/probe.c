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

#include "probe.h"

#include <mach/mach_time.h>
#include <pthread.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/mman.h>
#include <sys/qos.h>
#include <sys/sysctl.h>

extern const uint8_t guest_vectors[], guest_vectors_end[];

uint8_t* guest_mem;

const char* probe_strerror(hv_return_t r) {
  switch (r) {
    case HV_SUCCESS: return "HV_SUCCESS";
    case HV_ERROR: return "HV_ERROR";
    case HV_BUSY: return "HV_BUSY";
    case HV_BAD_ARGUMENT: return "HV_BAD_ARGUMENT";
    case HV_NO_RESOURCES: return "HV_NO_RESOURCES";
    case HV_NO_DEVICE: return "HV_NO_DEVICE";
    case HV_DENIED: return "HV_DENIED";
    case HV_UNSUPPORTED: return "HV_UNSUPPORTED";
  }
  static __thread char buf[16];
  snprintf(buf, sizeof(buf), "0x%x", (unsigned)r);
  return buf;
}

void probe_die(const char* what, hv_return_t r, const char* file, int line) {
  fprintf(stderr, "%s:%d: %s failed: %s\n", file, line, what,
          probe_strerror(r));
  exit(1);
}

static void sysctl_str(const char* name, char* buf, size_t len) {
  if (sysctlbyname(name, buf, &len, NULL, 0) != 0) snprintf(buf, len, "?");
}

void probe_print_env(const char* probe_name) {
  char chip[128], osver[64], build[64];
  sysctl_str("machdep.cpu.brand_string", chip, sizeof(chip));
  sysctl_str("kern.osproductversion", osver, sizeof(osver));
  sysctl_str("kern.osversion", build, sizeof(build));
  uint32_t max_vcpus = 0, max_ipa = 0, def_ipa = 0;
  hv_vm_get_max_vcpu_count(&max_vcpus);
  hv_vm_config_get_max_ipa_size(&max_ipa);
  hv_vm_config_get_default_ipa_size(&def_ipa);
  mach_timebase_info_data_t tb;
  mach_timebase_info(&tb);
  printf("# probe: %s\n", probe_name);
  printf("# chip: %s\n# macOS: %s (%s)\n", chip, osver, build);
  printf("# hv max_vcpu_count: %u, max_ipa_bits: %u, default_ipa_bits: %u\n",
         max_vcpus, max_ipa, def_ipa);
  printf("# timebase: %u/%u ns per tick (%.2f ns resolution)\n", tb.numer,
         tb.denom, (double)tb.numer / tb.denom);
}

void probe_vm_init(void) {
  CHECK(hv_vm_create(NULL));
  guest_mem = mmap(NULL, GUEST_SIZE, PROT_READ | PROT_WRITE,
                   MAP_ANON | MAP_PRIVATE, -1, 0);
  if (guest_mem == MAP_FAILED) {
    perror("mmap");
    exit(1);
  }
  CHECK(hv_vm_map(guest_mem, GUEST_BASE, GUEST_SIZE,
                  HV_MEMORY_READ | HV_MEMORY_WRITE | HV_MEMORY_EXEC));

  // Level-1 table, 4 KiB granule, T0SZ=32: four 1 GiB block entries. Entry 0
  // identity-maps [0, 1 GiB) as Normal WB (MAIR attr 0), inner shareable,
  // AF set, EL1 RW. GUEST_BASE lies in that block. IPAs in the block outside
  // the stage-2 mapping fault to the host.
  uint64_t* l1 = (uint64_t*)(guest_mem + GUEST_PT_OFF);
  memset(l1, 0, 4096);
  l1[0] = 0x0ULL | (1ULL << 10) /* AF */ | (3ULL << 8) /* SH inner */ |
          (0ULL << 2) /* AttrIndx 0 */ | 0x1 /* block */;

  size_t vlen = (size_t)(guest_vectors_end - guest_vectors);
  memcpy(guest_mem + GUEST_VEC_OFF, guest_vectors, vlen);
}

uint64_t probe_load_code(const void* start, const void* end, size_t off) {
  size_t len = (size_t)((const uint8_t*)end - (const uint8_t*)start);
  if (GUEST_CODE_OFF + off + len > GUEST_SIZE / 2) {
    fprintf(stderr, "guest code too large\n");
    exit(1);
  }
  memcpy(guest_mem + GUEST_CODE_OFF + off, start, len);
  return GUEST_BASE + GUEST_CODE_OFF + off;
}

struct sysreg_init {
  hv_sys_reg_t reg;
  uint64_t value;
};

void probe_vcpu_reset(hv_vcpu_t vcpu, uint64_t entry) {
  // SCTLR_EL1: legacy RES1 bits (11, 20, 22, 23, 28, 29) plus M, C, I.
  const uint64_t sctlr = 0x30D00800ULL | (1 << 0) | (1 << 2) | (1 << 12);
  // TCR_EL1: T0SZ=32, IRGN0/ORGN0=WBWA, SH0=inner, TG0=4K, EPD1=1, TG1=4K,
  // IPS=32 bits.
  const uint64_t tcr = 32ULL | (1ULL << 8) | (1ULL << 10) | (3ULL << 12) |
                       (0ULL << 14) | (1ULL << 23) | (2ULL << 30);
  const uint64_t spsr_el1h = 0x3c5;  // EL1h, DAIF masked.

  const struct sysreg_init regs[] = {
      {HV_SYS_REG_SCTLR_EL1, sctlr},
      {HV_SYS_REG_CPACR_EL1, 3ULL << 20},  // FPEN=0b11: no FP/SIMD trap.
      {HV_SYS_REG_TTBR0_EL1, GUEST_BASE + GUEST_PT_OFF},
      {HV_SYS_REG_TTBR1_EL1, 0},
      {HV_SYS_REG_TCR_EL1, tcr},
      {HV_SYS_REG_MAIR_EL1, 0xff},  // attr0 Normal WB, attr1 Device-nGnRnE.
      {HV_SYS_REG_AMAIR_EL1, 0},
      {HV_SYS_REG_VBAR_EL1, GUEST_BASE + GUEST_VEC_OFF},
      {HV_SYS_REG_SP_EL0, 0},
      {HV_SYS_REG_SP_EL1, GUEST_BASE + GUEST_SIZE - 0x10},
      {HV_SYS_REG_ELR_EL1, 0},
      {HV_SYS_REG_SPSR_EL1, 0},
      {HV_SYS_REG_ESR_EL1, 0},
      {HV_SYS_REG_FAR_EL1, 0},
      {HV_SYS_REG_PAR_EL1, 0},
      {HV_SYS_REG_AFSR0_EL1, 0},
      {HV_SYS_REG_AFSR1_EL1, 0},
      {HV_SYS_REG_CONTEXTIDR_EL1, 0},
      {HV_SYS_REG_TPIDR_EL1, 0},
      {HV_SYS_REG_TPIDR_EL0, 0},
      {HV_SYS_REG_TPIDRRO_EL0, 0},
      {HV_SYS_REG_CNTKCTL_EL1, 0},
      {HV_SYS_REG_CNTV_CTL_EL0, 0},  // VTimer disabled.
      {HV_SYS_REG_CNTV_CVAL_EL0, 0},
      {HV_SYS_REG_CSSELR_EL1, 0},
      {HV_SYS_REG_MDSCR_EL1, 0},
      {HV_SYS_REG_MDCCINT_EL1, 0},
      {HV_SYS_REG_APIAKEYLO_EL1, 0},
      {HV_SYS_REG_APIAKEYHI_EL1, 0},
      {HV_SYS_REG_APIBKEYLO_EL1, 0},
      {HV_SYS_REG_APIBKEYHI_EL1, 0},
      {HV_SYS_REG_APDAKEYLO_EL1, 0},
      {HV_SYS_REG_APDAKEYHI_EL1, 0},
      {HV_SYS_REG_APDBKEYLO_EL1, 0},
      {HV_SYS_REG_APDBKEYHI_EL1, 0},
      {HV_SYS_REG_APGAKEYLO_EL1, 0},
      {HV_SYS_REG_APGAKEYHI_EL1, 0},
  };
  for (size_t i = 0; i < sizeof(regs) / sizeof(regs[0]); i++) {
    hv_return_t r = hv_vcpu_set_sys_reg(vcpu, regs[i].reg, regs[i].value);
    if (r != HV_SUCCESS) {
      fprintf(stderr, "set_sys_reg(0x%x) failed: 0x%x\n", regs[i].reg,
              (unsigned)r);
      exit(1);
    }
  }

  // Hardware breakpoints and watchpoints: clear every implemented one.
  // HV_SYS_REG_DBG{B,W}{V,C}Rn_EL1 are spaced 8 apart starting at 0x8004.
  for (int n = 0; n < 16; n++) {
    for (int k = 0; k < 4; k++) {
      hv_sys_reg_t reg = (hv_sys_reg_t)(0x8004 + n * 8 + k);
      (void)hv_vcpu_set_sys_reg(vcpu, reg, 0);  // Unimplemented ones fail.
    }
  }

  for (int i = 0; i <= 30; i++) {
    CHECK(hv_vcpu_set_reg(vcpu, (hv_reg_t)(HV_REG_X0 + i), 0));
  }
  hv_simd_fp_uchar16_t zero = {0};
  for (int i = 0; i < 32; i++) {
    CHECK(hv_vcpu_set_simd_fp_reg(vcpu, (hv_simd_fp_reg_t)(HV_SIMD_FP_REG_Q0 + i),
                                  zero));
  }
  CHECK(hv_vcpu_set_reg(vcpu, HV_REG_FPCR, 0));
  CHECK(hv_vcpu_set_reg(vcpu, HV_REG_FPSR, 0));
  CHECK(hv_vcpu_set_reg(vcpu, HV_REG_CPSR, spsr_el1h));
  CHECK(hv_vcpu_set_reg(vcpu, HV_REG_PC, entry));
  CHECK(hv_vcpu_set_vtimer_mask(vcpu, true));
  CHECK(hv_vcpu_set_trap_debug_exceptions(vcpu, false));
}

void probe_set_qos(void) {
  pthread_set_qos_class_self_np(QOS_CLASS_USER_INTERACTIVE, 0);
}

struct spawn_args {
  void* (*fn)(void*);
  void* arg;
};

static void* spawn_trampoline(void* p) {
  struct spawn_args a = *(struct spawn_args*)p;
  free(p);
  probe_set_qos();
  return a.fn(a.arg);
}

pthread_t probe_spawn(void* (*fn)(void*), void* arg) {
  struct spawn_args* a = malloc(sizeof(*a));
  a->fn = fn;
  a->arg = arg;
  pthread_t t;
  if (pthread_create(&t, NULL, spawn_trampoline, a) != 0) {
    perror("pthread_create");
    exit(1);
  }
  return t;
}

double probe_ticks_to_ns(uint64_t ticks) {
  static mach_timebase_info_data_t tb;
  if (tb.denom == 0) mach_timebase_info(&tb);
  return (double)ticks * tb.numer / tb.denom;
}

static int cmp_u64(const void* a, const void* b) {
  uint64_t x = *(const uint64_t*)a, y = *(const uint64_t*)b;
  return (x > y) - (x < y);
}

void probe_print_percentiles(const char* label, uint64_t* ticks, size_t n) {
  qsort(ticks, n, sizeof(uint64_t), cmp_u64);
#define PCT(p) probe_ticks_to_ns(ticks[(size_t)((n - 1) * (p))])
  printf("%-28s min %7.1f  p50 %7.1f  p90 %7.1f  p99 %7.1f  p99.9 %8.1f  "
         "max %9.1f ns\n",
         label, PCT(0.0), PCT(0.50), PCT(0.90), PCT(0.99), PCT(0.999),
         PCT(1.0));
#undef PCT
}

void probe_expect_hvc(hv_vcpu_t vcpu, const hv_vcpu_exit_t* ex,
                      uint16_t imm) {
  uint64_t esr = ex->exception.syndrome;
  if (ex->reason == HV_EXIT_REASON_EXCEPTION && ESR_EC(esr) == EC_HVC64 &&
      (esr & 0xffff) == imm) {
    return;
  }
  uint64_t pc = 0, elr = 0, esr1 = 0, far1 = 0;
  hv_vcpu_get_reg(vcpu, HV_REG_PC, &pc);
  hv_vcpu_get_sys_reg(vcpu, HV_SYS_REG_ELR_EL1, &elr);
  hv_vcpu_get_sys_reg(vcpu, HV_SYS_REG_ESR_EL1, &esr1);
  hv_vcpu_get_sys_reg(vcpu, HV_SYS_REG_FAR_EL1, &far1);
  fprintf(stderr,
          "unexpected exit: reason %u syndrome 0x%llx (EC 0x%llx) va 0x%llx "
          "ipa 0x%llx pc 0x%llx | guest ELR_EL1 0x%llx ESR_EL1 0x%llx "
          "FAR_EL1 0x%llx\n",
          ex->reason, esr, ESR_EC(esr), ex->exception.virtual_address,
          ex->exception.physical_address, pc, elr, esr1, far1);
  exit(1);
}
