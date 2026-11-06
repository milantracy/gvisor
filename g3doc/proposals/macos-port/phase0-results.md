# Phase 0 results: Hypervisor.framework probes

This file records the measurements of the Phase 0 probe listed in
`RESUME.md`. The probe sources are in `tools/hvf-probe/`. Each probe is a C
program, ad-hoc signed with the `com.apple.security.hypervisor` entitlement
(`make run-<probe>`).

## Test machine

| Item | Value |
|---|---|
| Chip | Apple M2 Max |
| macOS | 26.6.2 (25G83) |
| Host page size | 16384 |
| `hv_vm_get_max_vcpu_count` | 64 |
| `hv_vm_config_get_max_ipa_size` | 40 bits |
| `hv_vm_config_get_default_ipa_size` | 36 bits |
| Timer | `mach_absolute_time`, 125/3 ns per tick (41.67 ns resolution) |
| Compiler | Apple clang (Command Line Tools), `-O2` |

All probes run on the main thread with QoS class `USER_INTERACTIVE`, so the
scheduler places them on a performance core. macOS has no CPU affinity API, so
the core is not pinned.

## Common guest setup

HVF has no vCPU reset state, so `probe_vcpu_reset()` in `probe.c` programs
every writable register:

- `CPSR` = `0x3c5`: EL1h, DAIF masked.
- `SCTLR_EL1` = `0x30d01805`: MMU, D-cache and I-cache on, legacy RES1 bits
  set.
- `TCR_EL1`: 4 KiB granule, `T0SZ=32`, WBWA inner shareable walks, `EPD1=1`.
  `TTBR0_EL1` points to one level-1 table that identity-maps the first 1 GiB as
  Normal WB memory (`MAIR_EL1` attr 0 = `0xff`).
- `VBAR_EL1`: a 16-slot vector table. Slot n executes `hvc #(0x100+n)`, so an
  unexpected guest exception exits with an identifiable immediate.
- `CPACR_EL1.FPEN` = `0b11` (no FP trap). VTimer disabled (`CNTV_CTL_EL0=0`) and
  masked.
- All other EL1/EL0 system registers, PAC keys, debug registers, X0-X30,
  Q0-Q31, FPCR and FPSR are set to 0.

Guest RAM is 1 MiB of anonymous memory mapped RWX at IPA `0x10000000`. The
guest-to-host exit is `hvc #1` (immediate 0 is avoided because HVF can consume
it as an SMCCC call).

## Probe 1: `hv_vcpu_run` round-trip latency

Source: `tools/hvf-probe/vcpu_run.c`, `vcpu_run_guest.S`.

The guest loop is `hvc #1; b loop`. One round trip is one `hv_vcpu_run` call
that enters the guest, executes the HVC and returns to the host. The mean is
taken over 5 trials of 1,000,000 round trips each, after 100,000 warm-up
round trips. Percentiles come from 200,000 individually timed round trips and
are quantized to the 41.67 ns timer tick.

Variants:

- **clean**: the guest does not use FP/SIMD and the host does not touch FP/SIMD.
- **guest-fp**: the guest writes `d0` and `d1` before every exit.
- **guest+host-fp**: as guest-fp, and the host writes `v0`-`v31` before every
  `hv_vcpu_run`.
- **syscall-shape**: as clean, and the host reads X0-X8 with
  `hv_vcpu_get_reg` and writes X0 with `hv_vcpu_set_reg` after every exit.
  This is the minimum register traffic of a syscall forwarded to a host-side
  Sentry.

Results, 2026-11-06, two separate runs (run 2 shown; run 1 means were within
3 ns):

| Variant | Median of 5 trial means (ns) | Trial means (ns) | min | p50 | p90 | p99 | p99.9 |
|---|---|---|---|---|---|---|---|
| clean | 683.0 | 681.0 683.2 683.0 683.4 680.8 | 583 | 667 | 708 | 875 | 1000 |
| guest-fp | 686.0 | 686.9 686.0 685.2 686.5 685.8 | 583 | 667 | 708 | 1000 | 1042 |
| guest+host-fp | 686.3 | 686.3 686.6 687.6 686.3 685.6 | 583 | 667 | 708 | 1000 | 1042 |
| syscall-shape | 725.5 | 724.8 725.5 725.7 725.0 725.8 | 625 | 708 | 750 | 1083 | 1292 |

Maximum single samples were 11-21 µs in every variant (host interrupts and
preemption).

Reference costs measured in the same process:

| Operation | ns/call |
|---|---|
| `hv_vcpu_get_reg` (X0) | 5.3 |
| `hv_vcpu_set_reg` (X0) | 3.0 |
| host `getppid()` | 84.3 |

Findings:

1. A guest EL1 `hvc` round trip to the host costs about **683 ns** on M2 Max,
   about 8 times a trivial host syscall.
2. The FP/SIMD state of the guest and the host has no measurable effect
   (+3 ns, inside run-to-run noise). Either HVF switches FP/SIMD state on
   every exit whether or not it is used, or the switch is too small to see
   against the fixed cost. This probe cannot tell the two apart.
3. The guest FP state is preserved correctly when the host clobbers
   `v0`-`v31` between runs: guest `d1` was 500500 after 1000 iterations, as
   expected.
4. Register access is a cheap framework call (3-5 ns), not a kernel
   crossing. Ten register accesses per exit add about 42 ns (6%).
5. After an HVC exit, PC points to the instruction after the `hvc` (entry+4).
   The host does not need to advance PC. The syndrome was `0x5a000001`:
   EC `0x16` (HVC64), IL=1, imm16=1.

Not yet done for probe 1: the same guest under KVM on an arm64 Linux host
(user-space exit against user-space exit). No arm64 Linux machine is
available in this session.
