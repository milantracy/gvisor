# Phase 0 results: Hypervisor.framework probes

This file records the measurements of the Phase 0 probe listed in
`RESUME.md`. The probe sources are in `tools/hvf-probe/` on the
`macos-port/probe` branch. Each probe is a C program, ad-hoc signed with the
`com.apple.security.hypervisor` entitlement (`make run-<probe>`).

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

Timings taken in the first ~100 ms of a process can be up to 40% slower
while the CPU clock ramps up (found in probe 5: the same guest access cost
950 ns at process start and 695 ns after 300 ms of warm-up). Probe 1 warms up
for 100,000 round trips and measures over seconds, so it is not affected.
Probe 2 creates and destroys vCPUs from process start, which can explain part
of its run-to-run spread (create p50 12.3 µs and 8.2 µs). Probes from probe 5
on warm up for 300 ms before they time anything.

Guest RAM is 1 MiB of anonymous memory mapped RWX at IPA `0x10000000`. The
guest-to-host exit is `hvc #1` (immediate 0 is avoided because HVF can consume
it as an SMCCC call).

## Probe 1: `hv_vcpu_run` round-trip latency

Source: `tools/hvf-probe/vcpu_run.c`, and `guest_clean` and `guest_fpdirty`
in `guest_loops.S`.

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
(user-space exit against user-space exit). The KVM probe is in
`tools/hvf-probe/kvm/` (`make run`, optionally `CPU=n` to pin; needs
read-write `/dev/kvm`, no root). It uses the same guest setup and variants.
Because KVM handles guest HVC in the kernel as an SMCCC call, its main exit
is an MMIO store to an IPA with no memory slot (`KVM_EXIT_MMIO`), which is
also the exit gVisor's KVM platform uses. On Linux 6.4+ it also measures
`hvc #0` forwarded to user space by the SMCCC filter (`KVM_EXIT_HYPERCALL`).
The probe compiles without warnings with gcc 14.2 and Debian trixie kernel
headers; it has not been run, because no arm64 Linux host with KVM is
available (Docker Desktop on M2 has no nested KVM).

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

## Probe 4: signals on a vCPU thread

Source: `tools/hvf-probe/vcpu_signal.c`. The vCPU thread has a 64 KiB
`sigaltstack`, as a Go thread has. Two runs on 2026-11-06 gave the same
semantic results.

### Blocked pending SIGURG

The vCPU thread blocks SIGURG and sends SIGURG to itself with
`pthread_kill`. `sigpending` then reports SIGURG pending. The thread then does
1,000,000 HVC round trips.

| Condition | ns/round trip (run 1 / run 2) | `hv_vcpu_run` failures | Non-HVC exits |
|---|---|---|---|
| No signal pending | 680.1 / 686.5 | 0 | 0 |
| SIGURG blocked and pending | 678.9 / 683.9 | 0 | 0 |

SIGURG stays pending through all runs. The handler runs once, when the thread
unblocks SIGURG.

### Unblocked SIGURG while the guest runs

The guest spins (`guest_spin`) and never exits by itself. Another thread sends
SIGURG to the vCPU thread with `pthread_kill` 2,000 times at random 20-100 µs
intervals and waits up to 10 ms for the handler to run each time. Then it
stops the vCPU with `hv_vcpus_exit`.

| Result | Go flags `SA_ONSTACK\|SA_RESTART\|SA_SIGINFO` | `SA_ONSTACK\|SA_SIGINFO` |
|---|---|---|
| Handler calls for 2,000 signals | 2000 (both runs) | 2000 (both runs) |
| `hv_vcpu_run` returns before the stop cancel | 0 | 0 |
| `hv_vcpu_run` failures | 0 | 0 |
| Guest GPR/FP/SIMD invariant mismatches | 0 | 0 |
| Interrupted PC seen by the handler | `hv_trap+0x8` in Hypervisor.framework | same |

Signal latency from `pthread_kill` to handler entry (ns):

| Target thread | Run | min | p50 | p90 | p99 | p99.9 |
|---|---|---|---|---|---|---|
| In `hv_vcpu_run`, Go flags | 1 | 2250 | 2292 | 3000 | 7125 | 12875 |
| | 2 | 2208 | 2292 | 2750 | 6792 | 10708 |
| In `hv_vcpu_run`, no `SA_RESTART` | 1 | 2208 | 2292 | 2667 | 6750 | 12458 |
| | 2 | 2208 | 2292 | 3042 | 6458 | 10792 |
| Spinning in the host | 1 | 1958 | 2083 | 2667 | 6667 | 20125 |
| | 2 | 1958 | 2000 | 2333 | 5958 | 7375 |

### Findings

1. **A blocked pending signal has no effect on `hv_vcpu_run`.** No failures,
   no extra exits, no change in round-trip cost. A vCPU thread can keep SIGURG
   (or any signal) blocked and pending while it runs the guest.
2. **A delivered signal does not end `hv_vcpu_run`.** The kernel takes the
   vCPU out of the guest, runs the handler on the vCPU thread with the
   interrupted PC at `hv_trap+0x8`, and then restarts the trap and re-enters
   the guest. The caller of `hv_vcpu_run` sees nothing. `SA_RESTART` makes
   no difference. Guest state is intact.
3. Consequence: a signal cannot be used to kick a vCPU out of the guest (KVM's
   signal-based kick has no HVF equivalent). Preemption must use
   `hv_vcpus_exit`, which probe 3 shows is reliable and about 3 times faster
   (0.65 µs against 2.3 µs).
