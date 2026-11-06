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

## Probe 2: vCPU limit, create/destroy cost, leaked vCPUs

Source: `tools/hvf-probe/vcpu_lifecycle.c`. Three modes, each in its own
process: `cost`, `leak` (200 threads) and `leak-one` (1 thread). Percentiles
come from 2,000 iterations after 100 warm-up iterations.

### Thread affinity

| Call | Result |
|---|---|
| Second `hv_vcpu_create` on a thread that owns a vCPU | `HV_BUSY` |
| `hv_vcpu_get_reg` from a non-owner thread | `HV_BAD_ARGUMENT` |
| `hv_vcpu_set_reg` from a non-owner thread | `HV_BAD_ARGUMENT` |
| `hv_vcpu_run` from a non-owner thread | `HV_BAD_ARGUMENT` |
| `hv_vcpu_destroy` from a non-owner thread | `HV_BAD_ARGUMENT` |

The failed non-owner calls have no side effect. The owner thread can still run
(exit reason `EXCEPTION`) and destroy the vCPU after them.

### vCPU limit

`hv_vm_get_max_vcpu_count` returns 64. 64 threads each create one vCPU
successfully (IDs 0-63). The 65th `hv_vcpu_create` returns
`HV_NO_RESOURCES`. Both runs gave the same result.

### Create and destroy cost (ns)

Two runs, 2026-11-06. "idle VM": no other vCPU exists. "busy VM": 63 other
vCPUs are live on parked threads.

| Operation | Run | min | p50 | p90 | p99 | p99.9 |
|---|---|---|---|---|---|---|
| create, idle VM | 1 | 7875 | 12250 | 17083 | 20458 | 24958 |
| create, idle VM | 2 | 6792 | 8208 | 10958 | 16500 | 21875 |
| destroy, idle VM | 1 | 4500 | 6667 | 9042 | 12458 | 16833 |
| destroy, idle VM | 2 | 3875 | 4125 | 5583 | 9042 | 16792 |
| create, busy VM | 1 | 6917 | 8292 | 12208 | 15708 | 24042 |
| create, busy VM | 2 | 6917 | 7542 | 11083 | 15417 | 20833 |
| destroy, busy VM | 1 | 3917 | 4167 | 5458 | 10250 | 15750 |
| destroy, busy VM | 2 | 3917 | 4083 | 6875 | 10333 | 13083 |

Full vCPU life cycle on one thread (create, program all registers, two runs,
destroy):

| Step | Run | min | p50 | p90 | p99 |
|---|---|---|---|---|---|
| create | 1 | 7583 | 9333 | 11125 | 16375 |
| create | 2 | 6750 | 7458 | 9458 | 14583 |
| program all registers (~170 calls) | 1 | 1875 | 2083 | 2292 | 2458 |
| program all registers (~170 calls) | 2 | 1625 | 1708 | 1792 | 2375 |
| first `hv_vcpu_run` | 1 | 1208 | 1333 | 1458 | 1583 |
| first `hv_vcpu_run` | 2 | 1042 | 1125 | 1208 | 1458 |
| second `hv_vcpu_run` | 1 | 708 | 792 | 917 | 958 |
| second `hv_vcpu_run` | 2 | 625 | 708 | 708 | 875 |
| destroy | 1 | 4458 | 4917 | 6167 | 9125 |
| destroy | 2 | 3958 | 4083 | 6167 | 10250 |

A thread start plus create plus register programming, including the
condition-variable hand-off of this probe, took 135-154 µs per thread. Most of
that is thread creation and the hand-off, not HVF.

### Leaked vCPUs

A leaking thread creates a vCPU, runs it once, and returns without calling
`hv_vcpu_destroy`.

