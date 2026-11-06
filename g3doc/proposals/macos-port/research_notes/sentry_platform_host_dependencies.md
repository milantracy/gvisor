# Sentry platform / memory / signal layers: Linux-host dependencies and what a macOS port must replace

Scope: local checkout `/home/jing/workspace/gvisor` at commit `f51a205a8` (master, 2026-11-06). All citations are `path:line` into that tree; line numbers refer to the file as checked out. No files were modified. Where a finding is about macOS behaviour rather than about this repository, it is placed under *Inferences* (it is not sourced from the repo) and marked "needs verification" when I am not certain.

Stage labels used throughout:

- **S1** = blocks a minimal demo (static busybox `echo hello` / `dmesg` on Apple Silicon via a Hypervisor.framework platform).
- **S2** = blocks full parity only (performance features, checkpoint/restore, networking, GPU, etc.).

---

## Q1. Which packages carry `//go:build linux` (or linux-only files) in the platform / memory / signal / host layers, and how big are they?

### Takeaway
Very few files use an explicit `linux` build tag (54 Go files in all of `pkg/`, most in `tcpip/link`, `shim/`, `abi/linux`); the real Linux coupling is implicit, through `golang.org/x/sys/unix` constants and raw syscalls spread across ~25 packages. The repo already contains three `darwin` stub files (`platforms_darwin.go`, `sighandling_darwin.go`, `syserr/host_darwin.go`) that exist only to let nogo/tooling build on macOS, and a `select_system(darwin = [])` in the platforms BUILD; nothing in the Sentry runs on Darwin today.