4. Consequence for Go: when Go's SIGURG preemption signal hits a vCPU thread
   inside `hv_vcpu_run`, the handler sees a PC in Hypervisor.framework, not
   in Go code. Go does not inject an asynchronous preemption at a non-Go PC,
   so the signal only costs a handler call (about 2.3 µs of latency on the
   sender side) and the guest keeps running. The vCPU goroutine is in a
   foreign call during `hv_vcpu_run`, so the Go scheduler can hand its P to
   another thread.
5. Signal delivery to a thread in `hv_vcpu_run` costs about 0.2-0.3 µs more
   than to a thread spinning in the host.

## Probe 5: `hv_vm_map` with different host backings

Source: `tools/hvf-probe/vm_map.c` and the `guest_memop` stub in
`guest_loops.S` (one guest load or store per run, then `hvc #1`). Four modes,
each in its own process: `main`, `eof`, `truncate`, `munmap`. Test files are
in `$TMPDIR` on APFS. Each mapping is 64 KiB (4 host pages) unless stated.
Two runs on 2026-11-06 gave the same semantic results.

### Backings

"host write → guest" is a host store (or `pwrite` for a read-only host
mapping) followed by a guest load. "guest → host" is a guest store followed by
a host load and, for files, `pread`.

| Host backing | Guest flags | `hv_vm_map` | host write → guest | guest store → host / file | `pwrite` after map → guest |
|---|---|---|---|---|---|
| anonymous `MAP_PRIVATE` | RW | ok | visible | visible | - |
| anonymous `MAP_SHARED` | RW | ok | visible | visible | - |
| `shm_open`, `MAP_SHARED` | RW | ok | visible | visible | - |
| file `O_RDWR`, `MAP_SHARED`, `PROT_READ\|PROT_WRITE` | RW | ok | visible | visible / visible | visible |
| file `O_RDWR`, `MAP_SHARED`, `PROT_READ` | R | ok | visible | stage-2 permission fault (DFSC 0xf, WnR 1) | visible |
| file `O_RDWR`, `MAP_SHARED`, `PROT_READ` | RW | ok | visible | **visible / visible** | visible |
| file `O_RDONLY` (mode 0444), `MAP_SHARED`, `PROT_READ` | R | **`HV_ERROR`** | - | - | - |
| file `O_RDONLY` (mode 0444), `MAP_SHARED`, `PROT_READ` | RW | `HV_ERROR` | - | - | - |
| file `O_RDONLY` (mode 0444), `MAP_PRIVATE`, `PROT_READ` | RW | ok | visible | visible / file unchanged | - |
| file `O_RDWR`, `MAP_PRIVATE` | RW | ok | visible | visible / file unchanged | not visible (page already copied) |

A separate check also failed with `HV_ERROR` for `O_RDONLY` + `MAP_SHARED` with
`READ|EXEC`, and succeeded for `O_RDONLY` + `MAP_PRIVATE` with `READ` only.

### Aliases

| Case | Result |
|---|---|
| One host VA range mapped at two IPAs | `hv_vm_map` ok; store via IPA 1 is visible via IPA 2 |
| Two host `MAP_SHARED` mappings of one file range, at two IPAs | ok; store via IPA 1 is visible via IPA 2 and via host mapping 2 |

### Rules

| Case | Result |
|---|---|
| Host VA, IPA or size aligned to 4 KiB but not 16 KiB | `HV_BAD_ARGUMENT` |
| Flags 0 (no access) | `HV_SUCCESS` |
| Map over an IPA range that is already mapped | `HV_ERROR` |
| Unmap one 16 KiB page in the middle of a mapping | ok; that page faults (DFSC 0x7 translation fault, exit IPA correct), the rest still works |
| Map a different host page into the hole | ok |
| One `hv_vm_unmap` across several separate mappings | `HV_SUCCESS` |
| `hv_vm_protect` READ, guest store | stage-2 permission fault (DFSC 0x6 after `protect`, WnR 1); load ok |
| `hv_vm_protect` 0, guest load | stage-2 fault (DFSC 0x6) |
| `hv_vm_protect` back to RW | store ok |

### Edge cases

| Mode | Result |
|---|---|
| `eof`: 4-page `MAP_SHARED` mapping of a 1-page file | `hv_vm_map` ok. Page 0 works. Load and store past EOF exit with a stage-2 translation fault (DFSC 0x7). No `SIGBUS`, no hang. File size unchanged. |
| `truncate`: `ftruncate` to 1 page while all 4 pages are mapped and touched by the guest | Page 3 load and store then exit with DFSC 0x7. Page 0 still works. `hv_vm_unmap` ok. |
| `munmap`: host `munmap` of anonymous memory that is still mapped into the guest | `munmap` ok. **The guest still reads the old contents (0x77…), also after the host maps new memory at the same VA.** `hv_vm_unmap` ok afterwards. |
| `reserve`: `PROT_NONE` anonymous reservation (4 pages and 1 GiB) | `hv_vm_map` ok. The guest reads zeros. After a host `mmap(MAP_FIXED)` of new memory over the reservation, the guest still reads zeros, not the new host contents. |
| `reserve`: `PROT_NONE` reservation, then host `mprotect` to RW and a host store | The guest sees the host store (same VM object). |
| `reserve`: host VA with no mapping, or a range with one unmapped 16 KiB page in the middle | **`HV_ERROR`** |

### Costs

After 300 ms of warm-up, host memory touched before mapping. Mean ns per call:

