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

// sanity: Phase 0 probe 10. Sanity checks:
//
//   1. ID_AA64MMFR0_EL1 as HVF reports it: PA range and supported stage-1
//      and stage-2 granules.
//   2. Stage-1 translation with TCR_EL1.TG0 = 4K, 16K and 64K, each from a
//      newly created (cold) vCPU: code fetch, load, store, and a stage-1
//      translation fault taken by the guest EL1 vector.
//   3. IPA size: default and maximum, and hv_vm_map and guest access at the
//      top of each.
//   4. The macOS 26 4 KiB IPA granule: hv_vm_map alignment rules, and two
//      4 KiB pieces of different host pages at adjacent IPAs.

#include <mach/mach_time.h>
#include <stdatomic.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/mman.h>

#include "probe.h"

#define DATA_OFF 0x70000ULL  // Test word in guest RAM.

static uint64_t entry;

static void vm_create(hv_vm_config_t config) {
  CHECK(hv_vm_create(config));
  CHECK(hv_vm_map(guest_mem, GUEST_BASE, GUEST_SIZE,
                  HV_MEMORY_READ | HV_MEMORY_WRITE | HV_MEMORY_EXEC));
}

static void field(const char* name, uint64_t reg, int lo) {
  printf("  %-10s = 0x%llx\n", name, (reg >> lo) & 0xf);
}

static void part1(void) {
  printf("\n## 1. ID_AA64MMFR0_EL1\n");
  hv_vcpu_config_t cfg = hv_vcpu_config_create();
  uint64_t mmfr0 = 0;
  CHECK(hv_vcpu_config_get_feature_reg(cfg, HV_FEATURE_REG_ID_AA64MMFR0_EL1,
                                       &mmfr0));
  printf("  value      = 0x%016llx\n", mmfr0);
  field("PARange", mmfr0, 0);
  field("TGran16", mmfr0, 20);
  field("TGran64", mmfr0, 24);
  field("TGran4", mmfr0, 28);
  field("TGran16_2", mmfr0, 32);
  field("TGran64_2", mmfr0, 36);
  field("TGran4_2", mmfr0, 40);
  printf("  (TGran4/64: 0 = supported, 0xf = not; TGran16: 0 = not, 1 = "
         "supported; *_2: 0 = as stage 1, 1 = not, 2 = supported)\n");
}

// Watchdog: cancels the vCPU if a run takes longer than 200 ms (for example
// a translation table the CPU does not support, which loops on exceptions).
static hv_vcpu_t watched;
static _Atomic int run_done;

static void* watchdog(void* arg) {
  (void)arg;
  uint64_t t0 = mach_absolute_time();
  while (!atomic_load(&run_done)) {
    if (probe_ticks_to_ns(mach_absolute_time() - t0) > 200e6) {
      hv_vcpus_exit(&watched, 1);
      return NULL;
    }
  }
  return NULL;
}

// Runs one guest access with a watchdog. Returns the access result, or -1 if
// the watchdog fired.
static int guarded_access(hv_vcpu_t v, hv_vcpu_exit_t* ex, int store,
                          uint64_t addr, uint64_t val, uint64_t* out,
                          uint64_t* esr) {
  watched = v;
  atomic_store(&run_done, 0);
  pthread_t w = probe_spawn(watchdog, NULL);
  enum access_result r = probe_guest_access(v, ex, entry, store, addr, val,
                                            out, esr, NULL);
  int canceled = ex->reason == HV_EXIT_REASON_CANCELED;
  atomic_store(&run_done, 1);
  pthread_join(w, NULL);
  return canceled ? -1 : (int)r;
}

static void describe(int r, uint64_t esr, hv_vcpu_t v) {
  if (r == ACCESS_OK) {
    printf("ok");
  } else if (r < 0) {
    uint64_t pc, esr1;
    hv_vcpu_get_reg(v, HV_REG_PC, &pc);
    hv_vcpu_get_sys_reg(v, HV_SYS_REG_ESR_EL1, &esr1);
    printf("no exit within 200 ms (PC 0x%llx, ESR_EL1 0x%llx)", pc, esr1);
  } else if (ESR_EC(esr) == EC_HVC64 && (esr & 0xffff) >= 0x100) {
    uint64_t esr1, far1;
    hv_vcpu_get_sys_reg(v, HV_SYS_REG_ESR_EL1, &esr1);
    hv_vcpu_get_sys_reg(v, HV_SYS_REG_FAR_EL1, &far1);
    printf("guest vector %llu (ESR_EL1 EC 0x%llx, DFSC 0x%llx, FAR 0x%llx)",
           (esr & 0xffff) - 0x100, ESR_EC(esr1), esr1 & 0x3f, far1);
  } else {
    printf("host exit EC 0x%llx syndrome 0x%llx", ESR_EC(esr), esr);
  }
}

