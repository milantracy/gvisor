# Resuming the gVisor-on-macOS work

This directory holds the output of a feasibility study done on 2026-11-06 on a
Linux machine. Claude Code sessions do not transfer between machines, so a new
session on the MacBook starts from these files. Everything the new session
needs is in this directory and in the gVisor checkout it lives in.

## Goal

A native macOS binary that starts the gVisor Sentry and the other gVisor
components on the Mac and runs Linux programs in it. Agreed on 2026-11-06.

| Component | On Linux today | On macOS | Phase |
|---|---|---|---|
| Sentry (the gVisor kernel) | Runs inside `runsc` | The same Go code, with darwin files for its Linux-only leaf packages | 1 |
| Platform (traps application syscalls) | ptrace, systrap, or KVM | New `hvf` platform on Hypervisor.framework | 1 |
| Launcher | `runsc` | A small darwin command that boots the Sentry and runs one program | 1 |
| Root filesystem | Gofer, EROFS, overlay | EROFS image | 1 |
| Host files | Gofer process with directfs | A darwin gofer process | 2 |
| Networking | netstack with host link endpoints | netstack with a user-space proxy or vmnet | 2 |
| Sentry confinement | seccomp and namespaces | Seatbelt profile and a separate gofer process | 2 |
| `runsc`-style CLI | `runsc run`, OCI | Grows from the launcher as Phase 2 lands | 2 and later |

The Phase 1 exit criterion is static busybox `echo hello` and `dmesg` from an
EROFS root. A full `runsc` build on macOS is not a Phase 1 goal. Linux builds
must not change.

## What is here

| File | What it is |
|---|---|
| `phase0-results.md` | Phase 0 probe results and summary. |
| `memory-mapping-options.md` | Whole-VA (A) against on-demand IPA registry (B); (B) is decided. |
| `darwin-build.md` | macOS host setup and the state of the native darwin Bazel build. |
| `feasibility-report.md` | The reviewed report (~9,100 words). Passed three rounds of independent review on 2026-11-06; both reviewers agree with the final design. Read this first. |
| `research_notes/sentry_platform_host_dependencies.md` | Inventory of Linux-host dependencies in the platform, ring0, pgalloc, signal and syscall layers, with `file:line` references at commit `f51a205a8`. |
| `research_notes/runsc_fs_net_build_dependencies.md` | Same inventory for runsc, gofer/lisafs/directfs/EROFS, netstack link layers, seccomp/cgroups, and the Bazel/`go` branch build. Includes the `GOOS=darwin` build experiment results. |
| `research_notes/macos_hypervisor_and_host_primitives.md` | Hypervisor.framework API facts (arm64 and Intel), XNU interception limits, kqueue/shm/madvise/utun/vmnet equivalents, signal layout. One erratum is marked inline (`hv_vcpu_invalidate_tlb` is macOS 27+). |
| `research_notes/go_darwin_apple_silicon_constraints.md` | Go-on-Darwin facts: libSystem syscalls, cgo-free bindings, 16 KiB pages, W^X, PAC, Rosetta, CI runners, codesign. |
| `research_notes/prior_art_and_ecosystem.md` | maci0/gvisor fork, Noah, Carrick, Apple `container`, Lima/Docker Desktop, upstream issue #1270 and the Oct 2026 CNCF post. |

## Decisions already made (do not re-litigate without new data)