| Size | `hv_vm_map` run 1 / 2 | `hv_vm_protect` run 1 / 2 | `hv_vm_unmap` run 1 / 2 |
|---|---|---|---|
| 16 KiB | 332 / 333 | 206 / 200 | 190 / 187 |
| 2 MiB | 328 / 328 | 202 / 197 | 187 / 185 |
| 64 MiB | 328 / 329 | 208 / 203 | 198 / 196 |

Guest access to a 64 MiB region, one access per 16 KiB page, each access is
one `hv_vcpu_run` ending in HVC. Mean ns per access over 4,096 pages:

| Host memory | Access | First touch run 1 / 2 | Second pass run 1 / 2 | One page ×4096 run 1 / 2 |
|---|---|---|---|---|
| touched by host | load | 1868 / 1834 | 770 / 768 | 695 / 696 |
| untouched | load | 1894 / 1948 | 756 / 758 | 709 / 700 |
| untouched | store | 1853 / 1896 | 730 / 735 | 700 / 699 |

A plain HVC round trip is about 683 ns (probe 1).

### Findings

1. **`hv_vm_map` is lazy.** Its cost (~330 ns) does not depend on size from
   16 KiB to 64 MiB. Each page is entered into stage 2 on the first guest
   access, which costs about 1.15 µs more than a later access. This cost is
   the same whether or not the host has touched the page; zero-filling a new
   16 KiB page is not the main cost. `hv_vm_protect` and `hv_vm_unmap` cost
   about 200 ns, also independent of size.
2. A mapped page costs nothing extra after its first access: one page or up
   to 128 pages cycled cost the same as an HVC round trip. Cycling through
   4,096 pages (64 MiB) adds about 60-75 ns per access (TLB misses).
3. **`hv_vm_map` takes a reference to the memory object, not the host VA.**
   After the host unmaps the range, the guest keeps the old pages, even when
   the host maps new memory at the same VA. The Sentry must call
   `hv_vm_unmap` before it unmaps or reuses host memory that is mapped into
   the guest; otherwise host and guest silently see different memory.
4. **A file truncate does reach stage 2.** Pages past the new EOF fault in
   the guest (DFSC 0x7) instead of keeping stale data, and pages past EOF are
   never mapped. A guest access to a truncated file region is a normal
   stage-2 fault exit, not a host `SIGBUS`. So stage-2 mappings follow the
   VM object's pages; they are not a separate pinned copy.
5. **HVF checks the maximum protection of the host mapping, not the current
   protection.** A `PROT_READ` mapping of a file opened `O_RDWR` can be mapped
   RW into the guest, and guest stores reach the file. The process could
   `mprotect` that mapping to RW itself, so this gives the guest nothing the
   host process does not have. The guest flags passed to `hv_vm_map` are what
   restrict the guest.
6. **A `MAP_SHARED` mapping of a file opened `O_RDONLY` cannot be mapped into
   the guest at all, not even read-only (`HV_ERROR`).** `MAP_PRIVATE` of the
   same file works. For gVisor this means a read-only host file that the
   Sentry maps into application memory needs an `O_RDWR` descriptor, or a
   `MAP_PRIVATE` mapping (which does not see later changes to pages it has
   copied), or a copy into the memory file.
7. A 16 KiB-aligned host VA, IPA and size are required (`HV_BAD_ARGUMENT`
   for 4 KiB alignment) with the default IPA granule. Mapping over a mapped
   range fails (`HV_ERROR`); partial unmap and remapping of single pages
   work, which is what an on-demand IPA registry needs.
8. **`hv_vm_map` needs host memory at every page of the range, and it never
   follows later host `mmap` calls** (`reserve` mode, two runs, same
   results). A `PROT_NONE` reservation is accepted and the guest sees that
   reservation's own zero pages; `mprotect` keeps the same object, so the
   guest then sees host stores. A new `mmap` at the same VA is not seen. A
   range with any unmapped page fails. A whole-VA mapping as KVM does it
   (map once, then let the host address space change) is therefore not
   possible with HVF.

## Probe 6: releasing memory that is mapped into the guest

Source: `tools/hvf-probe/mem_release.c`. Five modes, each in its own process:
`anon`, `file`, `shm`, `acct`, `cost`. Each case uses a 64 MiB region (4,096
host pages), filled by the guest with a per-page pattern before the release.
"old" means a page still holds the pattern after the release. Two runs on
2026-11-06 gave the same results (footprints within 0.1 MiB, timings within a
few percent).

### Anonymous memory (`MAP_ANON|MAP_PRIVATE`)

`phys_footprint` is from `TASK_VM_INFO`. Before the region exists it is
1.7 MiB; after the guest writes all 64 MiB it is 65.8 MiB.

| Release | Footprint after release | Host then reads | Guest then reads |
|---|---|---|---|
| `MADV_FREE_REUSABLE` while mapped into the guest | 65.8 MiB (not released) | all old | all old |
| `hv_vm_unmap`, then `MADV_FREE_REUSABLE` | 1.8 MiB (released) | all old, footprint stays 1.8 MiB | after `MADV_FREE_REUSE` + `hv_vm_map`: all old |
| `MADV_DONTNEED` while mapped into the guest | 65.8 MiB (not released) | all old | all old |
| `hv_vm_unmap`, then `MADV_DONTNEED` | 1.8 MiB (released) | all old, footprint back to 65.8 MiB | after `hv_vm_map`: all old |

### File memory (APFS file, `MAP_SHARED`)