// Writes identity-mapping tables for the granule into the page table area
// and returns the TCR_EL1 value. Maps VA [0, 1 GiB) to the same IPA.
static uint64_t build_tables(int granule_kib) {
  uint64_t* pt = (uint64_t*)(guest_mem + GUEST_PT_OFF);
  memset(pt, 0, 0x4000);
  const uint64_t attrs = (1ULL << 10) | (3ULL << 8) | 0x1;  // AF, SH, block.
  const uint64_t common = (1ULL << 8) | (1ULL << 10) | (3ULL << 12) |
                          (1ULL << 23) | (2ULL << 30);  // WBWA, ISH, EPD1.
  switch (granule_kib) {
    case 4:  // T0SZ=32: level 1, 4 entries of 1 GiB.
      pt[0] = 0 | attrs;
      return 32 | common | (0ULL << 14);
    case 16:  // T0SZ=28 (36-bit VA): level 2, 2048 entries of 32 MiB.
      for (uint64_t i = 0; i < 32; i++) pt[i] = (i << 25) | attrs;
      return 28 | common | (2ULL << 14);
    case 64:  // T0SZ=32: level 2, 8 entries of 512 MiB.
      pt[0] = 0 | attrs;
      pt[1] = (1ULL << 29) | attrs;
      return 32 | common | (1ULL << 14);
  }
  abort();
}

static void part2(void) {
  printf("\n## 2. Stage-1 granules from a cold vCPU (identity map of 1 GiB)\n");
  const int granules[] = {4, 16, 64};
  for (int g = 0; g < 3; g++) {
    uint64_t tcr = build_tables(granules[g]);
    *(uint64_t*)(guest_mem + DATA_OFF) = 0x600d0000 + granules[g];
    hv_vcpu_t v;
    hv_vcpu_exit_t* ex;
    CHECK(hv_vcpu_create(&v, &ex, NULL));
    probe_vcpu_reset(v, entry);
    CHECK(hv_vcpu_set_sys_reg(v, HV_SYS_REG_TCR_EL1, tcr));
    uint64_t val = 0, esr = 0;
    printf("TG0 %2d KiB (TCR_EL1 0x%llx):\n  first run, load:  ", granules[g],
           tcr);
    int r = guarded_access(v, ex, 0, GUEST_BASE + DATA_OFF, 0, &val, &esr);
    describe(r, esr, v);
    if (r == ACCESS_OK) printf(", value 0x%llx", val);
    printf("\n  store:            ");
    r = guarded_access(v, ex, 1, GUEST_BASE + DATA_OFF + 8, 0x5707e, NULL,
                       &esr);
    describe(r, esr, v);
    if (r == ACCESS_OK) {
      printf(", host sees 0x%llx",
             *(uint64_t*)(guest_mem + DATA_OFF + 8));
    }
    printf("\n  load VA 0x50000000 (outside the stage-1 map): ");
    probe_vcpu_reset(v, entry);
    CHECK(hv_vcpu_set_sys_reg(v, HV_SYS_REG_TCR_EL1, tcr));
    r = guarded_access(v, ex, 0, 0x50000000, 0, &val, &esr);
    describe(r, esr, v);
    printf("\n");
    CHECK(hv_vcpu_destroy(v));
  }
  build_tables(4);
}

// MMU-off guest for IPA checks: VA == IPA, data accesses are Device memory.
static void reset_mmu_off(hv_vcpu_t v) {
  probe_vcpu_reset(v, entry);
  CHECK(hv_vcpu_set_sys_reg(v, HV_SYS_REG_SCTLR_EL1, 0x30D00800ULL | (1 << 12)));
}

static void ipa_case(uint32_t ipa_bits) {
  hv_vm_config_t cfg = NULL;
  if (ipa_bits) {
    cfg = hv_vm_config_create();
    printf("hv_vm_config_set_ipa_size(%u): %s\n", ipa_bits,
           probe_strerror(hv_vm_config_set_ipa_size(cfg, ipa_bits)));
  }
  vm_create(cfg);
  uint32_t bits = ipa_bits;
  if (!bits) hv_vm_config_get_default_ipa_size(&bits);
  printf("VM with %s IPA size (%u bits):\n", ipa_bits ? "configured" : "default",
         bits);
  uint8_t* page = mmap(NULL, 16384, PROT_READ | PROT_WRITE,
                       MAP_ANON | MAP_PRIVATE, -1, 0);
  *(uint64_t*)page = 0x1234abcd;
  hv_vcpu_t v;
  hv_vcpu_exit_t* ex;
  CHECK(hv_vcpu_create(&v, &ex, NULL));
  const uint64_t tops[] = {(1ULL << bits) - 16384, 1ULL << bits,
                           (1ULL << 36) - 16384, (1ULL << 39)};
  for (int i = 0; i < 4; i++) {
    uint64_t ipa = tops[i];
    if (i == 2 && bits <= 36) continue;
    if (i == 3 && bits < 40) continue;
    hv_return_t r = hv_vm_map(page, ipa, 16384, HV_MEMORY_READ);
    printf("  hv_vm_map at IPA 0x%llx: %s", ipa, probe_strerror(r));
    if (r == HV_SUCCESS) {
      reset_mmu_off(v);
      uint64_t val = 0, esr = 0;
      int a = guarded_access(v, ex, 0, ipa, 0, &val, &esr);
      printf(", guest load (MMU off): ");
      describe(a, esr, v);
      if (a == ACCESS_OK) printf(", value 0x%llx", val);
      CHECK(hv_vm_unmap(ipa, 16384));
    }
    printf("\n");
  }
  CHECK(hv_vcpu_destroy(v));
  munmap(page, 16384);
  CHECK(hv_vm_destroy());
  if (cfg) os_release(cfg);
}