| Check | `leak-one` (1 leak) | `leak` (200 attempts) |
|---|---|---|
| Leaking threads whose create succeeded | 1 | 64 (the 65th got `HV_NO_RESOURCES`) |
| Process exit status | 0, no crash | 0, no crash |
| Main thread `get_reg`/`run`/`destroy` on a leaked ID | `HV_BAD_ARGUMENT` | `HV_BAD_ARGUMENT` |
| Main thread `hv_vcpus_exit` on a leaked ID | `HV_SUCCESS` | `HV_SUCCESS` |
| Concurrent vCPUs available afterwards | 63 | 0 |
| `hv_vm_destroy` | `HV_SUCCESS` | `HV_SUCCESS` |
| Second `hv_vm_create` | `HV_SUCCESS` | `HV_SUCCESS` |
| Concurrent vCPUs in the second VM | 63 | 0 |

A new process gets all 64 vCPUs again (the `cost` run after the leak runs).

### Findings

1. The limit is 64 vCPUs per process on M2 Max and macOS 26.6.2, the same
   value as `hv_vm_get_max_vcpu_count`. The failure is `HV_NO_RESOURCES`.
2. `hv_vcpu_create` costs 7.5-12 µs (p50) and `hv_vcpu_destroy` about 4 µs
   (p50). The number of live vCPUs does not change these costs. The first run
   after create costs about 400 ns more than later runs. Creating a vCPU per
   task switch is not practical; this agrees with the fixed pool.
3. Programming the full register state takes about 1.7-2.1 µs. A pool that
   reuses vCPUs must not re-program all registers on every hand-off.
4. **A vCPU whose owning thread exits without `hv_vcpu_destroy` is leaked for
   the life of the process.** Its slot is not freed when the thread exits,
   not by `hv_vm_destroy`, and not by a new `hv_vm_create`. No other thread
   can destroy it (`HV_BAD_ARGUMENT`). The leak is silent: nothing crashes,
   and `hv_vcpus_exit` on the dead ID returns `HV_SUCCESS`. Each leak removes
   one of the 64 slots permanently.
5. Consequence for the Go platform: a pool thread must call `hv_vcpu_destroy`
   before it can exit. In Go, a goroutine that returns while it is locked
   with `runtime.LockOSThread` makes the runtime terminate its thread, so a
   recovered panic or an early return on a vCPU goroutine leaks a slot. The pool
   goroutines must never return while they own a vCPU, and a leaked slot
   cannot be recovered without restarting the process.

## Probe 3: `hv_vcpus_exit` semantics

Source: `tools/hvf-probe/vcpu_cancel.c` and `guest_spin` in `guest_loops.S`.
Two runs on 2026-11-06 gave the same semantic results. Timings from both runs
are shown.

### Pending cancel (guest: `hvc #1` loop)

| Sequence | Next runs |
|---|---|
| `hv_vcpus_exit` before the first `hv_vcpu_run` | run 1 `CANCELED` (PC unchanged at entry, syndrome 0), run 2 `EXCEPTION` (HVC) |
| `hv_vcpus_exit` after an HVC exit, before the next run | run 1 `CANCELED`, run 2 `EXCEPTION` |
| Two `hv_vcpus_exit` calls, then run | run 1 `CANCELED`, runs 2 and 3 `EXCEPTION` |
| `hv_vcpus_exit` from another thread, which then exits | run 1 `CANCELED`, run 2 `EXCEPTION` |
| `hv_vcpus_exit`, then ~170 register writes, then run | run 1 `CANCELED`, run 2 `EXCEPTION` |
| 100,000 `hv_vcpus_exit` calls, then run | run 1 `CANCELED`, run 2 `EXCEPTION` |

| Bad argument | Result |
|---|---|
| `hv_vcpus_exit` with ID 1000 (never created) | `HV_BAD_ARGUMENT`, no pending cancel |
| `hv_vcpus_exit` with count 0 | `HV_SUCCESS`, no pending cancel |

### Cancel of a running guest