| Release | `st_blocks` after release | Host mapping and `pread` | Guest then reads | Guest rewrites all pages |
|---|---|---|---|---|
| `F_PUNCHHOLE` of the whole file while mapped into the guest | 64 MiB → 0 | all zero | **all zero, no fault** | ok, `st_blocks` 64 MiB again after `msync` |
| `hv_vm_unmap`, then `F_PUNCHHOLE`, then `hv_vm_map` | 64 MiB → 0 | all zero | all zero | ok |

`phys_footprint` stays at 1.8 MiB while the guest uses 64 MiB of file memory:
file-backed pages are not in the footprint.

### `shm_open` memory

| Release | Result |
|---|---|
| `F_PUNCHHOLE` | fails, `EBADF` (not supported on POSIX shm) |
| `MADV_FREE_REUSABLE` while mapped into the guest | ok, footprint stays 65.8 MiB, guest still sees all old data |
| `hv_vm_unmap`, then `MADV_FREE_REUSABLE` | footprint 65.8 → 1.8 MiB, host still reads all old data |

### Memory accounting (`acct` mode)

| Step | `phys_footprint` | `resident_size` | System free |
|---|---|---|---|
| start | 1.7 | 6.0 | 7522 |
| host writes 64 MiB | 65.8 | 70.0 | 7459 |
| `hv_vm_map` | 65.8 | 70.0 | 7459 |
| guest reads all pages | **129.8** | **134.0** | 7459 |
| `hv_vm_unmap` | 65.8 | 70.0 | 7459 |

The reverse order (guest writes first, then host reads) gives the same 129.8
MiB. All values in MiB.

### Cost of release and remap on fault (`cost` mode)

Anonymous memory, after 300 ms of warm-up. Release is `hv_vm_unmap` +
`MADV_FREE_REUSABLE` of one chunk whose pages the guest has touched. Refault
is: guest store to the chunk → stage-2 fault exit → `MADV_FREE_REUSE` →
`hv_vm_map` of the chunk → re-run (the store re-executes, then HVC). p50 ns,
run 1 / run 2:

| Step | 16 KiB chunk (4,096 chunks) | 2 MiB chunk (32 chunks) |
|---|---|---|
| release: `hv_vm_unmap` | 708 / 708 | 4667 / 4833 |
| release: `MADV_FREE_REUSABLE` | 208 / 208 | 2333 / 2333 |
| refault: run until fault exit | 792 / 792 | 833 / 833 |
| refault: `MADV_FREE_REUSE` | 167 / 167 | 1958 / 1958 |
| refault: `hv_vm_map` | 458 / 458 | 458 / 458 |
| refault: re-run, store, HVC | 1625 / 1625 | 1667 / 1667 |
| **refault: total** | **3042 / 3042** | **4958 / 4917** |

p99 for the 16 KiB refault total was 4.1-4.2 µs.

### Findings

1. **Anonymous pages that are in stage 2 cannot be released with `madvise`.**
   `MADV_FREE_REUSABLE` and `MADV_DONTNEED` succeed but the footprint does
   not drop and the guest still sees the data. After `hv_vm_unmap` the same
   calls release the pages at once. This confirms the design rule for
   anonymous memory: decommit is unmap, release, remap on fault.
2. **Neither `madvise` zeroes anonymous memory.** With no memory pressure
   the old contents are still there after `MADV_FREE_REUSABLE` or
   `MADV_DONTNEED`, for host and guest. Linux `MADV_DONTNEED` semantics
   (reads return zero) do not hold. A decommitted page that is later handed to
   another guest allocation must be zeroed explicitly, or replaced with a new
   mapping (`mmap MAP_FIXED`).
3. **`F_PUNCHHOLE` on an APFS file works while the range is in stage 2.**
   Disk blocks are freed, and host and guest both read zeros right away,
   without a fault and without `hv_vm_unmap`. Stage 2 follows the file's
   pages, as with truncate in probe 5. For file memory, decommit can be a
   single `F_PUNCHHOLE`, and it gives Linux-like zero-fill.
4. File memory is not counted in `phys_footprint`, but a `MAP_SHARED` file on
   APFS is backed by disk: `st_blocks` grows with guest writes after
   `msync`. This probe did not measure when the kernel writes dirty pages back
   without `msync`. A disk file as the guest memory file trades footprint
   accounting for possible disk I/O. `shm_open` memory cannot be punched (`EBADF`) and behaves like
   anonymous memory for `madvise`.
5. **Task memory accounting counts a page twice when both the host process
   and the guest map it.** `phys_footprint`, `resident_size` and `internal`
   go from 65.8 MiB to 129.8 MiB when the guest touches 64 MiB the host
   already touched, or the reverse; the system free page count does not
   change. The extra count goes away on `hv_vm_unmap`. Real memory use is not
   doubled, but `phys_footprint` is what macOS uses for memory limits,
   jetsam, and Activity Monitor. A Sentry that touches application memory
   through its own mappings (copy-in/copy-out) will be reported at up to
   twice its real use.
6. Releasing one 16 KiB page costs about 0.9 µs and refaulting it about
   3.0 µs (p50), about 4.5 HVC round trips. For 2 MiB chunks, release costs
   about 7 µs and refault about 5 µs. `hv_vm_unmap` of pages that the guest
   has touched costs 708 ns for 16 KiB and 4.7 µs for 2 MiB (about 33 ns
   per page). Unmapping a range the guest never touched costs about 190 ns
   (probe 5).

## Probe 7: kicking a set of vCPUs and running a TLBI on re-entry