static void part3(void) {
  printf("\n## 3. IPA size\n");
  uint32_t max = 0, def = 0;
  hv_vm_config_get_max_ipa_size(&max);
  hv_vm_config_get_default_ipa_size(&def);
  printf("max %u bits, default %u bits\n", max, def);
  ipa_case(0);
  ipa_case(max);
  hv_vm_config_t cfg = hv_vm_config_create();
  printf("hv_vm_config_set_ipa_size(%u): %s\n", max + 1,
         probe_strerror(hv_vm_config_set_ipa_size(cfg, max + 1)));
  os_release(cfg);
}

static void part4(void) {
  printf("\n## 4. IPA granule\n");
  hv_ipa_granule_t def;
  CHECK(hv_vm_config_get_default_ipa_granule(&def));
  printf("default IPA granule: %s\n",
         def == HV_IPA_GRANULE_4KB ? "4 KiB" : "16 KiB");
  hv_vm_config_t cfg = hv_vm_config_create();
  printf("hv_vm_config_set_ipa_granule(4KB): %s\n",
         probe_strerror(hv_vm_config_set_ipa_granule(cfg, HV_IPA_GRANULE_4KB)));
  hv_return_t r = hv_vm_create(cfg);
  printf("hv_vm_create with 4 KiB IPA granule: %s\n", probe_strerror(r));
  if (r != HV_SUCCESS) return;
  CHECK(hv_vm_map(guest_mem, GUEST_BASE, GUEST_SIZE,
                  HV_MEMORY_READ | HV_MEMORY_WRITE | HV_MEMORY_EXEC));
  uint8_t* a = mmap(NULL, 32768, PROT_READ | PROT_WRITE,
                    MAP_ANON | MAP_PRIVATE, -1, 0);
  uint8_t* b = a + 16384;
  for (int i = 0; i < 4; i++) {
    *(uint64_t*)(a + i * 4096) = 0xa000 + i;
    *(uint64_t*)(b + i * 4096) = 0xb000 + i;
  }
  uint64_t x = 0x20000000;
  hv_memory_flags_t rw = HV_MEMORY_READ | HV_MEMORY_WRITE;
  r = hv_vm_map(a + 4096, x, 4096, rw);
  printf("map 4 KiB: host page A + 4 KiB -> IPA 0x%llx: %s\n", x,
         probe_strerror(r));
  hv_return_t r2 = hv_vm_map(b + 8192, x + 4096, 4096, rw);
  printf("map 4 KiB: host page B + 8 KiB -> IPA 0x%llx: %s\n", x + 4096,
         probe_strerror(r2));
  printf("map 4 KiB at IPA +2 KiB: %s\n",
         probe_strerror(hv_vm_map(a, x + 0x10000 + 2048, 4096, rw)));
  printf("map 2 KiB: %s\n",
         probe_strerror(hv_vm_map(a, x + 0x20000, 2048, rw)));
  hv_vcpu_t v;
  hv_vcpu_exit_t* ex;
  CHECK(hv_vcpu_create(&v, &ex, NULL));
  probe_vcpu_reset(v, entry);
  for (int i = 0; i < 2; i++) {
    uint64_t val = 0, esr = 0;
    int ar = guarded_access(v, ex, 0, x + i * 4096, 0, &val, &esr);
    printf("guest load IPA 0x%llx: ", x + i * 4096);
    describe(ar, esr, v);
    if (ar == ACCESS_OK) printf(", value 0x%llx", val);
    printf("\n");
  }
  uint64_t val = 0, esr = 0;
  int ar = guarded_access(v, ex, 0, x + 8192, 0, &val, &esr);
  printf("guest load IPA 0x%llx (not mapped): ", x + 8192);
  describe(ar, esr, v);
  printf("\n");
  CHECK(hv_vcpu_destroy(v));
  CHECK(hv_vm_destroy());
  os_release(cfg);
}

int main(void) {
  probe_set_qos();
  probe_print_env("sanity");
  probe_vm_init();
  entry = probe_load_code(guest_memop, guest_memop_end, 0);
  part1();
  part2();
  CHECK(hv_vm_destroy());
  part3();
  part4();
  return 0;
}