**Spinning guest.** The guest loop never exits by itself. It keeps an exact
relation between x0, x1, d1 and both 64-bit lanes of v3 at every instruction
boundary. Another thread calls `hv_vcpus_exit` 5-100 µs (random) after the
runner signals it is about to run. This is repeated 5,000 times without
resetting the guest, and the host checks the invariant after each exit.

| Result | Run 1 | Run 2 |
|---|---|---|
| Exits that were not `CANCELED` | 0 | 0 |
| GPR/FP/SIMD invariant mismatches | 0 | 0 |
| Resumes with no forward progress | 0 | 0 |
| Stop PC offsets 0/4/8/12/16 | 4038/710/0/0/252 | 4003/786/0/2/209 |

**HVC-exiting guest.** The guest exits with HVC about every 0.7 µs. Another
thread issues 5,000 cancels at random 2-50 µs intervals and waits up to 10 ms
for each to be seen as a `CANCELED` exit.

| Result | Run 1 | Run 2 |
|---|---|---|
| `CANCELED` exits for 5,000 cancels | 5000 | 5000 |
| Lost cancels (none within 10 ms) | 0 | 0 |
| Extra `CANCELED` exits | 0 | 0 |
| HVC exits in the same period | 187,395 | 186,530 |

### Latency and cost (ns)

| Measurement | Run | min | p50 | p90 | p99 | p99.9 |
|---|---|---|---|---|---|---|
| `hv_vcpus_exit` call → `hv_vcpu_run` returns (spinning guest) | 1 | 542 | 625 | 1042 | 1583 | 3708 |
| | 2 | 542 | 667 | 667 | 1375 | 7208 |
| `hv_vcpus_exit` call → `CANCELED` seen by kicker (HVC guest) | 1 | 375 | 708 | 833 | 917 | 2625 |
| | 2 | 375 | 708 | 875 | 958 | 6458 |
| `hv_vcpus_exit` call duration, vCPU running | 1 | 167 | 208 | 333 | 542 | 1125 |
| | 2 | 167 | 208 | 250 | 250 | 875 |
| `hv_vcpu_run` with a cancel already pending | 1 | 42 | 83 | 125 | 125 | 125 |
| | 2 | 42 | 83 | 125 | 125 | 125 |

`hv_vcpus_exit` on a vCPU that is not running costs 100 ns per call (mean of
100,000 calls, both runs).

### Findings

1. **A cancel is sticky.** A cancel issued while the vCPU is not in
   `hv_vcpu_run` stays pending until the next run, from any thread. Register
   writes do not clear it. The next run returns `CANCELED` without entering
   the guest (PC unchanged) in about 83 ns.
2. **Cancels coalesce.** Any number of cancels before a run gives exactly one
   `CANCELED` exit; the run after it is normal. A preemption design can kick
   freely without counting kicks, but it cannot use the number of `CANCELED`
   exits to count events.
3. **No cancel was lost or duplicated** in 10,000 races against a guest that
   exits with HVC every 0.7 µs. A cancel that lands while the vCPU is in the
   host after an HVC exit gives `CANCELED` on the next run.
4. **Guest state is preserved.** A cancel stops the guest at an instruction
   boundary with GPRs, FP registers and both halves of the 128-bit SIMD
   registers consistent, and the guest resumes correctly. PC is not advanced.
   Offsets 8 and 12 (after `fadd`, after the vector `add`) were almost never
   the stop point; this is a microarchitectural effect of where the interrupt
   is taken, not a correctness issue.
5. Cancel latency from the `hv_vcpus_exit` call to the return of
   `hv_vcpu_run` on the owner thread is about 0.6-0.7 µs at p50, about the
   same as an HVC round trip. The kicking thread spends about 200 ns in the
   call when the target is running and 100 ns when it is not.
6. `hv_vcpus_exit` rejects an ID that never existed (`HV_BAD_ARGUMENT`) but
   accepts a leaked ID (`HV_SUCCESS`, probe 2), so its return value cannot
   detect a stale vCPU.