Source: `tools/hvf-probe/tlb_bounce.c` and `guest_tlbi_mailbox` in
`guest_loops.S`. The machine has 8 performance and 4 efficiency cores. Two
runs on 2026-11-06, after 300 ms of warm-up.

Method. A host-side Sentry cannot run TLBI, so it writes a new generation
number to a mailbox in guest memory and kicks the vCPUs with one
`hv_vcpus_exit` call. Each vCPU thread re-enters the guest at the mailbox
stub, which compares the generation with its own acknowledged generation,
runs the TLBI if it is new, stores the acknowledgement, and then spins. The
host times from the `hv_vcpus_exit` call until all N acknowledgements are
visible. 2,000 bounces per configuration, 20-50 µs apart, after 200 warm-up
bounces. The main thread is the kicker and has no vCPU.

### A. TLBI cost inside the guest (one vCPU)

Extra time of an HVC round trip that runs the TLBI and barriers, against a
loop with only `dsb nsh; isb`. Best of 5 × 200,000.

| Instruction sequence | Run 1 | Run 2 |
|---|---|---|
| baseline `dsb nsh; isb; hvc` | 690.6 ns | 688.7 ns |
| `tlbi vmalle1; dsb nsh; isb` | +239 ns | +210 ns |
| `tlbi vmalle1is; dsb ish; isb` | +232 ns | +212 ns |
| `tlbi vae1; dsb nsh; isb` | +231 ns | +235 ns |

With 8 other vCPUs spinning in the guest (part C):

| Instruction sequence | Run 1 | Run 2 |
|---|---|---|
| baseline | 779.6 ns | 781.4 ns |
| `tlbi vmalle1` | +346 ns | +306 ns |
| `tlbi vmalle1is` | +300 ns | +324 ns |
| `tlbi vae1` | +386 ns | +396 ns |

No TLBI exited to the host: HVF does not trap guest EL1 TLBI.

### B. Kick N vCPUs, TLBI on re-entry, until all N acknowledged

p50 / p99 in µs, run 1 ; run 2:

| N | No TLBI | `tlbi vmalle1` | `tlbi vmalle1is` | `hv_vcpus_exit` call alone (p50) |
|---|---|---|---|---|
| 1 | 1.00 / 4.8 ; 1.08 / 1.4 | 1.21 / 3.6 ; 1.33 / 9.6 | 1.38 / 3.5 ; 1.29 / 2.5 | 0.21 ; 0.25 |
| 2 | 1.25 / 8.7 ; 1.33 / 7.6 | 1.63 / 4.1 ; 1.67 / 4.9 | 1.63 / 4.5 ; 1.71 / 4.0 | 0.33 ; 0.38 |
| 4 | 2.46 / 3.4 ; 2.67 / 3.4 | 3.54 / 8.0 ; 4.58 / 7.0 | 3.13 / 7.9 ; 4.75 / 9.5 | 1.0-1.3 ; 1.4-1.5 |
| 8 | 4.67 / 12.8 ; 6.25 / 15.3 | 5.25 / 10.3 ; 6.88 / 16.2 | 6.92 / 26.3 ; 7.63 / 22.5 | 2.3-3.3 ; 3.5-3.6 |
| 11 | 8.88 / 225 ; 7.17 / 342 | 9.58 / 107 ; 7.00 / 316 | 9.38 / 403 ; 9.08 / 135 | 5.0-5.5 ; 3.3-4.5 |

At N=8 and N=11 the p99.9 values reach 0.45-3.5 ms, and the maximum single
bounce was 9.4 ms (N=11). With N=11 the 11 vCPU threads and the kicker use all
12 cores, and some vCPU threads run on efficiency cores.

### Findings

1. **Guest TLBI is not trapped and costs about 210-240 ns** with its barrier
   on an idle machine, about 300-400 ns while 8 other vCPUs run. A local
   (`vmalle1`), broadcast (`vmalle1is`) and single-address (`vae1`) TLBI
   all cost about the same. Using the cheapest form does not save
   anything measurable.
2. **The cost of one `hv_vcpus_exit` call grows with the number of vCPUs**:
   about 0.2 µs for 1, 0.35 µs for 2, 1.0-1.5 µs for 4, 2.3-3.6 µs for 8,
   3.3-5.5 µs for 11. The kicker pays roughly 0.3-0.45 µs per vCPU, so HVF
   appears to signal the vCPUs one after another.
3. **A full TLB shootdown through the mailbox takes about 1.0-1.4 µs for one
   vCPU, 2.5-4.8 µs for 4 and 4.7-7.6 µs for 8** (p50, depending on run and
   TLBI kind). The TLBI itself adds 0.2-2 µs; most of the cost is the kick
   and the guest exit and re-entry on each vCPU.
4. **Tails grow fast when vCPU threads fill the machine.** With 8 vCPUs plus
   the kicker, p99 is 10-26 µs and p99.9 up to 0.65 ms. With 11, p99 is
   0.1-0.4 ms and single shootdowns take several ms. A shootdown waits for
   the slowest vCPU, so the vCPU pool should stay below the number of
   performance cores, or shootdowns should only wait for vCPUs that have run
   the address space since its last flush.

## Probe 8: spin-then-wait hand-off between two threads

Source: `tools/hvf-probe/handoff.c`. No HVF calls. Both threads have QoS
`USER_INTERACTIVE`. The wait primitive is `os_sync_wait_on_address` /
`os_sync_wake_by_address_any` (macOS 14.4+) on a 64-bit word. The signaller
calls wake only if the waiter has set a "may be blocked" flag (Dekker-style,
sequentially consistent atomics). Three runs on 2026-11-06 after 300 ms of
warm-up; the first run did not have the busy-waker cases.