- Hypervisor.framework only, Apple Silicon (arm64) only. ptrace and systrap cannot be ported; Intel Macs are end-of-life.
- Phase 1 runs the Sentry on the host with a small EL1 stub in the guest (the maci0 fork's model). The in-guest Sentry (KVM model) is a Phase 3 spike whose measurements belong in Phase 0.
- A fixed pool of vCPU-owning locked threads with a systrap-style spin-then-futex hand-off and task affinity. KVM's `machine.Get()` vCPU rebinding cannot be ported because HVF binds a vCPU to its creating thread.
- `pagesize_16k` variant of `hostarch`/`ring0`/`pagetables` for Phase 1, including the vDSO linker-script `_params` offset. A 4 KiB guest view is a Phase 2 option gated on a measured compatibility need.
- On-demand IPA registry (B), decided 2026-11-06 from the Phase 0 data (`memory-mapping-options.md`). HVF stage 2 does not follow host VA changes, so a KVM-style whole-VA mapping (A) is not possible. `MapFile` maps `memmap.File` ranges with `hv_vm_map`, and the owner of a host mapping calls `hv_vm_unmap` before it unmaps or reuses it. Decommit is `F_PUNCHHOLE` for an APFS memory file, and unmap, release, remap on fault otherwise.
- The EL1 stub carries the fast-path table, EL0 ID-register `MRS` emulation, and a TLBI mailbox executed on every guest entry (a host-side Sentry cannot `tlbi`; `hv_vcpu_invalidate_tlb` is macOS 27+).
- Lazy FP/SIMD via the systrap `PullFullState` contract, with FP save/restore on every vCPU hand-off between different tasks.
- Hypervisor.framework bindings are cgo-free and in tree at `pkg/hvf`; the platform is in tree at `pkg/sentry/platform/hvf`; darwin host shims for the leaf packages are in tree, aimed at upstream.
- A design doc goes on google/gvisor#1270 after Phase 0 and before any platform code lands.

## Status

Phase 0 probes 1-10 are done (2026-11-06, Apple M2 Max, macOS 26.6.2). The
results and a summary are in `phase0-results.md`; the probes are in
`tools/hvf-probe/` on the `macos-port/phase0-probe` branch. Still open: the
KVM comparison for probe 1 on an arm64 Linux host (`tools/hvf-probe/kvm/`),
and the design doc on google/gvisor#1270. The A/B decision is (B).

The darwin build is in progress on `macos-port/darwin-shims` (`darwin-build.md`
has the host setup and the list of failing packages). Native Bazel works on the
Mac. The first layer of leaf packages is done (see the progress table in
`darwin-build.md`). Next: the second layer (`hostmm`, `fdnotifier` and
`socket/unix/transport`, `lisafs`, `stateio`). `safecopy` has a darwin signal
handler, so package tests run on darwin.

## Phase 0 probe plan (done)

A C probe (not Go, so the numbers are free of cgo and scheduler noise), ad-hoc
signed with the `com.apple.security.hypervisor` entitlement, measuring on the
MacBook:

1. `hv_vcpu_run` round-trip latency with clean and dirty FP state. Also run the same probe under KVM on an arm64 Linux box so the comparison is user-space exit against user-space exit.
2. `hv_vm_get_max_vcpu_count`; `hv_vcpu_create`/`destroy` cost; what happens to a vCPU whose owning thread exits without `hv_vcpu_destroy`.
3. `hv_vcpus_exit` semantics: is a cancel issued before `hv_vcpu_run` sticky; is PC/FP state preserved on `HV_EXIT_REASON_CANCELED`.
4. A blocked pending signal (SIGURG) on the vCPU thread does not make `hv_vcpu_run` fail.
5. `hv_vm_map` of read-only and read-write `MAP_SHARED` file-backed ranges and of anonymous ranges.
6. `F_PUNCHHOLE` and `MADV_FREE_REUSABLE` on stage-2-mapped pages, and the cost of `hv_vm_unmap` → release → refault-remap.
7. Cost of bouncing a set of vCPUs and executing a TLBI on re-entry.
8. Latency of a spin-then-futex (`os_sync_wait_on_address`) hand-off between two threads.
9. Cost of a `CPACR_EL1.FPEN` trap taken as a host exit.
10. Sanity checks: `TCR_EL1.TG0=4K` and `=16K` from a cold vCPU; `hv_vm_config_get_max_ipa_size`.

Suggested layout: `tools/hvf-probe/` on the `macos-port/phase0-probe` branch of
this fork, with a `Makefile` that builds, signs (`codesign -s - --entitlements
hvf.entitlements`) and runs each probe and writes results to
`g3doc/proposals/macos-port/phase0-results.md`.

## Prompt to paste into Claude Code on the MacBook

Start Claude Code inside the gVisor fork checkout on the `macos-port/research`
branch (or a worktree of it) and paste:

```
I am continuing the gVisor-on-macOS work. Read g3doc/proposals/macos-port/RESUME.md
first, then g3doc/proposals/macos-port/feasibility-report.md. The design decisions
in RESUME.md are settled; do not re-open them without new measurements.

Today's goal is Phase 0: the C probe described in RESUME.md. Steps:
1. Run `sw_vers; sysctl kern.hv_support hw.pagesize machdep.cpu.brand_string;
   xcode-select -p; go version` and confirm kern.hv_support is 1.
2. Create a git worktree on a new branch macos-port/phase0-probe from
   macos-port/research. Never edit on master.
3. Build the probe under tools/hvf-probe/ one measurement at a time, starting with
   hv_vcpu_run round-trip latency. Each probe is a small C program, ad-hoc signed
   with the com.apple.security.hypervisor entitlement via a Makefile target.
   Use hvc #imm with imm != 0 for the guest-to-host exit, program every system
   register yourself (HVF has no vCPU reset state), and set the EL via HV_REG_CPSR.
4. Record every number with the chip model and macOS version in
   g3doc/proposals/macos-port/phase0-results.md as you go.
5. Commit after each working probe and push to the milantracy remote.
Ask me before anything that needs sudo or a developer account.
```

## Things to know on the Mac

- No paid developer account is needed. `codesign -s - --entitlements <plist>` is enough for Hypervisor.framework.
- GitHub-hosted macOS runners cannot run HVF (they are VMs). Phase 0 numbers come from this MacBook.
- The gVisor tree does not build with plain `go build` (Bazel generates code). For the probe this does not matter; for later Go work use the `go` branch or a Bazel darwin build.
- The maci0/gvisor fork (https://github.com/maci0/gvisor, branch `master`, docs in `README-MACOS.md`, `docs/SYSCALL-SPEEDUP.md`, `pkg/sentry/platform/hvf/`) is a map and test oracle, not a merge base. It is 2,400+ commits behind upstream and its vCPU lifecycle panics at the cap.