### Cited Findings
- Explicit OS build tags in the relevant layers (full list from `grep '^//go:build' pkg | grep -E 'linux|darwin'`): [pkg/hostsyscall/hostsyscall.go:15](pkg/hostsyscall/hostsyscall.go#L15) `linux`; [pkg/sentry/platform/platforms/platforms.go:15](pkg/sentry/platform/platforms/platforms.go#L15) `linux && !debug`; [pkg/sentry/platform/platforms/platforms_darwin.go:15](pkg/sentry/platform/platforms/platforms_darwin.go#L15) `darwin` ("This file makes the platforms package buildable on Darwin", line 20); [pkg/sentry/platform/platforms/platforms_amd64.go:15](pkg/sentry/platform/platforms/platforms_amd64.go#L15) `amd64 && linux && !debug` (imports `slimvm`); [pkg/sentry/platform/ptrace/subprocess_linux.go:15](pkg/sentry/platform/ptrace/subprocess_linux.go#L15), [subprocess_linux_unsafe.go:15](pkg/sentry/platform/ptrace/subprocess_linux_unsafe.go#L15) `linux && (amd64 || arm64)`; [pkg/sentry/platform/systrap/subprocess_linux.go:15](pkg/sentry/platform/systrap/subprocess_linux.go#L15) `linux`, [subprocess_linux_unsafe.go:15](pkg/sentry/platform/systrap/subprocess_linux_unsafe.go#L15) `amd64 || linux`; [pkg/sentry/platform/kvm/seccomp_mmap_unsafe.go:15](pkg/sentry/platform/kvm/seccomp_mmap_unsafe.go#L15) `linux`; [pkg/sighandling/sighandling_linux_unsafe.go:15](pkg/sighandling/sighandling_linux_unsafe.go#L15) `linux`, [sighandling_darwin.go:15](pkg/sighandling/sighandling_darwin.go#L15) `darwin`; [pkg/memutil/memfd_linux_unsafe.go:15](pkg/memutil/memfd_linux_unsafe.go#L15) `linux`; [pkg/syserr/host_linux.go:15](pkg/syserr/host_linux.go#L15) / [host_darwin.go:15](pkg/syserr/host_darwin.go#L15); [pkg/fdnotifier/fdnotifier.go:15](pkg/fdnotifier/fdnotifier.go#L15), [poll_unsafe.go:15](pkg/fdnotifier/poll_unsafe.go#L15) `linux`; [pkg/flipcall/futex_linux.go:15](pkg/flipcall/futex_linux.go#L15); [pkg/aio/aio_linux_unsafe.go:15](pkg/aio/aio_linux_unsafe.go#L15); [pkg/rawfile/rawfile_unsafe.go:15](pkg/rawfile/rawfile_unsafe.go#L15); [pkg/abi/abi_linux.go:15](pkg/abi/abi_linux.go#L15) (`const Host = Linux`, line 19); [pkg/rand/rand.go:15](pkg/rand/rand.go#L15) `!linux` (a non-Linux fallback already exists); [pkg/fdchannel/fdchannel_unsafe.go:15](pkg/fdchannel/fdchannel_unsafe.go#L15) `aix || darwin || ... || linux`; [pkg/sentry/hostfd/hostfd_linux.go:15](pkg/sentry/hostfd/hostfd_linux.go#L15) `!false` (always built despite the name).
- The Darwin stubs are non-functional: `sighandling_darwin.go` returns `errors.New("IgnoreChildStop not supported on Darwin")` and `errors.New("ReplaceSignalHandler not supported on Darwin")` — [pkg/sighandling/sighandling_darwin.go:28-40](pkg/sighandling/sighandling_darwin.go#L28). `syserr/host_darwin.go` keeps a 107-entry errno table and notes "We currently only add translations for errors that exist both on Darwin and Linux" (TODO gvisor.dev/issue/1270) — [pkg/syserr/host_darwin.go:26-44](pkg/syserr/host_darwin.go#L26).
- The platforms BUILD uses `select_system(... darwin = [])` so no platform is linked on Darwin — [pkg/sentry/platform/platforms/BUILD:25](pkg/sentry/platform/platforms/BUILD#L25); `tools/bazeldefs/defs.bzl:56-58` defines `select_system(linux=..., darwin=...)` keyed on `@bazel_tools//src/conditions:darwin`.
- README says only tooling runs on macOS: "Some packages support running tests directly on macOS ... `bazelisk test --macos_sdk_version=... -- //tools/nogo/... //tools/check{aligned,const,escape,linkname,locks,unsafe}/...`" — [README.md:124-133](README.md#L124).
- The 2026 CNCF blog lists "gVisor-on-Mac: Allowing Linux programs to run on macOS, with a similar experience as to how Wine allows Windows programs on macOS" as a non-commercial direction — [website/blog/2026-10-02-gvisor-cncf.md:91-92](website/blog/2026-10-02-gvisor-cncf.md#L91).
- Non-test line counts (Go / tests / asm+C), measured with `wc -l`:

| Package | Go (non-test) | tests | asm / C |
|---|---:|---:|---:|
| pkg/sentry/platform (interface) | 877 | 0 | 0 |
| pkg/sentry/platform/kvm | 7,940 | 2,372 | 351 |
| pkg/sentry/platform/kvm/testutil | 318 | 0 | 349 |
| pkg/sentry/platform/kvm/mmapfilter | 61 | | |
| pkg/sentry/platform/slimvm (amd64, Google-internal kernel module) | 3,615 | | |
| pkg/sentry/platform/systrap | 6,045 | 688 | 533 |
| pkg/sentry/platform/systrap/sysmsg | 650 | 0 | 1,623 (C + .S) |
| pkg/sentry/platform/systrap/usertrap | 934 | | |
| pkg/sentry/platform/ptrace | 2,294 | 47 | 243 |
| pkg/sentry/platform/interrupt | 121 | 142 | |
| pkg/sentry/platform/platformdesc | 127 | | |
| pkg/ring0 | 2,024 | 0 | 2,055 (entry_arm64.s 852, entry_amd64.s 779) |
| pkg/ring0/pagetables | 2,212 | 534 | 46 |
| pkg/hostarch | 1,007 | 306 | 0 |
| pkg/hostsyscall | 238 (incl. .s) | | |
| pkg/hosttid | 30 | 107 | 57 |
| pkg/hostos | 137 | 91 | 0 |
| pkg/goid | 28 | 81 | 52 |
| pkg/sentry/hostmm | 389 | 0 | 0 |
| pkg/sentry/hostcpu | 67 | 52 | 52 |
| pkg/sentry/pgalloc | 4,444 (pgalloc.go 2,063; save_restore.go 2,257) | 1,564 | 0 |
| pkg/sentry/memmap | 918 | 260 | 0 |
| pkg/sentry/usage | 686 | 0 | 0 |
| pkg/memutil | 112 | 0 | 0 |
| pkg/safecopy | 547 | 642 | 1,219 |
| pkg/safemem | 980 | 491 | 0 |
| pkg/sigframe (amd64 only) | 210 | | |
| pkg/sighandling | 256 | 0 | 0 |
| pkg/seccomp | 3,369 | 2,108 | 0 |
| runsc/boot/filter + config | 97 + 1,517 | | |
| pkg/sync | 1,384 | 471 | 184 |
| pkg/sleep | 478 | 689 | 0 |
| pkg/fdnotifier | 322 | 87 | 0 |
| pkg/eventfd | 219 | 75 | 0 |
| pkg/unet | 833 | 711 | 0 |
| pkg/fd | 281 | 138 | 0 |
| pkg/rawfile | 331 | 0 | 83 |
| pkg/cpuid | 2,679 | 794 | 38 |
| pkg/sentry/time | 1,392 | 1,137 | 377 |
| pkg/sentry/arch | 2,846 | 0 | 0 |
| pkg/sentry/arch/fpu | 768 | | |
| vdso/ (guest vDSO, C) | 904 | | |
| pkg/sentry/kernel | 26,109 | 2,724 | 0 |
| pkg/sentry/mm | 7,207 | | |
| pkg/sentry/loader | 1,690 | | |
| pkg/sentry/syscalls/linux | 16,875 | | |
| pkg/sentry/vfs | 12,810 | | |
| pkg/sentry/fsimpl/proc | 6,363 | | |
| pkg/sentry/fsimpl/tmpfs | 5,021 | | |
| pkg/sentry/fsimpl/gofer | 10,441 | | |
| pkg/lisafs | 7,379 | | |
| pkg/tcpip (whole tree) | 92,362 | | |
| pkg/sentry (whole tree) | 222,815 | | |

### Inferences
- Because coupling is through `unix.*` rather than build tags, a port cannot be scoped by "files with `linux` tags"; it must be scoped by the syscall inventory in Q10–Q14 below.
- The existing Darwin stubs (`sighandling_darwin.go` returning errors) mean any package whose `init()` installs a signal handler (`pkg/safecopy`, `pkg/sentry/platform/kvm`) cannot even initialise on Darwin today; see Q11.

### Gaps
- The `slimvm` platform depends on a host kernel module that is not in the repo (`b/529809802`); I did not analyse it further than noting it reuses `ring0`, `pagetables`, bluepill and KVM-style constants (`slimvm_const.go:19-37`).

---

## Q2. What is the `platform.Platform` / `Context` / `AddressSpace` contract, and what are the "Requirements"?

### Takeaway
The platform interface is small and host-agnostic in shape (~20 methods); the only Linux leakage in the interface itself is `SeccompInfo()` (seccomp-BPF rules), `hostmm.HostMemBarrier` for `membarrier(2)`, `linux.SignalInfo` as the fault-report type, and `platform.SignalInterrupt = linux.SIGCHLD`. The old `Requirements` struct now lives in `platformdesc` and only records `RequiresCapSysPtrace` / `FrequentHostThreadWakeups`.

### Cited Findings
- `Platform` interface methods: `SupportsAddressSpaceIO`, `DetectsCPUPreemption`, `HaveGlobalMemoryBarrier`, `MapUnit`, `MinUserAddress`, `MaxUserAddress`, `NewAddressSpace(AddressSpaceOptions)`, `NewContext(context.Context)`, `PreemptCPU(int32)`, `PreemptAllCPUs`, `GlobalMemoryBarrier`, `SeccompInfo() SeccompInfo`, `ConcurrencyCount`, `HasCPUNumbers`, `NumCPUs` — [pkg/sentry/platform/platform.go:41-121](pkg/sentry/platform/platform.go#L41). Optional `TSCAdjustablePlatform` (`TSCOffset`, `TSCFrequency`) — lines 125-133.
- `Context` interface: `Switch(ctx, mm MemoryManager, ac *arch.Context64, cpu int32) (*linux.SignalInfo, hostarch.AccessType, error)`, `PullFullState`, `FullStateChanged`, `Interrupt`, `Preempt`, `Release`, `PrepareSleep`, `PrepareUninterruptibleSleep`, `PrepareStop`, `PrepareExecve`, `PrepareExit`, `LastCPUNumber` — [platform.go:223-319](pkg/sentry/platform/platform.go#L223). Return sentinels: `ErrContextSignal`, `ErrContextInterrupt`, `ErrContextCPUPreempted` — lines 339-361. `const SignalInterrupt = linux.SIGCHLD` — line 371.
- `AddressSpace` interface: `MapFile(addr, f memmap.File, fr memmap.FileRange, at hostarch.AccessType, precommit bool) error`, `Unmap`, `Release`, `PreFork`, `PostFork`, `UserModifiedGS`, plus embedded `AddressSpaceIO` (`CopyOut`, `CopyIn`, `ZeroOut`, `SwapUint32`, `CompareAndSwapUint32`, `LoadUint32`) — [platform.go:375-469](pkg/sentry/platform/platform.go#L375). `NoAddressSpaceIO` panics for all of these (lines 472-502); KVM uses it (`address_space.go:70`).
- The memory-barrier helpers wrap `hostmm.HostMemBarrier` (`membarrier(2)`): `UseHostGlobalMemoryBarrier`, `UseHostProcessMemoryBarrier` — [platform.go:171-207](pkg/sentry/platform/platform.go#L171); `hostmm/membarrier.go:55,70,95,111` issue `SYS_MEMBARRIER` with `MEMBARRIER_CMD_GLOBAL`, `PRIVATE_EXPEDITED`, `QUERY`, `REGISTER_PRIVATE_EXPEDITED`.
- `SeccompInfo` interface (`Variables`, `ConfigKey`, `SyscallFilters`, `HottestSyscalls`) and `StaticSeccompInfo` — [platform.go:529-603](pkg/sentry/platform/platform.go#L529). `Options` passed to constructors: `DeviceFile *fd.FD`, `DisableSyscallPatching`, `DisableFastPath`, `ApplicationCores`, `UseCPUNums`, `SandboxID`, `StartupTimer`, `PinRing`, `TSCOffset` — lines 606-642. Registry: `Register`, `List`, `Lookup` — lines 659-682.
- `Requirements` is in `pkg/sentry/platform/platformdesc/platformdesc.go:29-37` with only `RequiresCapSysPtrace` and `FrequentHostThreadWakeups`; the table `descs` lists `kvm` (Device `/dev/kvm`), `ptrace` (CapSysPtrace), `systrap` (CapSysPtrace + FrequentHostThreadWakeups) — lines 49-66. No `RequiresExternalAddressSpace` exists anywhere in the tree (grep over `pkg/sentry/platform`, `pkg/sentry/kernel`, `runsc/boot` returned only platformdesc).
- The kernel's run loop consumes this contract at [pkg/sentry/kernel/task_run.go:273](pkg/sentry/kernel/task_run.go#L273) (`t.p.Switch(t, t.MemoryManager(), t.Arch(), t.rseqCPU)`), handling `ErrContextInterrupt` (292), `ErrContextSignal` (297), `SignalInterrupt` (373) and `ErrContextCPUPreempted` (389).
- `platform.MMapMinAddr` reads `/proc/sys/vm/mmap_min_addr` at init — [pkg/sentry/platform/mmap_min_addr.go:27-48](pkg/sentry/platform/mmap_min_addr.go#L27) (S1: trivially replaceable with a constant).

### Inferences
- A new `hvf` platform would implement exactly these interfaces; `SeccompInfo()` could return empty rules (seccomp is not installed on macOS anyway), and `HaveGlobalMemoryBarrier()` could return false (the Sentry then falls back to IPI-free paths; KVM uses `UseHostProcessMemoryBarrier`). Both are S2 performance items, not S1 blockers.
- `linux.SignalInfo` is the *guest* ABI (the Sentry synthesises it from the fault), so it is fine to keep on macOS.

### Gaps
- None for the interface itself.

---

## Q3. KVM host contract: ioctls, capabilities, memory slots, physical map, how Sentry memory is mapped into the guest

### Takeaway
KVM maps the *entire Sentry host address space* into the guest as guest-physical memory with an injective `physical = virtual + offset` scheme: it pads the host VA space with `PROT_NONE` mmaps so that it is no larger than the guest-physical space, computes contiguous "physical regions", and lazily installs `KVM_SET_USER_MEMORY_REGION` slots in `faultBlockSize` (1 GiB or 8 GiB) chunks when the guest first touches them. The slot limit comes from `KVM_CAP_NR_MEMSLOTS` (default 256) and is the reason for the large fault-block size. Application memory is simply the Sentry's own `mmap(MAP_SHARED)` of the memfd, translated to guest-physical and installed in the per-address-space page tables.

### Cited Findings
- `/dev/kvm` and VM creation: `KVM_CREATE_VM` via `unix.Syscall(SYS_IOCTL, fd, KVM_CREATE_VM, 0)` — [pkg/sentry/platform/kvm/kvm.go:103](pkg/sentry/platform/kvm/kvm.go#L103); refuses to run unless `unix.Getpagesize() == hostarch.PageSize` — [kvm.go:81-83](pkg/sentry/platform/kvm/kvm.go#L81).
- All ioctl numbers are hard-coded Linux values: `KVM_CREATE_VM 0xae01, KVM_GET_VCPU_MMAP_SIZE 0xae04, KVM_CREATE_VCPU 0xae41, KVM_SET_TSS_ADDR 0xae47, KVM_RUN 0xae80, KVM_NMI, KVM_CHECK_EXTENSION 0xae03, KVM_GET/SET_TSC_KHZ, KVM_INTERRUPT, KVM_SET_MSRS, KVM_SET_USER_MEMORY_REGION 0x4020ae46, KVM_IOEVENTFD 0x4040ae79, KVM_SET/GET_REGS, KVM_SET/GET_SREGS, KVM_GET_SUPPORTED_CPUID, KVM_SET_CPUID2, KVM_SET_SIGNAL_MASK 0x4004ae8b, KVM_GET/SET_VCPU_EVENTS, KVM_SET_DEVICE_ATTR, KVM_ENABLE_CAP` — [kvm_const.go:22-46](pkg/sentry/platform/kvm/kvm_const.go#L22). Exit reasons `_KVM_EXIT_EXCEPTION ... _KVM_EXIT_ARM_NISV 0x1c` — lines 51-62. Capabilities probed: `_KVM_CAP_MAX_MEMSLOTS 0x0a, _KVM_CAP_MAX_VCPUS 0x42, _KVM_CAP_ARM_VM_IPA_SIZE 0xa5, _KVM_CAP_ARM_PTRAUTH_ADDRESS/GENERIC, _KVM_CAP_VCPU_EVENTS, _KVM_CAP_ARM_INJECT_SERROR_ESR, _KVM_CAP_TSC_CONTROL` — lines 67-74. Defaults `_KVM_NR_MEMSLOTS 0x100, _KVM_NR_VCPUS 0xff` — lines 79-80. Slot flags `_KVM_MEM_READONLY` — line 88.
- arm64 one-reg ids (`_KVM_GET/SET_ONE_REG`, `_KVM_ARM_PREFERRED_TARGET 0x8020aeaf`, `_KVM_ARM_VCPU_INIT 0x4020aeae`, `_KVM_ARM64_REGS_{PSTATE,SP_EL1,R0..,PC,MAIR_EL1,TCR_EL1,TTBR0_EL1,TTBR1_EL1,SCTLR_EL1,CPACR_EL1,VBAR_EL1,TIMER_CNT,CNTFRQ_EL0,MDSCR_EL1,CNTKCTL_EL1,TPIDR_EL1, AP*KEY*_EL1}`) — [kvm_const_arm64.go:19-60](pkg/sentry/platform/kvm/kvm_const_arm64.go#L19); vCPU feature bits `_KVM_ARM_VCPU_PSCI_0_2=2, _KVM_ARM_VCPU_PTRAUTH_ADDRESS=5, _GENERIC=6` — lines 147-150; TCR encodings for 4K/64K granules only (`_TCR_TG0_4K`, `_TCR_TG0_64K`, `_TCR_TG1_4K`, `_TCR_TG1_64K`) — lines 114-119.
- Memory slot installation: `setMemoryRegion(slot, physical, length, virtual, flags)` fills `userMemoryRegion{slot, flags, guestPhysAddr, memorySize, userspaceAddr}` and issues `KVM_SET_USER_MEMORY_REGION` — [machine_unsafe.go:49-65](pkg/sentry/platform/kvm/machine_unsafe.go#L49). `mapMemorySlot` takes the next free slot with an atomic protocol, retries on `EEXIST`, throws "out of slots" on `EINVAL` — [bluepill_fault.go:76-123](pkg/sentry/platform/kvm/bluepill_fault.go#L76). `maxSlots` from `KVM_CHECK_EXTENSION(_KVM_CAP_MAX_MEMSLOTS)` else 256 — [machine.go:322-329](pkg/sentry/platform/kvm/machine.go#L322).
- Fault-block size: "This should be large enough so that the total number of slots required to cover the 47-bit virtual address space does not exceed the KVM slot limit (e.g. 32764)" — [bluepill_fault.go:26-35](pkg/sentry/platform/kvm/bluepill_fault.go#L26); amd64 uses 8 GiB (`machine_amd64.go:607-609`) or 1<<42 when mapping the whole address space (`archOverrideFaultBlocks`, lines 612-616); arm64 uses 1 GiB if `PhysicalAddressBits <= 39`, else 8 GiB — [machine_arm64.go:252-261](pkg/sentry/platform/kvm/machine_arm64.go#L252).
- Physical map construction: `fillAddressSpace()` computes `vSize = ring0.UserspaceSize`, `pSize = 1 << ring0.PhysicalAddressBits - reservedMemory`, parses `/proc/self/maps` (`applyVirtualRegions`), and mmaps `PROT_NONE|MAP_ANONYMOUS|MAP_PRIVATE|MAP_NORESERVE` filler until `vSize < pSize` — [physical_map.go:58-209](pkg/sentry/platform/kvm/physical_map.go#L58); `computePhysicalRegions` assigns `physical` offsets starting at `reservedMemory`, aligned to `faultBlockSize` — lines 212-266; `translateToPhysical` is a linear scan — lines 293-303. `reservedMemory = 0x100000000` on amd64 ([physical_map_amd64.go:29](pkg/sentry/platform/kvm/physical_map_amd64.go#L29)) and `0` on arm64 ([physical_map_arm64.go:18](pkg/sentry/platform/kvm/physical_map_arm64.go#L18)).
- `/proc/self/maps` is parsed with a regexp — [virtual_map.go:37-38, 66-68](pkg/sentry/platform/kvm/virtual_map.go#L37); regions excluded when `excludeVvarRegion` and name starts with `[vvar`/`[vdso` (lines 47-49); the ioeventfd MMIO mapping is marked as MMIO (lines 56-58). `excludeVvarRegion` is decided by probing whether a read-only memslot over `[vvar]` is accepted, gated on host kernel version 6.17 ≤ v < 7.1 — [kvm_arm64_unsafe.go:62-97](pkg/sentry/platform/kvm/kvm_arm64_unsafe.go#L62), using `hostos.KernelVersion()` (`uname`).
- arm64 additionally splits physical regions at read-only host mappings so they can be installed with `_KVM_MEM_READONLY` ("Skip PROT_NONE mappings. Go-runtime uses them as place holders") — [machine_arm64.go:114-187](pkg/sentry/platform/kvm/machine_arm64.go#L114).
- `newMachine` sequence: `getMaxVCPU` (min(`runtime.NumCPU()`, `KVM_CAP_MAX_VCPUS`) on arm64, `machine_arm64.go:235-250`), `kernel.Init`, slot cap, TSC-control cap, `initShadowVDSO`, `EnableGuestAliasedOffset`, create upper-shared page tables + `mapUpperHalf` + `MarkReadOnlyShared`, then either `archOverrideFaultBlocks()` (map entire address space) or `seccompMmapRules(m)`; `applyPhysicalRegions` maps every lower-half physical region RW into the kernel page tables; `applyVirtualRegions` → `mapRegion` maps executable regions and calls `mapPhysical` for every live VMA (growing `[stack]` down 1 MiB), bracketed by `disableAsyncPreemption()/enableAsyncPreemption()` (SIGURG block); finally `initArchState` — [machine.go:300-478](pkg/sentry/platform/kvm/machine.go#L300). `mapEntireAddressSpaceDefault` is `true` on arm64 ([machine_arm64.go:252](pkg/sentry/platform/kvm/machine_arm64.go#L252)) and `false` on amd64 ([machine_amd64.go:605](pkg/sentry/platform/kvm/machine_amd64.go#L605)); cgo builds force it (`machine_cgo.go:21-27`, "libc calls mmap with blocked signals").
- `mapPhysical` loops over `calculateBluepillFault` blocks and calls `mapMemorySlot` unless `hasSlot(physicalStart)` — [machine.go:507-524](pkg/sentry/platform/kvm/machine.go#L507).
- Upper half (arm64): every physical region is mapped at `ring0.KernelStartAddress | pr.virtual` with `Global: true` into the shared upper page tables; the shadow vDSO gets an RX alias at the real vDSO VA and an R alias at its own VA — [machine_arm64.go:60-112](pkg/sentry/platform/kvm/machine_arm64.go#L60).
- Application mappings: `addressSpace.MapFile` calls `f.MapInternal(fr, ...)` to get the Sentry's own host mappings of the memmap.File, optionally touches each page (`precommit`), then for each block calls `bluepill(cpu)` and `mapLocked`, which `translateToPhysical`s the host VA, `mapPhysical`s it, and installs user PTEs (`pageTables.Map(addr, length, MapOpts{AccessType, User: true, MemoryType}, physical)`) — [address_space.go:112-205](pkg/sentry/platform/kvm/address_space.go#L112). The per-AS allocator must be in guest mode when touching PTEs ("Due to the way KVM's shadow paging implementation works, modifications to the page tables while in host mode may not be trapped") — [bluepill_allocator.go:28-33](pkg/sentry/platform/kvm/bluepill_allocator.go#L28).
- `MapUnit()` is 16 MiB ("We greedily creates PTEs in MapFile") — [kvm.go:142-147](pkg/sentry/platform/kvm/kvm.go#L142); `MaxUserAddress = ring0.MaximumUserAddress` (line 157).
- arm64 IPA size: `ring0.PhysicalAddressBits` is lowered to `KVM_CAP_ARM_VM_IPA_SIZE` if < 40, else 40 — [kvm_arm64.go:76-83](pkg/sentry/platform/kvm/kvm_arm64.go#L76); `arch.ConfigureAddressSpace(1 << 48)` and the check "gVisor doesn't support 3-level page tables on KVM platform. Try to recompile the kernel with CONFIG_ARM64_VA_BITS_48" — lines 97-102.
- Pointer authentication: `disableHostPAC()` uses `syscall.AllThreadsSyscall6(SYS_PRCTL, PR_PAC_SET_ENABLED_KEYS, keys, 0)` because "The PAC keys are per-process secrets and cannot be read from userspace, so there is no way to synchronise them between Guest EL1 and Host EL0" — [machine_arm64_unsafe.go:365-376](pkg/sentry/platform/kvm/machine_arm64_unsafe.go#L365) (comment lines 329-364).

### Inferences
- A Hypervisor.framework platform would replace `KVM_SET_USER_MEMORY_REGION` with `hv_vm_map(host_va, gpa, size, flags)` and `KVM_CREATE_VCPU`/`KVM_RUN` with `hv_vcpu_create`/`hv_vcpu_run`; the "physical = virtual + offset" injective scheme is reusable as-is because it only needs *some* way to map host VA ranges to guest-physical. Needs verification: whether `hv_vm_map` accepts `PROT_NONE` host ranges and lazily faults them in like KVM (KVM relies on `get_user_pages` at EPT-fault time and reports `EFAULT` from `KVM_RUN` for permission failures, `bluepill_unsafe.go:213-220`). If HVF requires committed, accessible memory, the whole-address-space mapping strategy (`mapEntireAddressSpaceDefault = true` on arm64) is not viable and the lazy per-fault approach, or mapping only the memfd/MemoryFile plus Go heap, must be used instead. **S1.**
- `/proc/self/maps` parsing (`virtual_map.go`) must be replaced with `mach_vm_region_recurse` enumeration. **S1.**
- Apple Silicon host page size is 16 KiB, so `hv_vm_map` granularity and all host mappings are 16 KiB; `kvm.go:81` already refuses mismatched host page sizes. A 16 KiB `hostarch.PageSize` build variant is a prerequisite (see Q6). **S1.**
- There is no documented memslot cap in HVF comparable to `KVM_CAP_NR_MEMSLOTS`, so `faultBlockSize` tuning is likely irrelevant; the `usedSlots`/`nextSlot` protocol (`bluepill_fault.go:76-123`) would be replaced by a simpler map. **S2 cleanup.**
- `disableHostPAC()` has no macOS equivalent; macOS arm64 processes use PAC heavily (`arm64e` ABI) in system libraries. Go binaries are plain arm64 (not arm64e), so the Go runtime itself does not use PAC, but libSystem does; running libSystem code at guest EL1 with mismatched keys would hit exactly the failure described in the comment. Needs verification; see Q4/Q12.

### Gaps
- HVF's behaviour on guest access to unmapped GPA (whether it reports an exit comparable to `KVM_EXIT_MMIO`, which gVisor uses for its hypercall at `getHypercallID`, `bluepill_arm64_unsafe.go:84-90`) is not determinable from the repo.

---

## Q4. How does bluepill enter guest mode, what do the signal handlers assume about Linux signal frames, and how are host signals (SIGILL/SIGSEGV/SIGBUS/SIGCHLD/SIGSYS/SIGURG, sigaltstack) used?

### Takeaway
On arm64 the Sentry enters guest mode by *deliberately executing an illegal instruction*: `bluepill()` loads the vCPU pointer into R8 and runs `MRS TPIDR_EL1` (undefined at EL0 → SIGILL); the SIGILL handler, installed with raw `rt_sigaction` behind the Go runtime's back, runs `bluepillHandler` on the signal stack, copies the Linux `ucontext.uc_mcontext` registers into the vCPU, loops on `ioctl(KVM_RUN)`, and on exit writes the guest registers back into the *same ucontext* so that `rt_sigreturn` resumes the host thread wherever the guest left it. Every part of this — SIGILL as entry, hard-coded `ucontext` offsets (`CONTEXT_PC 0x1B8`, `CONTEXT_R0 0xB8`), `arch.UContext64`/`SignalContext64` field layout, `rt_sigtimedwait` inside the handler, `tgkill(SIGCHLD)` to bounce a vCPU, `KVM_SET_SIGNAL_MASK`, SIGSYS seccomp traps for mmap, SIGURG masking — is Linux-signal-ABI specific.

### Cited Findings
- Handler installation bypasses Go: `ReplaceSignalHandler` does raw `SYS_RT_SIGACTION` get/set with `linux.SigAction`, saving the previous (Go runtime) handler for fallback — [pkg/sighandling/sighandling_linux_unsafe.go:52-77](pkg/sighandling/sighandling_linux_unsafe.go#L52). KVM arm64 installs it for `SIGILL` in `init()` — [bluepill_arm64.go:31, 142-147](pkg/sentry/platform/kvm/bluepill_arm64.go#L31); amd64 uses a different route (below). `savedHandler`, `savedSigsysHandler`, `bounceSignal = unix.SIGCHLD` ("not masked by the runtime"), `bounce = ring0.VirtualizationException` — [bluepill.go:41-64](pkg/sentry/platform/kvm/bluepill.go#L41).
- arm64 entry asm: `bluepill` puts `&c.CPU | 0xffff000000000000` in R9, executes `WORD $0xd538d08a // MRS TPIDR_EL1, R10` to trigger SIGILL, then checks R10 == R9 ("wrong vCPU" → `redpill`) — [bluepill_arm64.s:51-68](pkg/sentry/platform/kvm/bluepill_arm64.s#L51). `sighandler` checks `si_signo == 4` (SIGILL) at `SIGINFO_SIGNO 0x0`, reads `CONTEXT_PC(R2)` at `0x1B8`, and calls `bluepillHandler(ucontext)`; otherwise jumps to `savedHandler` — lines 78-96. `sigsysHandler` checks `si_code == 1` (SYS_SECCOMP) at `SIGINFO_CODE 0x8` and `R8 == SYS_MMAP (222)` before calling `seccompMmapHandler` — lines 110-127. `dieTrampoline` fakes a call frame for `dieHandler` — lines 136-141. `currentEL` reads `CurrentEL` — lines 150-154.
- Go side: `bluepillArchContext` casts the pointer to `*arch.UContext64` and returns `&MContext` — [bluepill_unsafe.go:59-61](pkg/sentry/platform/kvm/bluepill_unsafe.go#L59). `bluepillArchEnter` reads `c = vCPUPtr(context.Regs[8])`, copies `Regs/Sp/Pc/Pstate`, forces `KernelFlagsSet` (EL1h, DAIF masked), captures `TPIDR_EL0` — [bluepill_arm64.go:52-64](pkg/sentry/platform/kvm/bluepill_arm64.go#L52). `bluepillArchExit` writes regs back, sets `UserFlagsSet` (EL0t), restores `TPIDR_EL0`, and copies `Fpsr/Fpcr/Vregs` into `context.Fpsimd64` when lazy VFP was used — lines 69-86.
- The handler loop: `bluepillHandler` runs with "The world may be stopped ... executes on the signal stack ... only raw system calls and go:nosplit"; it swaps state to `vCPUGuest|vCPUUser`, loops `RawSyscallErrno(SYS_IOCTL, c.fd, KVM_RUN)`; on `EINTR` it drains the pending bounce signal with `SYS_RT_SIGTIMEDWAIT(bounceSignalMask, timeout 0, sigsetsize 8)` and either forces `_KVM_EXIT_IRQ_WINDOW_OPEN` or sets `requestInterruptWindow`; `EFAULT` → `bluepillSigBus`; `ENOSYS` → `bluepillHandleEnosys`; `ENOMEM/ENOSPC` → `exit_group(99)`; exit reasons `HLT` and MMIO-hypercall → `bluepillGuestExit`; others → `c.die` — [bluepill_unsafe.go:143-287](pkg/sentry/platform/kvm/bluepill_unsafe.go#L143).
- arm64 interrupt injection uses `KVM_SET_VCPU_EVENTS` with SError (`sErrPending`), SError-with-ESR `_ESR_ELx_SERR_NMI` ("Host must support ARM64_HAS_RAS_EXTN"), or `extDabtPending` — [bluepill_arm64_unsafe.go:95-171](pkg/sentry/platform/kvm/bluepill_arm64_unsafe.go#L95); `_KVM_EXIT_ARM_NISV` is handled by injecting an external data abort — lines 184-193.
- Bouncing a vCPU out of guest mode: `vCPU.bounce` loops `unix.Tgkill(pid, tid, bounceSignal)` (retrying `EAGAIN`) and waits with `futex` — [machine.go:785-855](pkg/sentry/platform/kvm/machine.go#L785); `notify`/`waitUntilNot` use `SYS_FUTEX` `FUTEX_WAKE/WAIT|FUTEX_PRIVATE_FLAG` on `c.state` — [machine_unsafe.go:122-150](pkg/sentry/platform/kvm/machine_unsafe.go#L122). `setSignalMask` issues `KVM_SET_SIGNAL_MASK` with everything but SIGCHLD masked so only the bounce signal interrupts `KVM_RUN` — lines 155-176. `disableAsyncPreemption/enableAsyncPreemption` block/unblock `SIGURG` via `SYS_RT_SIGPROCMASK` ("Go runtime async preemption") — lines 199-216.
- Host syscalls from the guest: `redpill()` is `RawSyscallErrno(^uintptr(0), ...)`, i.e. syscall number -1 (`_SYS_KVM_RETURN_TO_HOST`) — [bluepill.go:66-75](pkg/sentry/platform/kvm/bluepill.go#L66); it is allow-listed in the seccomp filter (`filters.go:58`). On arm64, the ring0 `KernelSyscall` hook rewinds `Pc -= 4` unless `Regs[8] == ^0`, saves FP state and `ring0.Halt()`s — [bluepill_arm64.go:93-110](pkg/sentry/platform/kvm/bluepill_arm64.go#L93); the amd64 comment explains the full round trip: SYSCALL → sysenter saves regs → KernelSyscall rewinds RIP and HLT → VM exit to bluepillHandler → sigreturn to the SYSCALL instruction on the host → later `KVM_RUN` resumes after HLT with the new thread's registers — [bluepill_amd64.go:113-133](pkg/sentry/platform/kvm/bluepill_amd64.go#L113).
- amd64 differs: it has no SIGSEGV/SIGILL entry; `bluepill()` checks `getcs()&3 == 0` and otherwise builds a synthetic signal frame with `sigframe.CallWithSignalFrame(&c.signalStack, addrOfBluepillUserHandler(), &sigmask(all), rax=c)`, and `bluepillUserHandler` calls `bluepillHandler` then `sigframe.Sigreturn` — [bluepill_amd64_unsafe.go:187-213](pkg/sentry/platform/kvm/bluepill_amd64_unsafe.go#L187). `sigframe` is amd64-only, uses `SYS_RT_SIGPROCMASK` and emits `rt_sigreturn` (syscall 15) with hard-coded `SignalContext64` offsets (`SIGCTX_R15 SIGMCTX+56`, `SIGCTX_SS SIGMCTX+150`, `SIGCTX_ERR SIGMCTX+152`) — [pkg/sigframe/sigframe_amd64_unsafe.go:30-55](pkg/sigframe/sigframe_amd64_unsafe.go#L30), [sigframe_amd64.s:26-79](pkg/sigframe/sigframe_amd64.s#L26). amd64 `loadSegments` calls `SYS_SIGALTSTACK` to record the thread's alt stack and `SYS_ARCH_PRCTL(ARCH_GET_FS/GS)` — [machine_amd64_unsafe.go:38-60](pkg/sentry/platform/kvm/machine_amd64_unsafe.go#L38); arm64 `loadSegments` only stores the TID ("TLS is not supported" TODO 1238) — [machine_arm64_unsafe.go:322-326](pkg/sentry/platform/kvm/machine_arm64_unsafe.go#L322). `sigaltstack` is not used at all on arm64 KVM (grep: only amd64 files reference `signalStack`).
- Signal-frame layout assumptions: `arch.SignalContext64` (arm64) = `FaultAddr, Regs[31], Sp, Pc, Pstate, _pad[8], Fpsimd64 FpsimdContext` and `UContext64` = `Flags, Link, Stack linux.SignalStack, Sigset, _pad[120], _pad2[8], MContext` ("glibc uses a 1024-bit sigset_t", "sigcontext must be aligned to 16-byte") — [pkg/sentry/arch/signal_arm64.go:33-75](pkg/sentry/arch/signal_arm64.go#L33); amd64 `SignalContext64` has `R8..R15, Rdi, Rsi, Rbp, Rbx, Rdx, Rax, Rcx, Rsp, Rip, Eflags, Cs, Gs, Fs, Ss, Err, Trapno, Oldmask, Cr2, Fpstate, Reserved[8]` — [signal_amd64.go:39-75](pkg/sentry/arch/signal_amd64.go#L39). Consumers of these types as *host* frames (not guest ABI): `kvm/bluepill*.go`, `slimvm/bluepill*.go`, `sigframe/sigframe_amd64_unsafe.go` (grep result); `arch/stack.go` uses them for the guest ABI only.
- seccomp-based mmap trap (amd64 default): `seccompMmapRules` installs a SIGSYS handler via `ReplaceSignalHandler(SIGSYS, addrOfSigsysHandler())` and loads the precompiled `kvm_mmap_trap` BPF program (`SYS_MMAP` without `PROT_EXEC`/`MAP_DENYWRITE` → `seccomp.Trap`) — [machine.go:915-931](pkg/sentry/platform/kvm/machine.go#L915), [mmapfilter/mmapfilter.go:33-52](pkg/sentry/platform/kvm/mmapfilter/mmapfilter.go#L33); `seccompMmapHandler` re-issues the mmap and `mapPhysical`s the result into every machine in `machinePool` — [seccomp_mmap_unsafe.go:31-69](pkg/sentry/platform/kvm/seccomp_mmap_unsafe.go#L31); the arm64 version pulls the syscall from `ctx.Regs[8]` and args from `Regs[0..5]` — [machine_arm64_unsafe.go:473-479](pkg/sentry/platform/kvm/machine_arm64_unsafe.go#L473).
- Sandbox seccomp allow-list for the KVM platform: `ioctl(KVM_RUN|KVM_SET_USER_MEMORY_REGION|KVM_IOEVENTFD|KVM_GET_REGS|KVM_SET_REGS)`, `membarrier(PRIVATE_EXPEDITED)`, `mmap`, `rt_sigsuspend`, `rt_sigtimedwait`, syscall -1 — [filters.go:25-62](pkg/sentry/platform/kvm/filters.go#L25); arm64 adds `KVM_SET_VCPU_EVENTS` ([filters_arm64.go:29-32](pkg/sentry/platform/kvm/filters_arm64.go#L29)); amd64 adds `arch_prctl(ARCH_GET_FS/GS)`, `KVM_INTERRUPT`, `KVM_NMI` and names `futex/ioctl/rt_sigreturn` as hottest ([filters_amd64.go:26-59](pkg/sentry/platform/kvm/filters_amd64.go#L26)).
- `safecopy` also installs raw SIGSEGV and SIGBUS handlers through `ReplaceSignalHandler` in `init()` — [pkg/safecopy/safecopy.go:135-137](pkg/safecopy/safecopy.go#L135), with an arm64 asm handler that hard-codes `SIGBUS 7`, `SIGSEGV 11` and chains to `savedSigBusHandler/savedSigSegVHandler` — [pkg/safecopy/sighandler_arm64.s:18-19, 124-128](pkg/safecopy/sighandler_arm64.s#L18); fault errors map `unix.SIGSEGV → SegvError`, `unix.SIGBUS → BusError` — [safecopy_unsafe.go:322-330](pkg/safecopy/safecopy_unsafe.go#L322).
- Other process-wide signal plumbing: `sighandling.IgnoreChildStop` (`SA_NOCLDSTOP` via `rt_sigaction`) — [sighandling_linux_unsafe.go:31-44](pkg/sighandling/sighandling_linux_unsafe.go#L31), called from [runsc/boot/loader.go:1028](runsc/boot/loader.go#L1028); `KillItself` uses `rt_tgsigqueueinfo` with `SI_KERNEL` — lines 88-103; `StartSignalForwarding` skips `SIGURG` ("used by Go's runtime scheduler"), `SIGPIPE`, `SIGCHLD` — [sighandling.go:92-104](pkg/sighandling/sighandling.go#L92).

### Inferences
- With Hypervisor.framework there is no `KVM_RUN`-returns-via-signal-frame mechanism: `hv_vcpu_run` is an ordinary blocking call on the owning thread. The bluepill design can therefore be re-implemented *without any signal handler*: save the host registers into the vCPU, program them with `hv_vcpu_set_reg`/`hv_vcpu_set_sys_reg`, call `hv_vcpu_run`, and on exit restore registers with a hand-written user-space context switch (the role `rt_sigreturn` plays today). The pieces that disappear are `sighandler`, `sigsysHandler`, `rt_sigtimedwait`, `KVM_SET_SIGNAL_MASK`, `tgkill` bounce (replaced by `hv_vcpus_exit`), and the ucontext offsets. Roughly the 1,000 lines of `bluepill*.go/.s` plus `sighandling` and the arm64 portions of `machine_arm64*.go` (~1,200 lines) would be rewritten. **S1.**
- The remaining raw-signal consumer that *must* exist on macOS is `pkg/safecopy` (SIGSEGV/SIGBUS fault recovery for user-memory access on platforms with `NoAddressSpaceIO`). Its Darwin version needs `sigaction` with `SA_SIGINFO|SA_ONSTACK` and a handler that understands the Darwin `ucontext_t` (`uc_mcontext` is a *pointer* to `__darwin_mcontext64` with `__ss`/`__ns` sub-structs, not inline as on Linux). The Go runtime on darwin installs its own handlers and expects to chain; the "save previous handler, fall back" pattern of `sighandling_linux_unsafe.go:52-77` is portable in concept. **S1.** Needs verification: whether the Go darwin runtime's `sigtramp` tolerates a foreign handler being installed via raw `sigaction` (on Linux gVisor relies on `savedHandler` chaining to `runtime.sigtramp`).
- `SIGURG` is also Go's async-preemption signal on darwin, so `disableAsyncPreemption` is portable in intent (`pthread_sigmask`). **S2** (only needed if page tables are modified under a spinlock as today).
- amd64 `sigframe` and SIGSYS/seccomp mmap trapping have no macOS analog; on Intel Macs the "map entire address space" path (`forceMappingEntireAddressSpace`, `machine_cgo.go:21-27`) is the only option. **S2 (Intel).**

### Gaps
- Darwin's `ucontext_t`/`mcontext64` layout for arm64 (offsets of `__ss.__pc`, `__ss.__x[0]`, `__ns` NEON state) is not in the repo; a port needs the SDK headers.

---

## Q5. ring0: size, EL1 assumptions, pagetables geometry, reusability under Hypervisor.framework on Apple Silicon

### Takeaway
`ring0` is ~4,100 lines (2,024 Go + 2,055 asm) and `ring0/pagetables` ~2,250 lines; both are pure guest-side EL1 code that touches only architectural registers (TTBR0/1, TCR, MAIR, SCTLR, VBAR, ELR/SPSR/ESR/FAR_EL1, TPIDR_EL0/EL1, CPACR, CNT*), not the host. They are reusable in principle under HVF on Apple Silicon, with two caveats: the arm64 code only exists for 4 KiB and 64 KiB granules (`pagesize_64k` tag), and `ring0.VirtualAddressBits` is fixed at 48 with `PhysicalAddressBits` 40.

### Cited Findings
- Files: `aarch64.go, aarch64_4k.go, aarch64_64k.go, defs.go, defs_arm64.go, entry_arm64.go, entry_arm64.s (852 lines), kernel.go, kernel_arm64.go, kernel_unsafe.go, lib_arm64.go, lib_arm64.s, ring0.go` plus amd64 counterparts (`x86.go`, `entry_amd64.s 779`, `kernel_amd64.go 345`) — directory listing of `pkg/ring0`.
- arm64 constants: "VirtualAddressBits is fixed at 48", `PhysicalAddressBits uintptr = 40` (a variable, lowered by KVM per `KVM_CAP_ARM_VM_IPA_SIZE`), PSR mode bits, `KernelFlagsSet = EL1h | D|A|I|F`, `UserFlagsSet = EL0t` — [pkg/ring0/aarch64.go:20-53](pkg/ring0/aarch64.go#L20); vector enum `El1Sync.. El0ErrNMI`, `Syscall = El0SyncSVC`, `PageFault = El0SyncDa`, `VirtualizationException = El0ErrBounce` — lines 61-111. `UserspaceSize = 1 << VirtualAddressBits`, `MaximumUserAddress = UserspaceSize`, `KernelStartAddress = ^0 - (UserspaceSize-1)` — [defs_arm64.go:20-29](pkg/ring0/defs_arm64.go#L20).
- Per-CPU arch state: `errorCode, errorType, faultAddr (far_el1), el0Fp, ttbr0Kvm, ttbr0App, vecCode, appAddr, lazyVFP (cpacr_el1), appASID`, and a 176-byte interrupt stack sized for the deepest Go call chain — [defs_arm64.go:36-85](pkg/ring0/defs_arm64.go#L36). `SwitchArchOpts{UserASID, KernelASID}` — lines 142-148.
- `SwitchToUser` (arm64): stores ASID and EL0 fpstate pointer, optional `LocalFlushTlbByASID`, sets `UserFlagsSet`, toggles FPSIMD trap, `kernelExitToEl0()`, saves FP on return, returns `c.vecCode` — [kernel_arm64.go:67-95](pkg/ring0/kernel_arm64.go#L67). `IsCanonical` is the 48-bit split — lines 56-58.
- Assembly uses `ERET`, `MRS/MSR TPIDR_EL1` as the per-CPU pointer, `TTBR0_EL1` swap between `CPU_TTBR0_KVM`/`CPU_TTBR0_APP`, `FAR_EL1`, `ESR_EL1`, `ELR_EL1`, `SPSR_EL1`, `TPIDR_EL0`, `VBAR_EL1` — [entry_arm64.s:27-29, 115-116, 338-354, 384-434, 511-605](pkg/ring0/entry_arm64.s#L338); ESR EC constants for SVC64 etc. — lines 217-250; `HaltEl1SvcAndResume`/`HaltEl1ExceptionAndResume` trampolines invoke the Go hooks — lines 485-501. TLB maintenance and FP save/restore helpers — [lib_arm64.s:20-161](pkg/ring0/lib_arm64.s#L20).
- `kernelAddr(obj)` = `KernelStartAddress | data pointer` (upper-half alias of Go heap objects) — [kernel_unsafe.go:29-39](pkg/ring0/kernel_unsafe.go#L29).
- Pagetable geometry is compile-time: 4K: `pteShift 12, pmdShift 21, pudShift 30, pgdShift 39, entriesPerPage 512` ([pagetables_arm64_4k.go:15-40](pkg/ring0/pagetables/pagetables_arm64_4k.go#L15), tag `arm64 && !pagesize_64k`); 64K: `pteShift 16, pmdShift 29, pgdShift 42, entriesPerPage 8192`, 3-level ([pagetables_arm64_64k.go](pkg/ring0/pagetables/pagetables_arm64_64k.go), tag `arm64 && pagesize_64k`); separate walkers `walker_arm64.go` (232 lines) and `walker_arm64_64k.go` (160 lines). `ring0/aarch64_4k.go` and `aarch64_64k.go` define `_PGD_PGT_BASE/_SIZE`, `_PTE_PGT_SIZE` (0x1000 vs 0x10000). The Bazel `pagesize_64k` tag is wired through `tools/bazeldefs/go.bzl:78,165` and `.bazelrc:55-56` (`--//tools/bazeldefs:page_size=64k`).
- PTE bits are standard VMSAv8-64 (`typeTable, typeSect, pteValid, AP[1]/AP[2], accessed, dbm, cont, pxn, xn, nG, shared, attrIndx`) — [pagetables_aarch64.go:54-80](pkg/ring0/pagetables/pagetables_aarch64.go#L54); `TTBR0_EL1`/`TTBR1_EL1` getters — lines 42-49. The `MAIR_EL1` value and "additional memory types must be configured in MAIR" guard live in KVM — [machine_arm64_unsafe.go:148-161](pkg/sentry/platform/kvm/machine_arm64_unsafe.go#L148), and `tcrTGFlags()` panics for any `hostarch.PageSize` other than 4096 or 65536 — [machine_arm64.go:263-278](pkg/sentry/platform/kvm/machine_arm64.go#L263).
- The guest sentry runs at EL1 via the vCPU init sequence: `KVM_ARM_PREFERRED_TARGET`, create all vCPUs up front (timer issue #5739), `KVM_ARM_VCPU_INIT` with `PSCI_0_2` (+ PTRAUTH), then `SET_ONE_REG` for `TCR_EL1`, `MAIR_EL1`, `TTBR0_EL1`, `TTBR1_EL1`, `CNTKCTL_EL1`, `CPACR_EL1`, `SCTLR_EL1`, `TPIDR_EL1`(=vCPU), `SP_EL1`, `PC`(=`ring0.AddrOfStart`), `VBAR_EL1`(=`KernelStartAddress | vectors`) and records the vector page's physical address as the hypercall MMIO base — [machine_arm64_unsafe.go:59-240](pkg/sentry/platform/kvm/machine_arm64_unsafe.go#L59).
- Guest faults are decoded from ESR: `hostarch.ESRAccessType(code)` ([pkg/hostarch/hostarch_arm64.go:67](pkg/hostarch/hostarch_arm64.go#L67)) and `_ESR_ELx_FSC` → `SEGV_MAPERR`/`SEGV_ACCERR` — [machine_arm64.go:202-233](pkg/sentry/platform/kvm/machine_arm64.go#L202); vector → signal mapping (`PageFault→SIGSEGV`, `El0ErrNMI→SIGBUS`, `El0SyncUndef/Inv→SIGILL`, `El0SyncSpPc→SIGBUS`, `El0SyncSys/Wfx` emulated) — [machine_arm64_unsafe.go:437-468](pkg/sentry/platform/kvm/machine_arm64_unsafe.go#L437).

### Inferences
- Hypervisor.framework on Apple Silicon exposes a guest with its own EL1 and lets the host set `TCR_EL1/TTBR*/SCTLR/VBAR/MAIR/CPACR/TPIDR*` via `hv_vcpu_set_sys_reg`, so the ring0 EL1 model (sentry at EL1, app at EL0, exceptions through `VBAR_EL1`) maps conceptually 1:1. **ring0 and pagetables are reusable with a 16 KiB granule variant added** (`pagetables_arm64_16k.go`/`walker_arm64_16k.go`, `aarch64_16k.go`, `hostarch_arm64_16k.go`, a `_TCR_TG0_16K/_TCR_TG1_16K` case in `tcrTGFlags`). Needs verification: whether Apple Silicon supports a 4 KiB *stage-1* granule in the guest while the host stage-2 uses 16 KiB; if yes, a 4 KiB guest granule could keep `hostarch.PageSize = 4096` for the application ABI, but every host mapping (and therefore every `MapFile` range handed to the platform) is still 16 KiB-aligned, so a 16 KiB `hostarch.PageSize` is the lower-risk route. **S1.**
- The stage-2 (IPA) size and `PhysicalAddressBits` would come from `hv_vm_config_get_max_ipa_size` instead of `KVM_CAP_ARM_VM_IPA_SIZE`. **S1.**
- The MMIO-based hypercall (guest touches the vector page's *physical* address region, `getHypercallID`) depends on an unmapped-GPA exit; HVF reports data aborts at EL2 as `HV_EXIT_REASON_EXCEPTION` with ESR_EL2 describing the abort, so the same trick is likely portable (needs verification). Alternatively `HVC` could be used since HVF traps it to the host. **S1.**
- SError injection for bounce (`KVM_SET_VCPU_EVENTS`) would be replaced by `hv_vcpus_exit`, which forces `hv_vcpu_run` to return `HV_EXIT_REASON_CANCELED`. **S1.**

### Gaps
- Whether HVF allows the guest to run with `SCTLR_EL1.M=1` and its own TTBR before any `hv_vcpu_run` (vs. requiring a reset state) is not determinable from the repo.

---

## Q6. How deep is the `hostarch.PageSize = 4096` assumption?

### Takeaway
`PageSize` is a compile-time constant selected by build tag (`PageShift = 12` for x86 and `arm64 && !pagesize_64k`; `16` for `arm64 && pagesize_64k`); there is no 16 KiB variant. 100 non-test files across `pkg/` and `runsc/` reference `hostarch.PageSize/PageShift/PageRound*/HugePage*`, but almost all do so symbolically; the literal-4096 assumptions are confined to `ring0/aarch64_4k.go`, `kvm/machine_arm64.go:272` (`case 4096`), `systrap/sysmsg/sysmsg.go:225`, and `slimvm`.

### Cited Findings
- Definitions: `PageShift = 12`, `HugePageShift = 21` — [pkg/hostarch/hostarch_arm64_4k.go:24-29](pkg/hostarch/hostarch_arm64_4k.go#L24) (`//go:build arm64 && !pagesize_64k`, line 15); `PageShift = 16`, `HugePageShift = 29` — [hostarch_arm64_64k.go:24-29](pkg/hostarch/hostarch_arm64_64k.go#L24); `PageSize = 1 << PageShift`, `HugePageSize`, `JumboPageSize`, `CacheLineSize` — [hostarch_arm64.go:27-39](pkg/hostarch/hostarch_arm64.go#L27); x86 `PageShift 12, HugePageShift 21, JumboPageShift 30` — [hostarch_x86.go:24-42](pkg/hostarch/hostarch_x86.go#L24).
- Usage spread (files per directory, non-test): `platform/kvm 9, platform/systrap 8, syscalls/linux 7, fsimpl/gofer 7, platform/slimvm 5, fsimpl/cgroupfs 4, fsimpl/tmpfs 3, fsimpl/proc 3, devices/tpuproxy 3, ... mm/{vma,syscalls,procfs,pma,address_space}.go 1 each, pgalloc 2, loader 3, ring0 2, pagetables 2, hostmm 1, kernel/{shm,pipe,kcov,fscheckpoint}` — 100 files total (grep).
- Literal page-size constants outside hostarch: `ring0/aarch64_4k.go:21-28` (`0x1000`), `ring0/aarch64_64k.go:29-35` (`0x10000`), `kvm/machine_arm64.go:272` (`case 4096`), `kvm/kvm_const_arm64.go:73` (`_SCTLR_I = 1<<12`, unrelated), `systrap/sysmsg/sysmsg.go:225` (`AllocatedSizeofThreadContextStruct uintptr = 4096`), `slimvm/bluepill_unsafe.go:165`, `slimvm/pcids_x86.go:27` — grep over `sentry/mm sentry/pgalloc sentry/memmap sentry/platform ring0 hostarch safecopy`.
- The KVM platform hard-fails on mismatch: "KVM platform requires a %d-byte page host, but the host page size is %d bytes" — [kvm.go:81-83](pkg/sentry/platform/kvm/kvm.go#L81). `sentry/time/tsc_offset_unsafe.go:78` uses `unix.Getpagesize()` dynamically for its two-page alias.
- `pagetables/allocator_unsafe.go:26-36` aligns PTE pages to `hostarch.PageSize` from Go heap allocations ("The runtime allocator backs PTEs with plain Go heap pages (new(PTEs), no mlock / MAP_POPULATE / memfile pinning)" — `pagetables.go:336-337`).
- The 64K build already proves the symbolic path works: `.bazelrc:55-56` defines `syscalls-arm64-64k` with `--//tools/bazeldefs:page_size=64k`, and `sysmsg_4k.go`/`sysmsg_64k.go` exist for systrap.

### Inferences
- Adding a 16 KiB variant is mostly mechanical (a `pagesize_16k` tag with `hostarch_arm64_16k.go`, `ring0/aarch64_16k.go`, `pagetables_arm64_16k.go` + walker, and a `tcrTGFlags` case). The 16 KiB granule with 48-bit VA is 4-level with a 2-entry level-0 table (`pteShift 14, pmdShift 25, pudShift 36, pgdShift 47`), which is a new walker shape not covered by either existing walker. **S1.**
- Guest ABI consequences of a 16 KiB `hostarch.PageSize`: `getpagesize()`/`AT_PAGESZ`, ELF segment alignment, `mmap` granularity and `/proc/*/maps` would all report 16 KiB inside the sandbox, which breaks Linux binaries linked with 4 KiB `-z max-page-size` assumptions (the 64K build has the same property). For a static busybox demo this is acceptable; for parity it is a real compatibility difference. **S2.**

### Gaps
- I did not audit the 7 `fsimpl/gofer` and 7 `syscalls/linux` call sites for implicit 4 KiB assumptions beyond the symbolic constant.

---

## Q7. KVM ioeventfd, shadow vDSO / TSC offset, and host-time plumbing

### Takeaway
`ioeventfd` lets the Sentry signal an eventfd from guest mode by writing to a reserved MMIO page (`KVM_IOEVENTFD`), avoiding a VM exit; the shadow vDSO patches the host `[vdso]`'s `cntvct_el0` reads to subtract a TSC offset so that Go runtime code running in guest mode sees consistent time. Both are S2 optimisations with no HVF analog; the underlying time machinery (`Rdtsc` = `MRS CNTVCT_EL0`, `vdsoClockGettime` linked to Go's `runtime.vdsoClockgettimeSym`) is Linux-vDSO specific but has an obvious libc fallback.

### Cited Findings
- `IoeventfdEnable` allocates an 8-byte slot from a `PROT_NONE|MAP_SHARED|MAP_ANONYMOUS` mapping of `maxIoeventfds*8` bytes that is deliberately excluded from guest-physical memory so accesses cause MMIO exits, translates to physical, and issues `KVM_IOEVENTFD{Addr, Len: 8, FD}` — [ioeventfd.go:32-60, 113-165](pkg/sentry/platform/kvm/ioeventfd.go#L32); `Enabled()` returns `inKernelMode()` (`currentEL() == 1`) — lines 98-100; `eventfd.Write` uses `safecopy.CopyOut(ev.mmioPtr(), buf)` when enabled, else `write(2)` — [pkg/eventfd/eventfd.go:79-95](pkg/eventfd/eventfd.go#L79). `eventfd.Create` is `SYS_EVENTFD2` — line 39.
- Shadow vDSO: `initShadowVDSO` (only when `tscOffset != 0`) finds `[vdso]` in `/proc/self/maps`, copies it to an anonymous mapping, patches it (`patchVDSOTSCOffset`), `mprotect(PROT_READ)` — [vdso_unsafe.go:52-106](pkg/sentry/platform/kvm/vdso_unsafe.go#L52); `parseVDSO` validates a little-endian ELF64 with `PT_LOAD` offsets == vaddr — [vdso.go:66-99](pkg/sentry/platform/kvm/vdso.go#L66); arm64 patcher scans `.text` for `MRS CNTVCT_EL0`/`CNTVCTSS_EL0`, writes 24-byte stubs into dead sections (`.altinstructions`) and flushes the I-cache (`flushICache`) — [vdso_arm64_unsafe.go:47-132](pkg/sentry/platform/kvm/vdso_arm64_unsafe.go#L47), [vdso_stub_arm64.go:31-59](pkg/sentry/platform/kvm/vdso_stub_arm64.go#L31).
- TSC offset alias: `ktime.EnableGuestAliasedOffset()` mmaps two pages (`unix.Mmap`, `Mprotect`) so the same VA reads as the offset in host mode and zero in guest mode — [pkg/sentry/time/tsc_offset_unsafe.go:76-90](pkg/sentry/time/tsc_offset_unsafe.go#L76) (design comment lines 24-50); KVM maps the alias in `mapTSCOffsetAlias` — [machine.go:529-545](pkg/sentry/platform/kvm/machine.go#L529). arm64 sets the guest counter with `_KVM_ARM64_REGS_TIMER_CNT` (`setTSC`/`getTSC`) and calibrates in `setSystemTime` — [machine_arm64_unsafe.go:250-320](pkg/sentry/platform/kvm/machine_arm64_unsafe.go#L250); amd64 uses `KVM_SET_MSRS(IA32_TSC)`, `KVM_GET/SET_TSC_KHZ`, `KVM_SET_DEVICE_ATTR(KVM_VCPU_TSC_OFFSET)` — [machine_amd64_unsafe.go:106-168](pkg/sentry/platform/kvm/machine_amd64_unsafe.go#L106).
- Host time: `Rdtsc` is `MRS CNTVCT_EL0` ([pkg/sentry/time/tsc_arm64.s:17-20](pkg/sentry/time/tsc_arm64.s#L17)) / `RDTSC` on amd64; `getCNTFRQ` reads `CNTFRQ_EL0` (line 24); `vdsoClockGettime` is a template-generated asm that calls `runtime·vdsoClockgettimeSym` and manipulates `m.vdsoPC/m.vdsoSP`, falling back to `SYS_clock_gettime (113)` — [vdso_arm64.tmpl.s:19-126](pkg/sentry/time/vdso_arm64.tmpl.s#L19); `HostTSCRealtime` samples `rawHostRdtsc()` around it — [host_tsc.go:64-80](pkg/sentry/time/host_tsc.go#L64). The kernel `Timekeeper` writes `vdsoParams` into a `VDSOParamPage` in the MemoryFile — [pkg/sentry/kernel/timekeeper.go:150-294](pkg/sentry/kernel/timekeeper.go#L150).
- Guest vDSO is a separately built `-nostdlib` ELF `.so` (`vdso.cc`, `vdso_time.cc`, `vdso_arm64.lds`) embedded as `vdsodata.Binary` — [vdso/BUILD:18-58](vdso/BUILD#L18), [pkg/sentry/loader/vdso.go:182-209](pkg/sentry/loader/vdso.go#L182).

### Inferences
- `ioeventfd` has no HVF counterpart (there is no in-kernel eventfd); the fallback path in `eventfd.Write` (plain `write(2)`) already exists, so an HVF platform simply never calls `IoeventfdEnable`. `eventfd` itself needs a macOS substitute (pipe or `kqueue` `EVFILT_USER`). **S2** for the MMIO path; **S1** for `eventfd.Create` if anything on the demo path uses it (hostmm memcg pressure does, but `UseHostMemcgPressure` is disabled by `runsc/boot/loader.go:1267-1271`).
- `CNTVCT_EL0` is readable from EL0 on macOS arm64 (needs verification; `mach_absolute_time` is the libc path), so `Rdtsc` is portable; `vdsoClockGettime` must become a libc `clock_gettime` call (Go darwin `time.now` goes through libSystem). The shadow-vDSO patcher is Linux-vDSO-specific and irrelevant unless a guest-visible TSC offset is needed (checkpoint/restore across hosts). **S2.**
- The guest vDSO build (`vdso/BUILD`) requires a Linux-targeting cross toolchain on macOS or a vendored prebuilt binary. **S1 (build infra).**

### Gaps
- None.

---

## Q8. The `pkg/sentry/platform/kvm/testutil` harness

### Takeaway
`testutil` is 318 Go + 349 asm lines of tiny guest "applications" (functions that execute `SVC`, touch a register-addressed word, spin, halt, twiddle registers, exercise FP/TLS/PAC) addressed via `AddrOf*` helpers; `kvm_test.go` drives them through `kvmTest`/`bluepillTest`/`applicationTest` by opening `/dev/kvm` through `platformdesc`, creating a `KVM`, grabbing a vCPU and either bluepilling into it or mapping all physical regions user-accessible and running the target as EL0 code. The harness is the right skeleton for bringing up an HVF platform because it exercises exactly the host contract (enter guest, syscall exit, fault exit, bounce, invalidate, FP state).

### Cited Findings
- Exported targets: `Getpid`, `AddrOfGetpid`, `AddrOfTouch`/`touch`, `AddrOfSyscallLoop`, `AddrOfSpinLoop`, `AddrOfHaltLoop`, `AddrOfTwiddleRegsFault`, `AddrOfTwiddleRegsSyscall`, `FloatingPointWorks`, `RegisterMismatchError` — [testutil/testutil.go:23-90](pkg/sentry/platform/kvm/testutil/testutil.go#L23); arm64 adds `AddrOfPACLoop`, `LoadPair`, `TouchStackAt`, `SetTestTarget` (sets `Pc`), `SetTouchTarget` (uses `Regs[8]`), `RewindSyscall` (`Pc -= 4`), `SetTestRegs`/`CheckTestRegs` (R0..R30 and `TPIDR_EL0`) — [testutil_arm64.go:35-86](pkg/sentry/platform/kvm/testutil/testutil_arm64.go#L35); the asm bodies use `SVC`, `HLT`, `MSR/MRS TPIDR_EL0` — [testutil_arm64.s:25-202](pkg/sentry/platform/kvm/testutil/testutil_arm64.s#L25).
- Harness: `kvmTestWithConfig` → `platformdesc.Lookup("kvm")`, `desc.OpenDevice("")`, `New(deviceFile, config)`, loop `k.machine.Get()`/`fn(c)`/`Put`, with deferred `redpill()` recovery — [kvm_test.go:54-93](pkg/sentry/platform/kvm/kvm_test.go#L54); `bluepillTest` enters guest mode then calls `fn` — lines 95-101; `applicationTest` creates an `addressSpace`, optionally maps every physical region `User: true` ("This is normally dangerous ... fine for test purposes"), and passes `(c, &regs, pt)` to the test — lines 138-172. Tests cover kernel syscall/fault/FP, application syscall/fault, register round-trips, bounce (incl. stress), invalidate, empty address space, wrong vCPU, rdtsc/TSC offset, vDSO, ioeventfd, plus benchmarks — lines 103-713. The target is `manual`, `nogotsan`, `requires-kvm`, `pure = True` — [kvm/BUILD:128-145](pkg/sentry/platform/kvm/BUILD#L128).

### Inferences
- The guest-side asm (`testutil_arm64.s`) is architecture-only and reusable verbatim for an HVF platform's tests; only the harness functions (`kvmTest*`, which call `New`, `machine.Get`, `bluepill`, `redpill`) need an HVF twin. **S1 (dev tooling).**

### Gaps
- None.

---

## Q9. ptrace and systrap platforms: mechanisms, and why they are not portable to macOS

### Takeaway
Both platforms run application code in a *separate host process* ("stub") whose address space is the application's, and rely on Linux-only tracing primitives: ptrace uses `PTRACE_SYSEMU`/`PTRACE_SYSEMU_SINGLESTEP` to stop at every syscall without executing it; systrap uses seccomp `SECCOMP_RET_TRAP` (SIGSYS delivered into a shared signal stack), `SECCOMP_FILTER_FLAG_NEW_LISTENER`/`SECCOMP_IOCTL_NOTIF_*`, a memfd shared between Sentry and stub, futex-based wake-ups, `PR_SET_SYSCALL_USER_DISPATCH`, and (amd64) binary patching of `syscall` instructions. None of these exist on macOS, so neither platform is a porting candidate; an HVF platform is the only realistic path.

### Cited Findings
- ptrace doc: "The creation of a new address space creates a new child process with a single thread which is traced by a single goroutine ... Locks the runtime thread ... The requested operation is performed in the traced subprocess thread (e.g. set registers, execute, return)" — [pkg/sentry/platform/ptrace/ptrace.go:15-45](pkg/sentry/platform/ptrace/ptrace.go#L15). Mechanism counts (grep): `SYS_PTRACE 17`, `PTRACE_SYSEMU 5`, `PTRACE_SYSEMU_SINGLESTEP 1`, `PTRACE_GETREGSET/SETREGSET 3/3`, `PTRACE_ATTACH 2`, `PTRACE_O_TRACESYSGOOD|TRACEEXIT|EXITKILL`, `PTRACE_GETSIGINFO`, `PTRACE_GETEVENTMSG`, `CLONE_PTRACE 3`, `CLONE_FILES 3`, `CLONE_VM/THREAD/SIGHAND/FS`, `SYS_WAIT4 2`, `PR_SET_PDEATHSIG 4`, `SIGSTOP 32`, `ARCH_SET_CPUID`. Key lines: `attachedThread(SIGKILL|CLONE_FILES)` — [subprocess_linux.go:68](pkg/sentry/platform/ptrace/subprocess_linux.go#L68); `PTRACE_SYSEMU` loop and "SIGSTOP was delivered to another thread" handling — [subprocess.go:555-580](pkg/sentry/platform/ptrace/subprocess.go#L555); `PTRACE_O_EXITKILL` option set — lines 429-437; `PTRACE_DETACH` with SIGSTOP — line 327; `Wait4(WALL|WUNTRACED)` — line 375; arm64 note "the support of PTRACE_SYSEMU was added in the 5.3 kernel" — [subprocess_arm64.go:183](pkg/sentry/platform/ptrace/subprocess_arm64.go#L183). Requires `CAP_SYS_PTRACE` — `platformdesc.go:57`. `g3doc` says ptrace "is no longer supported and is expected to eventually be removed entirely" — [g3doc/architecture_guide/platforms.md:73-76](g3doc/architecture_guide/platforms.md#L73).
- systrap doc: "install a signal stack which is shared with the Sentry; install a signal handler for SYS, BUS, FPE, CHLD, TRAP, SEGV signals ... install seccomp filters to trap user system calls ... Wake up a stub thread by changing sysmsg->stage and calling FUTEX_WAKE" — [pkg/sentry/platform/systrap/systrap.go:15-42](pkg/sentry/platform/systrap/systrap.go#L15); README: "Linux allows setting seccomp filters with SECCOMP_RET_TRAP, such that when a thread tries to call a system call caught by the seccomp filter, this thread will receive the SIGSYS signal" — [systrap/README.md:6-8](pkg/sentry/platform/systrap/README.md#L6). Mechanism counts (grep over systrap, sysmsg C, usertrap): `SIGSTOP 39`, `SYS_PTRACE 18`, `FUTEX_WAKE 20`, `FUTEX_WAIT 16`, `SYS_MMAP 15`, `SIGSYS 11`, `MAP_SHARED 10`, `SYS_PRCTL 9`, `PR_SET_PDEATHSIG 7`, `PTRACE_SETREGSET 5`, `PTRACE_ATTACH 4`, `SYS_SECCOMP 3`, `SECCOMP_SET_MODE_FILTER 3`, `SECCOMP_FILTER_FLAG_NEW_LISTENER 3`, `SECCOMP_USER_NOTIF_FD_SYNC_WAKE_UP 3`, `SECCOMP_IOCTL_NOTIF_RECV/SEND/SET_FLAGS`, `PTRACE_SYSEMU 3`, `PTRACE_SETSIGMASK 3`, `PR_SET_SYSCALL_USER_DISPATCH 2`, `PR_SET_NO_NEW_PRIVS 2`, `SECCOMP_RET_TRAP 1`, `sigaltstack 2`, `SA_ONSTACK|SA_RESTORER|SA_SIGINFO`, `ARCH_SET_FS/GS`, `memfd 1`, `/proc/self/maps 3`.
- systrap specifics: `initSeccompNotify` probes `seccomp(SECCOMP_SET_MODE_FILTER, SECCOMP_FILTER_FLAG_NEW_LISTENER)` ("available since Linux 5.0") — [subprocess.go:193-205](pkg/sentry/platform/systrap/subprocess.go#L193); stubs are created with `clone(CLONE_FILES|CLONE_PARENT|SIGCHLD)` and `PTRACE_ATTACH` ("PTRACE_ATTACH sends SIGSTOP") — [subprocess_linux.go:44-183](pkg/sentry/platform/systrap/subprocess_linux.go#L44), [subprocess.go:581-585](pkg/sentry/platform/systrap/subprocess.go#L581); shared memory via `memutil.CreateMemFD` and `mmap(MAP_SHARED|MAP_FILE|MAP_FIXED)` into the stub — [systrap.go:428-430](pkg/sentry/platform/systrap/systrap.go#L428), [subprocess.go:455-486, 1091, 1206](pkg/sentry/platform/systrap/subprocess.go#L455); the stub's `sigaltstack`/`sigaction(SA_ONSTACK|SA_RESTORER|SA_SIGINFO, mask SIGCHLD|SIGSYS)` — [sysmsg_thread_unsafe.go:60-82](pkg/sentry/platform/systrap/sysmsg_thread_unsafe.go#L60); the C signal handler reads `ucontext->uc_mcontext.regs/sp/pc/pstate` and the `fpsimd_context` in `__reserved` — [sysmsg/sighandler_arm64.c:76-139](pkg/sentry/platform/systrap/sysmsg/sighandler_arm64.c#L76); `/proc/self/maps` parsing for stub placement — [stub_unsafe.go:112-115](pkg/sentry/platform/systrap/stub_unsafe.go#L112). `usertrap` rewrites `mov sysno,%eax; syscall` into `jmp *%ds:offset` trampolines using `%gs` per-thread memory (amd64 only) — [usertrap/usertrap.go:15-40](pkg/sentry/platform/systrap/usertrap/usertrap.go#L15). Requires `CAP_SYS_PTRACE`, `FrequentHostThreadWakeups` — `platformdesc.go:61-64`.
- Neither platform appears in any `darwin` select; `platforms.go` is `linux && !debug` only.

### Inferences
- macOS has `ptrace(2)` but without `PT_SYSEMU`-style syscall-entry stops for arbitrary tracees, no seccomp at all (the Seatbelt sandbox cannot deliver SIGSYS-style traps to a supervisor), no `process_vm_readv` (its analog `mach_vm_read_overwrite` exists but gVisor does not use `process_vm_*` anyway: grep found no hits), no `memfd_create`, no futexes (private `__ulock_wait`, public `os_sync_wait_on_address` since macOS 14.4), and no `/proc`. The stub-process architecture would need Mach exception ports plus `thread_set_state` — a complete re-implementation with much worse syscall latency than even ptrace. **Not a porting path; document and exclude.**

### Gaps
- None.

---

## Q10. What does `pgalloc` (and `memutil`, `usage`, `hostmm`) need from the host, and what are the macOS equivalents?

### Takeaway
The MemoryFile is a `memfd_create` file that the Sentry `ftruncate`s/`fallocate`s, `mmap(MAP_SHARED)`s in chunks, decommits with `fallocate(PUNCH_HOLE|KEEP_SIZE)`, advises with `madvise(MADV_HUGEPAGE/NOHUGEPAGE/POPULATE_WRITE)`, probes with `mlock/munlock` and accounts with `mincore(2)` plus `fstat`. The S1 subset (create, truncate, mmap shared, zero on free) maps onto `shm_open`+`ftruncate`+`mmap`; the S2 subset (hole punching, THP, memcg pressure via cgroup v1 `event_control`, membarrier, `SEEK_DATA/SEEK_HOLE` for save/restore) has partial or no macOS analogs.

### Cited Findings
- Creation: `memutil.CreateMemFD(name, flags)` = `SYS_MEMFD_CREATE` ("Check that you have Linux 3.17 or higher") — [pkg/memutil/memfd_linux_unsafe.go:28-41](pkg/memutil/memfd_linux_unsafe.go#L28); callers: `runsc/boot/loader.go:1261` (`"runsc-memory"`), `pkg/sentry/usage/memory.go:270` (`"memory-usage"`), `systrap.go:428`, `stateio_util.go:31`, `flipcall/packet_window.go:81` (`MFD_CLOEXEC|MFD_ALLOW_SEALING`), test utilities. `memutil.MapFile`/`MapSlice`/`UnmapSlice` are raw `SYS_MMAP`/`SYS_MUNMAP` — [memutil/mmap.go:26-27](pkg/memutil/mmap.go#L26), [memutil_unsafe.go:25-37](pkg/memutil/memutil_unsafe.go#L25).
- `NewMemoryFile` truncates to 0, optionally registers `hostmm.NotifyCurrentMemcgPressureCallback(..., "low")`, starts `releaserMain`, and calls `hostmm.IMAWorkAroundForMemFile` (mmap `PROT_EXEC|MAP_SHARED` once to defeat IMA) unless `DisableIMAWorkAround` — [pkg/sentry/pgalloc/pgalloc.go:430-472](pkg/sentry/pgalloc/pgalloc.go#L430); `MemoryFileOpts` fields: `DelayedEviction, UseHostMemcgPressure, DecommitOnDestroy, DisableIMAWorkAround, DiskBackedFile, ResourceID, ExpectHugepages, AdviseHugepage, AdviseNoHugepage, DisableMemoryAccounting` — same file (struct definition).
- Chunk mapping: `SYS_MMAP(PROT_READ|PROT_WRITE, MAP_SHARED, fd, offset)` — [pgalloc.go:944-949](pkg/sentry/pgalloc/pgalloc.go#L944); unmap on destroy — line 522; `madvise(MADV_HUGEPAGE)` / `MADV_NOHUGEPAGE` — lines 984-995; `madvise(MADV_POPULATE_WRITE)` with `EINVAL` fallback — lines 1018-1020; `mlock`/`munlock` probe tolerating `ENOMEM|EPERM` — lines 1050-1053.
- Commit/decommit: `commitFile` = `unix.Fallocate(fd, 0, start, len)`; `decommitFile` = `Fallocate(FALLOC_FL_PUNCH_HOLE|FALLOC_FL_KEEP_SIZE)` ("subsequent reads from this range will return zeroes") — [pgalloc.go:1122-1141](pkg/sentry/pgalloc/pgalloc.go#L1122); `manuallyZero` fallback exists — line 1143.
- Accounting: `mincore(s, buf)` via `SYS_MINCORE` — [pgalloc_unsafe.go:29-31](pkg/sentry/pgalloc/pgalloc_unsafe.go#L29), driven from `updateUsageLocked` ("mincore might take a really long time. So unlock f.mu while mincore runs") — [pgalloc.go:1735-1765](pkg/sentry/pgalloc/pgalloc.go#L1735); `unix.Fstat` for file size — line 1842. Save/restore uses `Fstatfs`, `Seek(SEEK_DATA/SEEK_HOLE)`, iovecs and another `MAP_SHARED` mmap — [save_restore.go:606-633, 1182-1187](pkg/sentry/pgalloc/save_restore.go#L606).
- `usage.Init` creates a memfd, truncates to `RTMemoryStatsSize`, `MapFile(MAP_SHARED)` and casts to `RTMemoryStats` ("We rely on the returned page being initially zeroed") — [pkg/sentry/usage/memory.go:266-299](pkg/sentry/usage/memory.go#L266); called from [runsc/boot/loader.go:674](runsc/boot/loader.go#L674).
- `hostmm`: THP enums from `/sys/kernel/mm/transparent_hugepage/{enabled,shmem_enabled,defrag}` — [hostmm.go:33-47](pkg/sentry/hostmm/hostmm.go#L33); memcg pressure via `/proc/self/mounts` → cgroup v1 `cgroup.event_control` + eventfd — [hostmm.go:58-108](pkg/sentry/hostmm/hostmm.go#L58), [cgroup.go:27-83](pkg/sentry/hostmm/cgroup.go#L27); `membarrier(2)` — [membarrier.go:55-112](pkg/sentry/hostmm/membarrier.go#L55). `runsc/boot/loader.go:1267-1271` leaves `UseHostMemcgPressure` off ("we're already in a mount namespace in which the relevant cgroupfs is not visible"); THP handling is gated on `appHugePages` — lines 1272-1300.
- `memmap` has no host syscalls (grep for `unix.|syscall.` in `pkg/sentry/memmap` returned nothing).

### Inferences
- **S1 mapping (macOS):** `memfd_create` → `shm_open` (POSIX shm, size via `ftruncate`) or an unlinked temp file; `mmap(MAP_SHARED)` works identically; `commitFile`/`decommitFile` → no-op commit and `madvise(MADV_FREE_REUSABLE)`-or-manual-zero decommit (the `manuallyZero` fallback already exists at `pgalloc.go:1143`); `mincore` exists on macOS (BSD) but reports residency of the *mapping*, which is semantically what gVisor wants; `mlock/munlock` exist. `usage.Init` can use the same shm file. `IMAWorkAroundForMemFile` is Linux-IMA-specific and should be disabled (`DisableIMAWorkAround`).
- **S2:** `fcntl(F_PUNCHHOLE)` on APFS could implement true hole punching (needs verification of availability on shm/tmp files); THP/`MADV_HUGEPAGE` has no equivalent (Apple Silicon host pages are already 16 KiB); memcg pressure and `membarrier` have none; `SEEK_DATA/SEEK_HOLE` exist on APFS for save/restore of sparse images (needs verification).
- Because `hostarch.PageSize` would be 16 KiB, every `FileRange` and chunk is 16 KiB-granular, which aligns with the host and avoids partial-page `mmap` issues. **S1.**

### Gaps
- Whether macOS POSIX shm objects support `fcntl(F_PUNCHHOLE)` or only regular APFS files is not determinable here.

---

## Q11. Host signal usage summary and signal-frame layout assumptions (cross-reference)

### Takeaway
Three code paths install raw Linux signal handlers: KVM bluepill (SIGILL on arm64, SIGSYS for mmap trapping, synthetic frames on amd64), `safecopy` (SIGSEGV/SIGBUS), and slimvm. Everything else goes through Go's `os/signal` (`sighandling.StartSignalForwarding`). The only Linux-frame-layout consumers are the bluepill/sigframe files and `safecopy`'s asm handler; `arch.UContext64`/`SignalContext64` are otherwise used purely as the *guest* ABI.

### Cited Findings
- Covered in Q4; additionally: the Go-level forwarder uses `signal.Notify` for all signals except `SIGURG`, `SIGPIPE`, `SIGCHLD` — [pkg/sighandling/sighandling.go:73-104](pkg/sighandling/sighandling.go#L73); the loader forwards signals to the sandboxed init/console — [runsc/boot/loader.go:1508-1535](runsc/boot/loader.go#L1508).
- `platform.SignalInterrupt = linux.SIGCHLD` is used by the kernel to detect misfired interrupts — [platform.go:371](pkg/sentry/platform/platform.go#L371), [task_run.go:373](pkg/sentry/kernel/task_run.go#L373).
- `safecopy` arm64 handler compares `si_signo` against literal 7/11 — [sighandler_arm64.s:18-19, 128](pkg/safecopy/sighandler_arm64.s#L18); Darwin numbers for SIGBUS (10) and SIGSEGV (11) differ for SIGBUS, so even the constants are host-specific.

### Inferences
- A port needs: (1) `sighandling_darwin.go` with a real `sigaction`-based `ReplaceSignalHandler`; (2) a Darwin `safecopy` handler using Darwin `ucontext`; (3) no bluepill signals at all if HVF is used. **S1 for (1) and (2).**

### Gaps
- None beyond Q4.

---

## Q12. Go runtime interactions: `LockOSThread`, `procid`/`hosttid`, `goid`, linknames, raw syscalls

### Takeaway
The Sentry relies on a handful of Go-runtime internals that are *OS-independent in shape* (`g.m.procid`, `g.goid`, `runtime.gopark/goready/wakep/fastrand`, `entersyscallblock/exitsyscall`) and on raw `SVC`/`SYSCALL` instructions in `pkg/hostsyscall` plus `golang.org/x/sys/unix` everywhere. On darwin, Go routes syscalls through libSystem and `m.procid` has different semantics, so `hostsyscall` (Linux ABI: syscall number in x8, negative errno return) must be replaced, and the vCPU-per-host-thread model keyed on `hosttid.Current()` needs a Darwin thread id.

### Cited Findings
- `hosttid.Current()` reads `getg().m.procid` via template-generated asm (`M_OFFSET`, `PROCID_OFFSET` from `runtime`) — [pkg/hosttid/hosttid_arm64.tmpl.s:1-10](pkg/hosttid/hosttid_arm64.tmpl.s#L1); `goid.Get()` reads `g.goid` — [pkg/goid/goid_arm64.tmpl.s](pkg/goid/goid_arm64.tmpl.s). KVM's `machine.Get()` does `runtime.LockOSThread()` then `hosttid.Current()` to look up `vCPUsByTID`, steals idle vCPUs otherwise, and `Put` unlocks — [machine.go:595-700](pkg/sentry/platform/kvm/machine.go#L595); `LockOSThread` sites in the Sentry proper: `kvm/machine.go`, `ptrace/subprocess.go`, `systrap/subprocess.go`, `slimvm/machine.go`, `seccomp/seccomp_unsafe.go`, `hosttid/hosttid.go`, `coretag/coretag.go`, `socket/plugin/stack/notifier.go` (grep).
- `pkg/hostsyscall` provides `RawSyscall6/RawSyscall/RawSyscallErrno6/RawSyscallErrno` implemented with `SVC` (arm64) / `SYSCALL` (amd64) — [pkg/hostsyscall/hostsyscall.go:31-45](pkg/hostsyscall/hostsyscall.go#L31), [hostsyscall_arm64.s:18-89](pkg/hostsyscall/hostsyscall_arm64.s#L18); it is `//go:build linux` (line 15). It is used throughout KVM's nosplit paths (e.g. `bluepill_unsafe.go:174`, `machine_unsafe.go:59`, `physical_map.go:134`).
- `go:linkname` into the runtime (12 non-test files): `sync/runtime_unsafe.go` (`gopark`, `wakep`, `goready`, `fastrand`, `sync.runtime_Semacquire/Semrelease/canSpin/doSpin`), `sync/goyield_unsafe.go` (`runtime.goyield`), `rawfile/blockingpoll_yield_unsafe.go` (`entersyscallblock`, `exitsyscall`), `kvm/bluepill_unsafe.go` and `machine_unsafe.go` (`runtime.throw`, plus `exitsyscall` used at `machine_arm64_unsafe.go:433`), `sigframe`, `ptrace/systrap/slimvm subprocess_unsafe.go`, `gohacks`, `coverage` (grep). `pkg/sleep` uses `sync.Gopark`-style parking (`sleep_unsafe.go:172-255`).
- `syscall.AllThreadsSyscall6` is used for `PR_PAC_SET_ENABLED_KEYS` — [machine_arm64_unsafe.go:367](pkg/sentry/platform/kvm/machine_arm64_unsafe.go#L367).
- `pkg/seccomp` installs filters with `prctl(PR_SET_NO_NEW_PRIVS)` / `seccomp(2)` and uses `runtime.LockOSThread` + `runtime.syscall` linkage — [pkg/seccomp/seccomp_unsafe.go:37-78](pkg/seccomp/seccomp_unsafe.go#L37) (grep lines 37, 39, 63, 66, 78); `runsc/boot/filter/config` (1,517 lines) defines the sandbox allow-list; `loader.installSeccompFilters` calls `filter.Install` — [runsc/boot/loader.go:1323-1363](runsc/boot/loader.go#L1323).
- `gomaxprocs` manipulates `runtime.GOMAXPROCS` (portable) — grep.

### Inferences
- The runtime linknames (`gopark`, `goready`, `wakep`, `fastrand`, `g.m.procid`, `g.goid`) are platform-independent Go internals and compile on darwin unchanged (subject to the same Go-version churn as today). **Not a blocker.**
- `hosttid.Current()` on darwin returns whatever the darwin runtime stores in `m.procid` (the runtime sets it from the pthread/thread port, not a kernel TID; needs verification). It is only used as a stable per-OS-thread key in KVM, so any unique per-thread value suffices for an HVF platform, but `Tgkill(pid, tid)` bounce (`machine.go:822`) must become `hv_vcpus_exit`. **S1.**
- `pkg/hostsyscall` must be either (a) given a darwin implementation that calls libSystem (`syscall.rawSyscall`/`libcCall` are not exported; cgo or Go's internal `//go:cgo_import_dynamic` trampolines as used by `x/sys/unix` on darwin would be needed), or (b) avoided by making every former nosplit raw-syscall site a normal Go/libc call. Since the HVF design removes the in-signal-handler constraint, (b) is feasible. Direct `SVC #0x80` into XNU is possible but uses a different ABI (x16 = number, carry flag for error) and is unsupported by Apple. **S1.**
- `seccomp` is irrelevant on macOS (no equivalent); `runsc/boot/filter` would be skipped, which removes a defence-in-depth layer (security posture note for the report). **S1 (simply disabled), S2 (replacement sandboxing, e.g. Seatbelt profile).**

### Gaps
- Exact darwin semantics of `m.procid` were not verified from Go sources (not in this repo).

---

## Q13. `cpuid`, `hostcpu`, `hostos`: what they read from the host

### Takeaway
arm64 `cpuid` learns host features from `/proc/self/auxv` (`AT_HWCAP`/`AT_HWCAP2`) and tolerates failure with a warning; `hostcpu.GetCPU` is a raw `getcpu(2)` `SVC` and `MaxPossibleCPU` reads `/sys/devices/system/cpu/possible`; `hostos.KernelVersion` is `uname(2)` and it also reads `/proc/meminfo`. amd64 `cpuid` executes the `CPUID` instruction (portable) and KVM uses `KVM_GET_SUPPORTED_CPUID`/`KVM_SET_CPUID2` plus MSRs for CPUID faulting.

### Cited Findings
- `readHWCap("/proc/self/auxv")` parses 16-byte auxv entries for `_AT_HWCAP = 16`, `_AT_HWCAP2 = 26`; `initHWCap` logs "cpuid HWCap not initialized: %v" on error — [pkg/cpuid/cpuid.go:49-51, 302-335](pkg/cpuid/cpuid.go#L302); arm64 `HostFeatureSet`, `Fixed`, `Intersect`, `initCPUInfo` — [cpuid/native_arm64.go:36-56](pkg/cpuid/native_arm64.go#L36); `AllowedHWCap1/2` enumerate guest-visible HWCAP bits and emit `/proc/cpuinfo` text — [cpuid_arm64.go:116-160](pkg/cpuid/cpuid_arm64.go#L116).
- `hostcpu.GetCPU` arm64 asm: `MOVD $0xA8, R8 // SYS_GETCPU; SVC` — [pkg/sentry/hostcpu/getcpu_arm64.s:17-29](pkg/sentry/hostcpu/getcpu_arm64.s#L17); `MaxPossibleCPU` reads `/sys/devices/system/cpu/possible` — [hostcpu.go:34-35](pkg/sentry/hostcpu/hostcpu.go#L34). `pkg/sentry/kernel` depends on `hostcpu` ([kernel/BUILD:390](pkg/sentry/kernel/BUILD#L390)).
- `hostos.KernelVersion` uses `unix.Uname`; the package also reads `/proc/meminfo` — grep ([pkg/hostos](pkg/hostos)).
- amd64 KVM: `KVM_GET_SUPPORTED_CPUID` into `cpuidEntries` (`_KVM_NR_CPUID_ENTRIES 0x100`), masks VMX/SVM, `ring0.Init(featureSet)` — [kvm_amd64.go:224-250](pkg/sentry/platform/kvm/kvm_amd64.go#L224), [kvm_amd64_unsafe.go:34-63](pkg/sentry/platform/kvm/kvm_amd64_unsafe.go#L34); `setCPUID` (`KVM_SET_CPUID2`), `setPAT` (`KVM_SET_MSRS IA32_PAT`), `enableCPUIDFaulting` (`MSR_MISC_FEATURES_ENABLE`), `KVM_SET_TSS_ADDR` at `reservedMemory-3 pages` — [machine_amd64_unsafe.go:64-186](pkg/sentry/platform/kvm/machine_amd64_unsafe.go#L64), [machine_amd64.go:40-48, 171-178](pkg/sentry/platform/kvm/machine_amd64.go#L40).

### Inferences
- On macOS arm64, HWCAP would come from `sysctl hw.optional.arm.FEAT_*` (or by reading `ID_AA64*` registers inside the guest via HVF, which exposes them through `hv_vcpu_get_sys_reg`); the `/proc/cpuinfo` emitted to the guest is Sentry-synthesised and unaffected. `GetCPU` would use `hv_vcpu`'s index or `pthread`/`cpu_number()`; `MaxPossibleCPU` → `sysctl hw.ncpu`. All **S1 but small** (the code already degrades gracefully when `/proc/self/auxv` is absent).
- Intel macOS: `CPUID` executes natively; HVF x86 exposes `hv_vmx_*` and `hv_vcpu_read/write_msr`, so CPUID faulting (`MSR_MISC_FEATURES_ENABLE`) may or may not be virtualised. **S2 (Intel).**

### Gaps
- None.

---

## Q14. Host-FD utility packages: `fdnotifier` (epoll), `eventfd`, `unet`, `fd`, `rawfile`, `aio`, `flipcall`, `fdchannel`, `rand`, `abi.Host`

### Takeaway
These are small, each with a clear BSD/macOS analog except `aio` (Linux AIO) and `flipcall` (futex). `fdnotifier` is `epoll_create1/epoll_ctl/epoll_pwait` + `ppoll`; `eventfd` is `eventfd2`; `unet`/`fdchannel` use `AF_UNIX` sockets, `ppoll`, `SCM_RIGHTS`; `rawfile` uses `readv/recvmmsg/ppoll/ioctl(SIOCGIFMTU)`; `rand` already has a `!linux` fallback.

### Cited Findings
- `fdnotifier`: `unix.EpollCreate1`, `EpollCtl(ADD/MOD/DEL)` with `EPOLLET`, `SYS_EPOLL_PWAIT`, `SYS_PPOLL` for `NonBlockingPoll` — [pkg/fdnotifier/fdnotifier.go:70-102](pkg/fdnotifier/fdnotifier.go#L70), [poll_unsafe.go:29-77](pkg/fdnotifier/poll_unsafe.go#L29); `pkg/sentry/kernel` depends on it ([kernel/BUILD:363](pkg/sentry/kernel/BUILD#L363)).
- `eventfd`: `SYS_EVENTFD2`, `SetNonblock`, raw `SYS_WRITE` — [pkg/eventfd/eventfd.go:38-44](pkg/eventfd/eventfd.go#L38), [eventfd_unsafe.go:29-35](pkg/eventfd/eventfd_unsafe.go#L29).
- `unet`: `Socket(AF_UNIX, SOCK_CLOEXEC)`, `Accept`, `Listen`, `Msghdr/Iovec`, `SYS_PPOLL` — grep over `pkg/unet`. `fdchannel`: `Socketpair(AF_UNIX, SOCK_SEQPACKET|SOCK_CLOEXEC)`, `SCM_RIGHTS`, `SYS_SENDMSG` — grep over `pkg/fdchannel`. `fd`: `Open/Openat(O_LARGEFILE)`, `Pread`, `Dup`, `SetFinalizer` — grep. `sentry/hostfd`: `pread64/preadv2/pwrite64/pwritev2` — grep. `rawfile`: `SYS_READV`, `SYS_RECVMMSG(MSG_DONTWAIT)`, `SYS_PPOLL`, `ioctl(SIOCGIFMTU)`, and `entersyscallblock/exitsyscall` linknames — grep. `aio`: `io_setup/io_destroy/io_getevents`, `pread64/preadv2/pwrite64/pwritev2` — grep. `flipcall`: `SYS_FUTEX`, `SYS_FCNTL`, `sched_yield`, `mmap(MAP_SHARED)` — grep.
- `rand/rand.go` is `//go:build !linux` with a `Read` fallback — [pkg/rand/rand.go:15-26](pkg/rand/rand.go#L15); Linux uses `getrandom`/`/dev/urandom` ([rand_linux.go:79-85](pkg/rand/rand_linux.go#L79)). `abi.Host = Linux` is defined only under `linux` — [pkg/abi/abi_linux.go:15-19](pkg/abi/abi_linux.go#L15).

### Inferences
- `fdnotifier` → `kqueue`/`kevent` with `EV_CLEAR` for edge-trigger semantics; `eventfd` → pipe pair or `EVFILT_USER`; `preadv2/pwritev2` → `pread/pwrite` (losing `RWF_NOWAIT`); `recvmmsg` → `recvmsg` loop; `flipcall` futexes → `os_sync_wait_on_address` (macOS 14.4+) — flipcall is only used for gofer RPC fast paths (**S2**). `abi.Host` needs an `abi_darwin.go` (a one-liner). `fdnotifier`/`eventfd`/`fd` are **S1** because the kernel package links them and host stdout/stderr go through `fsimpl/host` (1,879 lines) which uses `fdnotifier`.

### Gaps
- None.

---

## Q15. Minimal demo: how `dmesg`/`uname` work, and which subsystems must be alive for static busybox `echo hello` / `dmesg`

### Takeaway
`dmesg` is entirely synthetic: `syslog(SYSLOG_ACTION_READ_ALL)` returns a randomly selected easter-egg log from `kernel.syslog.Log()`; `uname` comes from the `UTSNamespace`. Neither touches the host. The demo therefore needs only: a platform, a MemoryFile, `usage.Init`, `rand`, a Timekeeper (host clock + counter), `cpuid` feature set, `kernel.Init` with namespaces, the ELF loader + embedded guest vDSO, `mm`, VFS with tmpfs (and optionally `proc`), a host-FD-backed stdout, and the syscall table — but *not* the gofer, netstack, seccomp, cgroups, or hostinet. The current `runsc/boot` loader is not reusable as-is because it hard-requires gofer FDs, seccomp, and Linux-only host setup.

### Cited Findings
- `syslog.Log()` builds `"<6>[%11.6f] %s\n"` lines from a fixed list ("Synthesizing system calls...", ..., "Rewriting the kernel in Rust...", "Politicking the oom killer...") plus "Starting gVisor..." / "Ready!" — [pkg/sentry/kernel/syslog.go:38-122](pkg/sentry/kernel/syslog.go#L38); `sys_syslog.go` implements `_SYSLOG_ACTION_READ_ALL = 3` and `_SYSLOG_ACTION_SIZE_BUFFER = 10` via `t.Kernel().Syslog().Log()` — [pkg/sentry/syscalls/linux/sys_syslog.go:24-56](pkg/sentry/syscalls/linux/sys_syslog.go#L24). No `/proc/kmsg` or `/dev/kmsg` exists (grep in `fsimpl/proc` for `kmsg|Syslog` returned nothing), so busybox `dmesg` must use the syscall path (it does by default).
- `kernel.InitKernelArgs`: `FeatureSet, Timekeeper, RootUserNamespace, RootNetworkNamespace, ApplicationCores, UseHostCores, ExtraAuxv, Vdso, VdsoParams, RootUTSNamespace, RootIPCNamespace, RootPIDNamespace, MaxFDLimit, Cgroup2FSInit, SignalUnkillable, DumpGoroutinesSignal` — [pkg/sentry/kernel/kernel.go (type InitKernelArgs)](pkg/sentry/kernel/kernel.go); `UTSNamespace` is built from the OCI spec hostname — [runsc/boot/loader.go:947](runsc/boot/loader.go#L947).
- Loader boot order (host-touching steps in bold): `rand.Init` (669), **`usage.Init`** (674), `runtime.NumCPU` (729), **`createPlatform`** (825; `platform.Lookup(conf.Platform)` and `p.New(platform.Options{...})` at 1223-1250), **`createMemoryFile`** (844; memfd + THP sysfs), `loader.PrepareVDSO` (854), `kernel.NewTimekeeper` / `NewVDSOParamPage` (861-862), `cpuid.HostFeatureSet()` (898), `kernel.Init` (939), `registerFilesystems` (gofer FDs, 788-792), `watchdog.New` (978), `getrlimit` (980-989, "needs getrlimit(2), which the seccomp..."), **`sighandling.IgnoreChildStop`** (1028), **`installSeccompFilters`** (1323-1363), `StartSignalForwarding` (1508) — [runsc/boot/loader.go](runsc/boot/loader.go). The loader struct carries `goferFDs`, `devGoferFD`, `goferFilestoreFDs` (lines 132-145) and a `host.TTYFileDescription` (433).
- `pkg/sentry/kernel` imports from host-ish packages: `cpuid, fdnotifier, hostarch, sentry/hostcpu, sentry/pgalloc, sentry/platform, sentry/time` — [pkg/sentry/kernel/BUILD:356-413](pkg/sentry/kernel/BUILD#L356). The kernel package itself makes no host syscalls other than errno constants (`fd_table.go`, `task_syscall.go`, `seccomp.go` only reference `unix.E*`).
- The guest vDSO is embedded (`vdsodata.Binary`) and validated/copied into the MemoryFile at `PrepareVDSO` — [pkg/sentry/loader/vdso.go:182-209](pkg/sentry/loader/vdso.go#L182); it is built by `vdso/BUILD` with `-nostdlib -Wl,-soname=linux-vdso.so.1` and an arch `.lds` — [vdso/BUILD:44-58](vdso/BUILD#L44).

### Inferences
- **S1 subsystem list** (all guest-side packages are portable Go): `kernel` (26k), `mm` (7.2k), `pgalloc` (4.4k, with a darwin backend), `loader` (1.7k) + `vdsodata`, `arch`/`fpu`, `syscalls/linux` (16.9k; only `write`, `exit_group`, `syslog`, `brk/mmap`, `uname`, `ioctl(TCGETS)` matter for busybox but the whole table links), `vfs` (12.8k) + `tmpfs` (5k) + `fsimpl/host` (1.9k, stdout) + optionally `proc` (6.4k), `sentry/time`, `cpuid`, `hostcpu`, `usage`, `rand`, `fdnotifier`, `eventfd`, `fd`, `safecopy`, `sighandling`, `hostarch` (16K variant), `ring0` + `pagetables` (16K variant), and a new `platform/hvf` of roughly the size of `kvm` minus bluepill asm (~6–8k lines). The rootfs can be a tmpfs populated from a tar by a custom darwin loader, avoiding `lisafs`/`fsgofer` (`runsc/fsgofer` is Linux-only: not analysed here, out of scope).
- **S2** adds: gofer/lisafs (7.4k + fsgofer), netstack (92k, portable Go, but `tcpip/link/*` host drivers are Linux-tagged) or hostinet (2.8k, Linux sockets), checkpoint/restore (`pgalloc/save_restore.go` 2.3k, `state`), seccomp/sandboxing, cgroups, nvproxy/tpuproxy, systrap-specific fast paths, membarrier/ioeventfd optimisations.

### Gaps
- The exact minimal syscall set busybox `echo`/`dmesg` issue was not traced (no strace run here); the Sentry's full table links regardless.

---

## Q16. Consolidated inventory of Linux-host assumptions, with macOS mapping and stage

### Takeaway
About 20 distinct host mechanisms are involved. Nine are S1 blockers for an Apple Silicon demo (KVM ioctls → HVF; signal-frame bluepill → function-call bluepill; `/proc/self/maps` → `mach_vm_region`; `memfd/fallocate` → `shm_open`/madvise; raw `SVC` syscalls → libSystem; `rt_sigaction`-based `safecopy`/`sighandling`; `epoll`/`eventfd` → `kqueue`; 4 KiB page size → 16 KiB build; `/proc`/`/sys` probes → `sysctl`). The rest are S2.

### Cited Findings (table; each row cites the defining location)

| # | Mechanism (Linux) | Where | macOS mapping (inference) | Stage |
|---|---|---|---|---|
| 1 | `/dev/kvm` ioctls (`KVM_CREATE_VM/VCPU`, `KVM_RUN`, `KVM_SET_USER_MEMORY_REGION`, `KVM_SET_ONE_REG`, `KVM_ARM_VCPU_INIT`, `KVM_SET_VCPU_EVENTS`, `KVM_SET_SIGNAL_MASK`, caps) | `kvm_const*.go`, `machine*.go`, `bluepill*_unsafe.go` | Hypervisor.framework `hv_vm_create/hv_vm_map/hv_vcpu_create/hv_vcpu_run/hv_vcpu_set_reg/hv_vcpu_set_sys_reg/hv_vcpus_exit` | S1 |
| 2 | Signal-frame bluepill (SIGILL entry, ucontext register copy, `rt_sigreturn`, `rt_sigtimedwait`, `tgkill(SIGCHLD)` bounce) | `bluepill_arm64.s:51-96`, `bluepill_unsafe.go:159-287`, `machine.go:785-855` | Function-call `hv_vcpu_run` loop + user-space register restore; `hv_vcpus_exit` for bounce | S1 |
| 3 | `rt_sigaction` raw handler install (`ReplaceSignalHandler`, `IgnoreChildStop`) | `sighandling_linux_unsafe.go:31-77` | darwin `sigaction` (`sighandling_darwin.go` currently returns errors) | S1 |
| 4 | SIGSEGV/SIGBUS fault recovery with Linux `ucontext` | `safecopy/sighandler_arm64.s`, `safecopy.go:135-137` | darwin `ucontext_t`/`__darwin_mcontext64` handler | S1 |
| 5 | `/proc/self/maps` enumeration | `virtual_map.go:66-68`, `systrap/stub_unsafe.go:115` | `mach_vm_region_recurse` | S1 |
| 6 | `memfd_create`, `ftruncate`, `fallocate(0 / PUNCH_HOLE)`, `mmap(MAP_SHARED)`, `madvise(HUGEPAGE/NOHUGEPAGE/POPULATE_WRITE)`, `mlock`, `mincore`, `fstat` | `memutil/memfd_linux_unsafe.go`, `pgalloc.go:522-1053, 1122-1141, 1753, 1842`, `usage/memory.go:270-282` | `shm_open`+`ftruncate`+`mmap`; `madvise(MADV_FREE_REUSABLE)` or manual zero; `mincore` exists; `F_PUNCHHOLE` (verify) | S1 (create/map/zero), S2 (punch hole, THP) |
| 7 | Raw `SVC` syscalls with Linux ABI (`pkg/hostsyscall`, `getcpu` asm, `syscall.AllThreadsSyscall6(prctl)`) | `hostsyscall_arm64.s`, `getcpu_arm64.s`, `machine_arm64_unsafe.go:367` | libSystem calls (`x/sys/unix` darwin trampolines); no PAC prctl needed | S1 |
| 8 | `epoll`, `eventfd2`, `ppoll` | `fdnotifier`, `eventfd` | `kqueue`/`kevent`, pipe or `EVFILT_USER`, `poll` | S1 |
| 9 | `hostarch.PageSize = 4096` (4K/64K only) | `hostarch_arm64_4k.go`, `ring0/aarch64_4k.go`, `pagetables_arm64_4k.go`, `kvm/machine_arm64.go:270-278` | New `pagesize_16k` variant | S1 |
| 10 | `/proc/self/auxv`, `/sys/devices/system/cpu/possible`, `/proc/sys/vm/mmap_min_addr`, `/proc/meminfo`, `/sys/kernel/mm/transparent_hugepage/*`, `uname` | `cpuid.go:333`, `hostcpu.go:35`, `platform/mmap_min_addr.go:27`, `hostos`, `hostmm.go:36` | `sysctl`, constants | S1 (small) |
| 11 | seccomp (`SECCOMP_RET_TRAP` mmap trap in KVM; sandbox filters) | `kvm/mmapfilter`, `machine.go:915-931`, `runsc/boot/filter` | None; disable (`forceMappingEntireAddressSpace` path) | S1 disable / S2 replace |
| 12 | `membarrier(2)` | `hostmm/membarrier.go` | None; `HaveGlobalMemoryBarrier()=false` | S2 |
| 13 | `KVM_IOEVENTFD` MMIO eventfd | `ioeventfd.go` | None; plain `write` fallback | S2 |
| 14 | Shadow vDSO / `cntvct` patching, `KVM_ARM64_REGS_TIMER_CNT`, Go `runtime.vdsoClockgettimeSym` | `vdso*.go`, `sentry/time/vdso_arm64.tmpl.s` | `clock_gettime` via libc; HVF `HV_SYS_REG_CNTVOFF_EL2`-style offset (verify) | S2 |
| 15 | cgroup v1 memcg pressure (`cgroup.event_control`) | `hostmm/hostmm.go:58-108` | None | S2 |
| 16 | Linux AIO (`io_setup`...), `preadv2/pwritev2`, `recvmmsg` | `aio`, `hostfd`, `rawfile` | `pread/pwrite`, `recvmsg` loops | S2 |
| 17 | futex (`flipcall`, KVM vCPU state waits) | `flipcall/futex_linux.go`, `machine_unsafe.go:122-150` | `os_sync_wait_on_address` or Go channels/`sync` | S1 for KVM waits (can use Go sync), S2 for flipcall |
| 18 | ptrace/seccomp stub platforms | `ptrace/*`, `systrap/*` | Not portable | excluded |
| 19 | PAC key disabling (`PR_PAC_SET_ENABLED_KEYS`) | `machine_arm64_unsafe.go:365-376` | Not available; libSystem is arm64e-PAC'd (verify interaction) | S1 risk |
| 20 | `abi.Host = Linux`, `syserr` host errno table | `abi/abi_linux.go`, `syserr/host_darwin.go` | `abi_darwin.go`; extend errno table | S1 (trivial) |

### Inferences
- Rough new/changed code for S1: `platform/hvf` ≈ 5–7k lines (KVM minus bluepill asm and seccomp, plus HVF bindings via cgo or `purego`-style dynamic linking), 16K page-size variants ≈ 400 lines across `hostarch`/`ring0`/`pagetables`, darwin backends for `pgalloc`/`memutil`/`usage`/`hostmm` ≈ 300 lines, `sighandling`+`safecopy` darwin ≈ 300 lines, `fdnotifier`/`eventfd` kqueue ≈ 300 lines, `hostsyscall`/`hostcpu`/`cpuid`/`hostos` darwin ≈ 300 lines, a custom darwin loader (replacing `runsc/boot` for the demo) ≈ 1–2k lines, plus Bazel toolchain work for the guest vDSO. Total on the order of 8–12k lines, dominated by the platform.
- Security note for the report: on macOS the Sentry would run with neither seccomp nor a separate gofer process in S1; the VM boundary (HVF) still isolates the *application* from the Sentry, but the Sentry-to-host boundary loses the seccomp layer described in `SECURITY.md`.

### Gaps
- HVF API specifics (lazy mapping of `PROT_NONE` ranges, unmapped-GPA exit semantics, stage-1 4K granule support on Apple Silicon, `hv_vcpu_run` thread affinity rules, availability of `F_PUNCHHOLE` on shm) must be confirmed from Apple documentation; they are not derivable from this repository.