### Ping-pong round trip

| Mode | Mean round trip, runs 1/2/3 (ns) | One way (ns) | p50 / p99 / p99.9 round trip (ns) | Wake calls per round trip |
|---|---|---|---|---|
| spin only | 71.9 / 71.2 / 73.0 | 36 | 83 / 83 / 125 | 0 |
| spin 10 µs, then wait | 70.8 / 69.7 / 73.4 | 35 | 83 / 83 / 83-167 | ~0 |
| spin 2 µs, then wait | 74.3 / 71.1 / 72.2 | 36 | 83 / 83 / 125 | < 0.001 |
| no spin (always wait) | 2421 / 2268 / 2394 | 1134-1211 | 2292 / 5292-5500 / 9458-9917 | ~2 |
| pthread mutex + condvar | 2640 / 2609 / 2617 | 1305-1320 | - | - |

The timer resolution is 41.67 ns, so the spin percentiles are one or two
ticks.

### Wake latency of a blocked thread

The waiter is blocked in `os_sync_wait_on_address` (no spin). After the idle
period, the waker stamps the time and calls wake; the waiter stamps the time
when it returns. "sleep": the waker is in `nanosleep` during the idle period
(both cores idle). "busy": the waker spins during the idle period (only the
waiter's core is idle). p50 / p99 in µs, run 2 ; run 3 (run 1, sleep only:
5.96/9.17, 6.46/11.58, 11.88/41.92, 19.33/55.42).

| Idle period | sleep | busy |
|---|---|---|
| 20 µs | 6.00 / 9.04 ; 6.08 / 9.42 | 7.42 / 19.75 ; 8.04 / 20.42 |
| 200 µs | 5.25 / 11.08 ; 5.29 / 11.38 | 5.96 / 12.71 ; 6.04 / 12.21 |
| 2 ms | 10.54 / 62.17 ; 9.92 / 37.96 | 11.42 / 15.46 ; 11.46 / 14.29 |
| 20 ms | 19.58 / 354 ; 20.13 / 47.58 | 11.88 / 14.92 ; 11.83 / 15.71 |

`os_sync_wake_by_address_any` with no waiter costs 114-147 ns and returns
-1 with `ENOENT`.

### Findings

1. **A spinning hand-off costs about 36 ns one way**, about 1/20 of an HVC
   round trip (683 ns). A 2 µs spin budget catches almost every hand-off in
   a tight ping-pong (fewer than 1 in 1,000 iterations blocked).
2. **A wait/wake hand-off between threads that blocked only briefly costs
   about 1.1-1.2 µs one way**, slightly less than a pthread condvar
   (1.3 µs). This is about 1.7 HVC round trips.
3. **Waking a thread whose core has gone idle costs much more:** 5-8 µs at
   p50 after 20-200 µs idle, 10-12 µs after 2 ms, 12-20 µs after 20 ms,
   with p99 up to tens of µs (and 0.35 ms once with both cores idle). This
   is the cost the vCPU pool pays when a vCPU thread sleeps between guest
   entries, and it is 8-30 HVC round trips.
4. Consequence for the design: the systrap-style spin-then-wait is right,
   and the spin phase matters more than the wait primitive. A vCPU thread
   that expects its task back within a few µs should spin; once it blocks,
   the next hand-off costs 5-20 µs. The signaller should check a "blocked"
   flag before calling wake, since an unneeded wake costs 114-147 ns.

## Probe 9: lazy-FP trap (`CPACR_EL1.FPEN`) taken as a host exit

Source: `tools/hvf-probe/fp_trap.c`, and `guest_fp_*` and
`guest_vectors_fp_*` in `guest_loops.S`. Two runs on 2026-11-06 after 300 ms
of warm-up. One iteration is: write `CPACR_EL1`, `isb`, `fmov d0, x0`,
`hvc #1`. With `FPEN = 0` the `fmov` traps (EC 0x07) to the guest EL1 vector
at `VBAR_EL1 + 0x200`; HVF does not route it to the host. The guest's
`msr cpacr_el1` did not exit to the host either.

| Variant | What handles the trap | ns/iteration, run 1 / 2 | Extra over no trap | Host trap exits per iteration |
|---|---|---|---|---|
| no trap | - (`FPEN = 0b11`) | 693.9 / 691.2 | - | 0 |
| in guest | EL1 vector enables FP and `eret`s | 722.4 / 721.0 | +29 / +30 | 0 |
| forward | EL1 vector does `hvc #2; eret`; host writes `CPACR_EL1` | 1510.5 / 1507.3 | +817 / +816 | 1 |
| forward+FP | as forward, and the host writes Q0-Q31 | 1611.8 / 1619.2 | +918 / +928 | 1 |
| host does all | default vector `hvc #0x104`; host reads ESR/ELR/SPSR_EL1 and writes CPACR_EL1, PC, CPSR | 1548.3 / 1549.9 | +854 / +859 | 1 |

Each value is the median of 5 × 200,000 iterations.

FP register access through HVF, per vCPU, 32 calls each:

| Operation | Run 1 | Run 2 |
|---|---|---|
| `hv_vcpu_get_simd_fp_reg` Q0-Q31 | 161.7 ns | 167.1 ns |
| `hv_vcpu_set_simd_fp_reg` Q0-Q31 | 100.4 ns | 102.9 ns |

### Findings

1. **An FP trap handled inside the guest costs about 30 ns.** A trap that
   must reach the host costs about 816 ns: one extra guest exit and entry
   (683 ns, probe 1) plus the trap and the `eret`. A host-side lazy-FP
   switch makes the first FP instruction after a task switch cost about
   1.2 HVC round trips.
2. Letting the stub return with `eret` after `hvc` ("forward") is about 40 ns
   cheaper than having the host emulate the exception return (read ESR, ELR
   and SPSR, write PC and CPSR). The stub-forward shape is the cheaper one.
3. Restoring all 32 Q registers on the trap adds about 100 ns, and saving
   them costs about 165 ns. A full FP save+restore through the HVF API costs
   about 265 ns, less than half of an HVC round trip, and much less than the
   trap itself.
4. These numbers support the settled design (lazy FP access through the
   `PullFullState` contract, with FP save and restore on every vCPU hand-off
   between different tasks): the save and restore costs about 265 ns per
   hand-off. A trap-based alternative (`FPEN = 0` after each hand-off,
   restore on first use) costs about 920 ns each time the task uses FP
   before its next exit, so it would only be cheaper if fewer than about 30%
   of hand-offs touch FP. Go code and libc `memcpy` use SIMD registers often,
   so this is not expected; it can be checked on real workloads later.

## Probe 10: sanity checks (granules, IPA size, IPA granule)

Source: `tools/hvf-probe/sanity.c`. Two runs on 2026-11-06 gave identical
output.

### `ID_AA64MMFR0_EL1` as HVF reports it

`hv_vcpu_config_get_feature_reg` returns `0x000010000f100002`:

| Field | Value | Meaning |
|---|---|---|
| PARange | 0x2 | 40-bit physical address range |
| TGran4 | 0x0 | 4 KiB stage-1 granule supported |
| TGran16 | 0x1 | 16 KiB stage-1 granule supported |
| TGran64 | 0xf | 64 KiB granule **not** supported |
| TGran4_2, TGran16_2, TGran64_2 | 0x0 | stage 2 as stage 1 |

### Stage-1 granules from a cold vCPU

Each case uses a newly created vCPU, programmed with an identity map of the
first 1 GiB, then one guest load, one guest store, and one load from VA
`0x50000000`, which is outside the stage-1 map.

| `TCR_EL1.TG0` | Tables | Load / store | Load outside the map |
|---|---|---|---|
| 4 KiB (`TCR_EL1` `0x80803520`) | T0SZ=32, level-1 1 GiB block | ok / ok, host sees the store | guest vector 4: `ESR_EL1` EC 0x25 (data abort, same EL), DFSC 0x5 (translation fault, level 1), FAR `0x50000000` |
| 16 KiB (`0x8080b51c`) | T0SZ=28, level-2 32 MiB blocks | ok / ok | guest vector 4: EC 0x25, DFSC 0x6 (level 2), FAR `0x50000000` |
| 64 KiB (`0x80807520`) | T0SZ=32, level-2 512 MiB blocks | no exit within 200 ms | no exit within 200 ms |

With the 64 KiB granule the guest loops on instruction aborts (`ESR_EL1`
`0x86000006`, PC at `VBAR_EL1 + 0x200`): the vector fetch itself faults. The
watchdog's `hv_vcpus_exit` stops the loop.

### IPA size

| VM | `hv_vm_map` at top page | `hv_vm_map` at the limit | Guest load at top page (MMU off) |
|---|---|---|---|
| default (36 bits) | IPA `0xf_ffff_c000` (2^36 − 16 KiB): ok | IPA 2^36: `HV_BAD_ARGUMENT` | ok, correct value |
| `hv_vm_config_set_ipa_size(40)` | 2^40 − 16 KiB: ok; 2^39: ok; 2^36 − 16 KiB: ok | 2^40: `HV_BAD_ARGUMENT` | ok at all three |

`hv_vm_config_set_ipa_size(41)` returns `HV_UNSUPPORTED`.

### 4 KiB IPA granule (macOS 26)

The default IPA granule is 16 KiB. `hv_vm_config_set_ipa_granule(4KB)` and
`hv_vm_create` with it succeed.

| Check | Result |
|---|---|
| Map 4 KiB at host page A + 4 KiB to IPA `0x20000000` | ok |
| Map 4 KiB at host page B + 8 KiB to IPA `0x20001000` (adjacent) | ok |
| Map at an IPA aligned to 2 KiB | `HV_BAD_ARGUMENT` |
| Map 2 KiB | `HV_BAD_ARGUMENT` |
| Guest load IPA `0x20000000` | ok, `0xa001` (page A, 4 KiB piece 1) |
| Guest load IPA `0x20001000` | ok, `0xb002` (page B, 4 KiB piece 2) |
| Guest load IPA `0x20002000` (not mapped) | stage-2 fault exit, EC 0x24, DFSC 0x7 |

### Findings

1. Both 4 KiB and 16 KiB stage-1 granules work from a cold vCPU with the
   register set from `probe_vcpu_reset`. 64 KiB is not supported on M2 Max.
   A guest stage-1 fault is delivered to the guest EL1 vector with the
   correct EC, fault level and FAR; it does not exit to the host.
2. The IPA space is 36 bits by default and 40 bits at most
   (`hv_vm_config_set_ipa_size(40)`), matching PARange. Phase 1 should
   configure 40 bits, as the report says.
3. **With the macOS 26 4 KiB IPA granule, HVF maps 4 KiB pieces of 16 KiB
   host pages at any 4 KiB-aligned IPA**, and two pieces from different
   host pages at adjacent IPAs read correctly. A 4 KiB guest view (Phase 2
   option) is possible at the stage-2 level without copying.
4. A guest that loops on exceptions (here, an unsupported granule) never
   exits by itself; `hv_vcpus_exit` stops it. The platform needs a
   watchdog or a timer-based kick for this case.

## Summary

All ten probes ran on Apple M2 Max, macOS 26.6.2 (25G83), on 2026-11-06.
The KVM comparison for probe 1 is written (`tools/hvf-probe/kvm/`) but not
run (no arm64 Linux machine with KVM).

### Costs at a glance (p50 or mean, ns)

| Operation | Cost |
|---|---|
| Guest `hvc` round trip (`hv_vcpu_run` → guest → host) | 683 |
| `hv_vcpu_get_reg` / `hv_vcpu_set_reg` | 5 / 3 |
| Host `getppid()` | 84 |
| `hv_vcpu_create` / `hv_vcpu_destroy` | 7,500-12,000 / 4,100 |
| Program all vCPU registers (~170 calls) | 1,700-2,100 |
| `hv_vcpus_exit` → owner's `hv_vcpu_run` returns | 630-670 |
| `hv_vcpu_run` with a cancel already pending | 83 |
| Signal to a thread in `hv_vcpu_run` (handler runs, run continues) | 2,300 |
| `hv_vm_map` / `hv_vm_protect` / `hv_vm_unmap` (any size, untouched) | 330 / 200 / 190 |
| First guest touch of a mapped page (extra) | 1,150 |
| Release 16 KiB (unmap + `MADV_FREE_REUSABLE`) / refault it | 900 / 3,040 |
| Guest TLBI + barrier | 210-240 |
| Kick N vCPUs, TLBI on re-entry, all acknowledged: N = 1 / 4 / 8 | 1,000-1,400 / 2,500-4,800 / 4,700-7,600 |
| Thread hand-off: spin / wait-wake (brief) / wake of an idle core | 36 / 1,150 / 5,000-20,000 |
| FP trap: in guest / forwarded to host | 30 / 816 |
| Save + restore Q0-Q31 through HVF | 265 |

### Results that change or refine the feasibility report

1. `hv_vm_map` rejects a `MAP_SHARED` mapping only when its **maximum**
   protection lacks write (file opened `O_RDONLY`); then even a read-only
   guest mapping fails. A `PROT_READ` `MAP_SHARED` mapping of an `O_RDWR`
   file works, and so does `MAP_PRIVATE` of an `O_RDONLY` file (probe 5).
   The EROFS image can stay `MAP_SHARED` if it is opened `O_RDWR`.
2. "Pinned" is true for `madvise` only. `MADV_FREE_REUSABLE` and
   `MADV_DONTNEED` do not release or zero anonymous pages that are in stage
   2, but `F_PUNCHHOLE` on an APFS file and `ftruncate` take effect in stage
   2 at once: the guest reads zeros or faults (probes 5 and 6). For a
   file-backed memory file on APFS, decommit is one `F_PUNCHHOLE` with no
   `hv_vm_unmap`. `F_PUNCHHOLE` does not work on `shm_open` objects (`EBADF`).
3. `hv_vm_map` holds the memory object, not the host VA: after a host
   `munmap` the guest still sees the old pages (probe 5). The registry must
   `hv_vm_unmap` before the Sentry unmaps or reuses a range.
4. The `phys_footprint` double count is real for `mmap`ed memory and is
   accounting only: it appears when both the host process and the guest map
   a page, and the system free count does not change (probe 6).
5. A delivered signal does not end `hv_vcpu_run` (probe 4). Kicks must use
   `hv_vcpus_exit`, which is sticky, coalescing and never lost (probe 3).
6. A vCPU leaked by a thread exit is lost until the process exits; neither
   `hv_vm_destroy` nor a new VM frees its slot (probe 2).
7. `hv_vcpus_exit` costs the caller about 0.3-0.45 µs per vCPU, and
   shootdown tails reach milliseconds once vCPU threads fill all cores
   (probe 7).

### Data for the whole-VA (A) against on-demand registry (B) decision

These are inputs to the decision that `RESUME.md` puts after Phase 0; the
decision itself is for the design doc.

- `hv_vm_map` fails on any host VA range with an unmapped page, and a
  stage-2 mapping never follows a later host `mmap` at the same VA (probe 5,
  finding 8). KVM's model, one mapping of the whole Sentry VA that follows
  host changes through MMU notifiers, has no HVF equivalent. See
  [memory-mapping-options.md](memory-mapping-options.md).
- `hv_vm_map` cost does not depend on size (330 ns for 16 KiB or 64 MiB), and
  stage 2 is filled lazily on first guest touch. Mapping large, mostly
  untouched ranges up front, as (A) does, is cheap in the map call.
- The IPA space is at most 40 bits (1 TiB). (A) must fit every Sentry VA
  range that can reach the guest into 1 TiB of IPA.
- Under (A), every page the guest touches is counted twice in
  `phys_footprint` while the Sentry also maps it, and anonymous pages
  cannot be released without `hv_vm_unmap`. Under (B) the same applies to
  the ranges the registry maps.
- File-backed memory released with `F_PUNCHHOLE` needs no unmap under
  either option.
- The refault path that (B) depends on (stage-2 fault exit, `hv_vm_map`,
  re-run) costs about 3 µs for a 16 KiB page; the exit reports the faulting
  IPA in `physical_address`, and a stage-1 fault never exits to the host
  (probe 10), so the two cannot be confused.
