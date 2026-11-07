# Building gVisor on macOS (darwin/arm64)

Status: busybox runs on the `hvf` platform (Phase 1 exit criterion, 2026-11-07).
Apple M2 Max, macOS 26.6.2, Go 1.27.3. The code is on the
`macos-port/prototype` branch; the commands below run in a checkout of it.

## Host setup

Install these once before building gVisor on a Mac:

1.  Xcode Command Line Tools, for `clang`:

    ```
    xcode-select --install
    ```

2.  `lld` (the LLVM ELF linker) and `bazelisk`, from Homebrew:

    ```
    brew install lld bazelisk
    ```

3.  Optional, to run `//vdso:vdso_test`: Homebrew LLVM, for `llvm-readelf`.
    The test calls `readelf`, so put a link named `readelf` on `PATH`:

    ```
    brew install llvm
    ln -s "$(brew --prefix llvm)/bin/llvm-readelf" "$(brew --prefix)/bin/readelf"
    ```

No sudo and no Docker are needed. Bazel downloads the Go SDK and a Linux
header sysroot itself. Build with `bazelisk build //<target>` from the
repository root; `.bazelrc` applies the `macos` config automatically.


Native Bazel works on the Mac. No Docker and no `make` wrapper are needed.

-   Bazel 9.3.1 (from `.bazelversion`) through bazelisk. bazelisk was
    installed with `GOBIN=<dir> go install
    github.com/bazelbuild/bazelisk@v1.26.0`; nothing system-wide.
-   The host platform is `darwin_arm64`; Bazel downloads the darwin Go SDK.
    `//pkg/abi/linux` builds from a cold cache in 2 min 47 s.
-   `.bazelrc` sets `--//tools/bazeldefs:page_size=16k` on macOS hosts
    (`build:macos`), so `hostarch`, `ring0` and `pagetables` use 16K pages.
    See [16K pages](#16k-pages). A Linux cross-build from the Mac must set
    `--//tools/bazeldefs:page_size=4k` for a 4K Linux host.

## First pass over the demo path

Command (2026-11-06):

```
bazelisk build --keep_going //pkg/ring0/pagetables //pkg/sentry/pgalloc \
  //pkg/sentry/kernel //pkg/sentry/loader //pkg/sentry/fsimpl/erofs \
  //pkg/sentry/fsimpl/host //pkg/sentry/fsimpl/tmpfs //pkg/sentry/fsimpl/dev \
  //pkg/sentry/platform //pkg/sentry/vfs //pkg/sentry/mm \
  //pkg/sentry/syscalls/linux
```

Result: 1 of 12 targets built (`//pkg/ring0/pagetables`). These are the
first-level failures only; packages that depend on them were not compiled.

| Target | First error | Cause |
|---|---|---|
| `//vdso` | `/usr/bin/aarch64-linux-gnu-gcc: No such file` (and the x86_64 one) | Needs a Linux-targeting C toolchain. Fixed; see [vDSO](#vdso). |
| `//pkg/fdchannel` | `unix.SOCK_CLOEXEC` undefined | No `SOCK_CLOEXEC` on darwin. |
| `//pkg/sentry/usage` | `memutil.CreateMemFD` undefined | No `memfd_create`. |
| `//pkg/fd` | `unix.O_LARGEFILE` undefined | Linux-only flag. |
| `//pkg/p9` | `Stat_t.Atim`, `Rdev` type | darwin `Stat_t` layout. |
| `//pkg/pinring` | `SYS_IO_URING_*` undefined | io_uring. |
| `//pkg/seccomp` | `SYS_PRCTL`, `ENOTUNIQ` undefined | No seccomp on darwin. |
| `//pkg/fsutil` | `unix.Getdents`, `SYS_UTIMENSAT`, `SYS_RENAMEAT2`, `Dirent.Off` | Linux syscalls and dirent layout. |
| `//pkg/eventfd` | `SYS_EVENTFD2` undefined | No eventfd. |
| `//pkg/sentry/hostfd` | `SYS_PREAD64`, `SYS_PREADV2`, `MaxReadWriteIov` | Linux syscall numbers. |
| `//pkg/aio` | `SYS_PREAD64`, `SYS_PREADV2` | Linux syscall numbers. |
| `//pkg/flipcall` | `futexSetPeerActive` undefined | futex. |

### Why the demo path needs them

Every failing package is reached from `//pkg/sentry/kernel`
(`bazelisk cquery "somepath(//pkg/sentry/kernel, <target>)"`), so each needs
darwin support or a dependency cut:

| Package | Path from `//pkg/sentry/kernel` |
|---|---|
| `seccomp`, `pinring` | `sentry/platform` |
| `aio` | `sentry/state/stateio` |
| `p9`, `fdchannel`, `flipcall` | `devutil` → `lisafs` |
| `eventfd` | `sentry/time` → `metric` → `eventchannel` → `unet` |
| `fd`, `sentry/usage` | direct |
| `fsutil`, `sentry/hostfd` | `sentry/fsimpl/tmpfs` |
| `//vdso` | `sentry/loader` → `loader/vdsodata` |

## Progress

Each package is checked with a native darwin build and test, and with a
linux/arm64 cross-build whose tests run in a `debian:trixie` container.

| Package | darwin build | darwin tests | linux/arm64 tests | Change |
|---|---|---|---|---|
| `//vdso` | ok | `check_vdso.py` ok | unchanged | clang, lld, `@linux_sysroot` |
| `//pkg/fd` | ok | pass | pass | `O_LARGEFILE` per OS |
| `//pkg/fdchannel` | ok | pass | pass | `SOCK_DGRAM` and one iovec on darwin |
| `//pkg/sentry/hostfd` | ok | pass | pass | libSystem read/write wrappers on darwin |
| `//pkg/aio` | ok | pass | pass | libSystem wrappers; no `LinuxQueue` on darwin |
| `//pkg/p9` | ok | pass | pass | `abi/linux` fallocate constants |
| `//pkg/seccomp` | ok | Linux-only test | pass | Filter install returns `ENOSYS` on darwin |
| `//pkg/pinring` | ok | Linux-only test | pass | io_uring calls return `ENOSYS` on darwin |
| `//pkg/eventfd` | ok | pass | pass | Non-blocking pipe on darwin; `Read` returns the sum |
| `//pkg/unet` | ok | pass (4 skipped) | pass | Per-OS socket creation, `poll`, `SO_NOSIGPIPE`; no `SOCK_SEQPACKET` on darwin |
| `//pkg/fsutil` | ok | no tests | builds | darwin directory listing; gofer-only wrappers stay Linux-only |
| `//pkg/memutil` | ok | no tests | builds | `CreateMemFD` is an unlinked APFS temp file on darwin |
| `//pkg/flipcall` | ok | pass | pass | `os_sync_wait_on_address` and `os_sync_wake_by_address_*` (shared) on darwin; no file seals |
| `//pkg/sentry/usage` | ok | no tests | builds | Fixed by `memutil` |
| `//pkg/sighandling` | ok | no tests | builds | libSystem `sigaction` through a cgo-free trampoline |
| `//pkg/safecopy` | ok | pass | pass | darwin arm64 signal handler |
| `//pkg/sentry/hostmm` | ok | no tests | builds | `membarrier` returns `ENOSYS` on darwin |
| `//pkg/sentry/state/stateio` | ok | pass | builds | No `O_DIRECT` on darwin |
| `//pkg/fdnotifier` | ok | pass | builds | kqueue with `EV_CLEAR` on darwin |
| `//pkg/errors/linuxerr`, `//pkg/syserr` | ok | pass | pass | Host errno to Linux errno table on darwin; see below |
| `//pkg/sentry/socket/unix/transport` | ok | pass | pass | Family from `getsockname`; `SO_NOSIGPIPE`; no `SO_PASSCRED` |
| `//pkg/lisafs` | ok | pass | pass | Linux ABI values on the wire; kqueue hangup watchdog on darwin |
| `//tools/bazeldefs:tags.bzl` | | | | `_darwin` file suffix; see below |
| `//pkg/sentry/pgalloc` | ok | pass | pass | `F_PUNCHHOLE` decommit; commit writes zeros; no huge page hints or `MADV_POPULATE_WRITE` |
| `//pkg/sentry/fsimpl/eventfd` | ok | pass (1 skipped) | builds | No host eventfd on darwin |
| `//pkg/sentry/platform` | ok | no tests | builds | `vm.mmap_min_addr` read only on Linux; 65536 on darwin |
| `//pkg/sentry/contexttest` | ok | | not checked | ptrace platform only on Linux; nil platform on darwin |
| `//pkg/sentry/fsutil` | ok | pass | pass | No `MAP_FIXED_NOREPLACE`; the address is a hint |
| `//pkg/sentry/socket`, `socket/netstack`, `socket/unix`, `fsimpl/gofer` | ok | pass | pass | Linux ABI constants for guest values; see below |
| `//pkg/sentry/fsimpl/host` | ok | pass (new test) | pass | Per-OS stat, readiness, status flags, timestamps; no host TTY on darwin |
| `//pkg/sentry/kernel`, `//pkg/sentry/syscalls/linux` | ok | not run | builds | Host errnos and status flags converted to Linux values |
| `//pkg/hvf` (new) | ok | pass | builds (no files) | cgo-free Hypervisor.framework binding; see below |

### Second pass over the demo path (2026-11-06)

With the packages above fixed, the same 12 targets still build 1 of 12. The
next failures:

| Package | First errors |
|---|---|
| `//pkg/sentry/hostmm` | `SYS_MEMBARRIER` |
| `//pkg/sentry/socket/unix/transport` | `pkg/fdnotifier` has no darwin files (epoll); `SO_DOMAIN`, `SO_PASSCRED` |
| `//pkg/lisafs` | `STATX_*`, `POLLRDHUP`, `Ppoll`, `EREMOTEIO` |
| `//pkg/sentry/state/stateio` | `O_DIRECT` |

### darwin host behavior found on the way

These differ from Linux and matter beyond the build:

-   **`PROT_NONE` access raises SIGBUS**, with the same `si_code` (1) as an
    access past the end of a mapped file, so `safecopy` returns `BusError`
    where Linux gives `SegvError`. An access to unmapped memory raises
    SIGSEGV.
-   **Signals sent by a process have positive `si_code`** (`SI_USER` is
    `0x10001`). A signal handler that checks "from the kernel" must use
    `0 < si_code < 0x10000`.
-   **`SCM_RIGHTS` truncation leaks descriptors.** If the passed descriptors do
    not fit in the receiver's control buffer, XNU still installs all of them
    in the receiver, sets `MSG_CTRUNC` and leaves `cmsg_len` at the full
    length. Linux closes the ones that do not fit. A darwin gofer protocol
    must always receive with room for every descriptor the peer can send.
-   **No `AF_UNIX` `SOCK_SEQPACKET`**, no `SOCK_CLOEXEC`, no `MSG_NOSIGNAL`.
-   **`sendmsg` and `recvmsg` need at least one iovec** (`EMSGSIZE`
    otherwise), and `recvmsg` returns 0 at once if the iovecs have no space.
-   **`sun_path` is 104 bytes** (108 on Linux).
-   **errno values differ.** Only the classic errnos up to `ERANGE` (34)
    have the Linux value; for example `EAGAIN` is 35, which is `EDEADLK` on
    Linux. The rule in the port: `unix.Errno` holds host values,
    `errno.Errno` and `*errors.Error` hold Linux values, and
    `linuxerr.HostToLinux` and `linuxerr.LinuxToHost` convert between them.
    On Linux both are the identity. A darwin errno with no Linux equivalent
    (such as `EAUTH`) becomes `EIO`. `kernel.ExtractErrno` converts host
    errnos before it returns them to the application.
-   **Open and `*at` flags differ.** `O_TRUNC` is `0x400` (Linux `0x200`)
    and `AT_REMOVEDIR` is `0x80` (Linux `0x200`). lisafs carries Linux
    values; a darwin gofer must convert them before it calls the host.
-   **`poll` with only `POLLHUP` does not report a shutdown while the socket
    has unread data.** With no unread data it does, for a local or peer
    shutdown, and pending data does not wake it. A kqueue `EVFILT_READ`
    filter with `EV_CLEAR` and `NOTE_LOWAT` above the buffer size reports
    `EV_EOF` in all cases.
-   **Linux ABI values must not come from `unix.*`.** On a Linux host,
    `unix.AF_INET6`, `unix.SizeofCmsghdr` and `unix.SYS_SETSOCKOPT` equal
    the guest values, so the Sentry used them for application values. On
    darwin they differ (`AF_INET6` is 30, Linux 10; `cmsghdr` is 12 bytes,
    Linux 16). Use `linux.*` for anything the application sees. The compiler
    finds only the constants that darwin does not define; a search for
    `unix.` in code that handles application values finds the rest.
-   **APFS `SEEK_DATA` does not see pages that are dirtied through a
    mapping** until they are written back (`msync`; `fsync` is not enough),
    and it reports data in 16 KiB units. pgalloc already does not trust
    `SEEK_DATA` on an unknown filesystem, so save scans every committed
    page. Files are sparse after `ftruncate` (1 GiB truncate: 0 bytes
    allocated).
-   **No file seals.** The flipcall packet window cannot get
    `F_SEAL_SHRINK`, so a peer that truncates it causes SIGBUS in the other
    party.
-   **`ID_AA64MMFR0_EL1` is not readable from EL0.** Linux emulates the
    `mrs`; macOS raises SIGILL. `pagetables` read it in `init`, so every
    binary that imported it crashed at startup. On darwin, `pagetables` uses
    8-bit ASIDs, which all implementations support.

### Build rules

`tools/bazeldefs/tags.bzl` lists the file suffixes that split a package into
sets for `go_marshal` and `go_stateify`. Without `_darwin` in the list, a
`foo_darwin.go` file was in the default set, its `//go:build darwin`
constraint went to the generated file, and the package lost its generated
methods on Linux. nogo found this, because it type-checks each package for
linux/amd64 and linux/arm64 too. Run the `_nogo` targets on darwin after each
change.

### Building Linux targets on a Mac host

The seccomp precompile rule (`pkg/seccomp/precompiledseccomp`) builds its
generator for the target and runs it during the build. For a Linux target
on a Mac host it needs a QEMU user-mode toolchain, which exists only for
Linux hosts, so it fails with exit 126. This affects every target that
reaches `platform/ptrace`, `systrap` or `kvm` for Linux, including the
linux/amd64 and linux/arm64 nogo analyses of those packages and Linux tests
that use `contexttest`. Check these on a Linux host, or with `make test`.

### Third pass over the demo path (2026-11-07)

| Package | First errors |
|---|---|
| `//pkg/sentry/pgalloc` | `unix.Fallocate`, `FALLOC_FL_PUNCH_HOLE`, `MADV_HUGEPAGE`, `MADV_NOHUGEPAGE`, `MADV_POPULATE_WRITE` |
| `//pkg/sentry/fsimpl/eventfd` | `unix.Eventfd` |

Both are done. Later passes found `sentry/fsutil`, `sentry/socket` and
`socket/unix` (done), then:

| Package | First errors |
|---|---|
| `//pkg/sentry/fsimpl/host` | `statx`, epoll, `Stat_t` layout, host terminal `ioctl`s |

`fsimpl/host` is done (`fstat` in place of `statx`, kqueue readiness, host
status flags converted to Linux flags, `futimens`; host TTYs return
`ENOTTY`). After it, **all 12 demo-path targets build on darwin**
(2026-11-07), including `kernel`, `mm` and `syscalls/linux`. Nothing runs
yet: there is no darwin platform.

A search of the demo-path Sentry packages for `unix.*` constants that can
be application values found one more (`fdsToHostFiles` masked host status
flags with `linux.O_NONBLOCK`), now fixed. The other uses are host calls.

## `pkg/hvf`

`pkg/hvf` calls Hypervisor.framework without cgo. Each function has a
`cgo_import_dynamic` from the framework and an arm64 trampoline, as for
libSystem. `hv_vcpu_run` goes through `syscall.syscall` so that the scheduler
can give the P to another thread; the other calls use `syscall.rawSyscall`.
`hv_vcpu_set_simd_fp_reg` takes its value in Q0, so a shim loads Q0 from a
pointer and then jumps to it. The package has code only on darwin/arm64.

The test copies its own binary, signs the copy ad hoc with the
`com.apple.security.hypervisor` entitlement (`codesign -s -`; no developer
account), and runs the copy. If signing fails, the tests skip. The tests
run guest code at EL1h with the MMU off: an `hvc #1` exit with the expected
X0, register and Q-register round trips, and `hv_vcpus_exit` from another
thread (`ExitCanceled`).

`BenchmarkHVCRoundTrip` (5 runs, 2026-11-07): 713 to 730 ns per HVC exit and
re-entry, against about 680 ns for the C probe 1 (`phase0-results.md`).

## 16K pages

The `pagesize_16k` build tag (`--//tools/bazeldefs:page_size=16k`) selects:

-   `hostarch`: `PageShift` 14, `HugePageShift` 25 (32 MiB).
-   `pagetables`: four levels for a 48-bit VA. The top level has two entries
    (bit 47); the other levels have 2048 entries (11 bits). The 16K granule
    has no level 1 block descriptors without FEAT_LPA2, so the walker makes
    PMD blocks (32 MiB) and pages only (`pudSect`). The 4K walker is shared.
-   `ring0`: initial page table offsets in 16K steps.

The vDSO reads its parameters 4 KiB below its base (`_params` in
`vdso/vdso_*.lds`), and the parameter page (one `PageSize` page) is mapped
just below the vDSO. So `kernel.NewVDSOParamPage` writes the parameters in
the last 4 KiB of the page. With 4K pages this is the whole page, so
nothing changes on Linux. The 64K variant had the same problem.

Results, 2026-11-07:

| Target | 16k on darwin | 4k on darwin | linux/arm64 (4k) |
|---|---|---|---|
| `pagetables` | pass | pass | pass |
| `hostarch` | pass | pass | pass |
| `pgalloc`, `tmpfs`, `fsutil`, `memmap`, `fsimpl/host`, `hvf` | pass | | |
| 12 demo-path targets | build | build | |

`pagetables` tests that hard-coded 4K offsets now use `pteSize`.
`Test1GAnd4K` expects 32 MiB blocks when `pudSect` is false. `mm_test` does
not build on darwin at any page size (it uses memfd seals). `systrap` still
has 4K and 64K variants only; it is not built on darwin.

## IPA registry

`//pkg/sentry/platform/hvf/ipa` maps host ranges to IPA on demand
(decision (B), `memory-mapping-options.md`). It is pure Go behind a `Mapper`
interface, so its tests use a fake stage 2 that, like HVF, rejects a map over
a mapped IPA, and run on both OSes.

-   `Translate(addr, length, flags, fn)` gives the IPA of each part of a host
    range. It registers and maps parts that are not registered yet
    (requirement 4: only ranges that the caller has mapped), and adds
    permissions with `Protect`.
-   IPA is bump-allocated and never reused, so a stale guest stage-1 entry
    cannot reach new memory (requirement 1). A region of at least 32 MiB gets
    an IPA equal to its host address modulo 32 MiB, for PMD blocks.
-   The owners of host mappings call `memutil.WillUnmap` before `munmap`
    (`pgalloc` chunks, `fsutil` cached and precise mappings, the EROFS
    image) and `memutil.WillReplace` before `mmap(MAP_FIXED)` over a
    mapping (`fsutil` read-to-write upgrades and `RegenerateMappings`).
    The registry is the `memutil.MappingObserver`. `WillUnmap` removes the
    stage-2 mapping and forgets the range. `WillReplace` removes the stage-2
    mapping and keeps the IPA; `Translate` or `Refault` (on a stage-2 fault,
    requirement 5) maps the new host pages at the same IPA.
-   On darwin the EROFS image is mapped `MAP_PRIVATE` (requirement 6).

EROFS needs a block size of at least `hostarch.PageSize`, so with 16K pages
an image must be made with `mkfs.erofs -b 16384`. The `erofs` test that
hard-coded 4 KiB blocks now uses `hostarch.PageSize`.

Not done yet: the guest-memory `pagetables.Allocator` (requirement 9) and
`hv_vm_unmap` before `F_PUNCHHOLE` decommit of mapped `pgalloc` ranges.
Both are part of the platform step.

## `hvf` platform

`//pkg/sentry/platform/hvf` (darwin/arm64, 16K pages only).

-   **Guest EL1 stub.** One page, mapped at `0xfffffffffe000000` through
    TTBR1. Vector slot n is `hvc #(0x100+n)`, so an EL0 exception exits to
    the host with the application registers still in the vCPU. The host
    reads `ESR_EL1`, `ELR_EL1`, `FAR_EL1` and `SPSR_EL1`, and resumes by
    setting PC and CPSR to EL0 directly. A flush stub at offset `0x800`
    (`tlbi vmalle1is; ic ialluis; dsb ish; isb; eret`) is the entry point
    when the address space changed since the vCPU last ran it.
-   **vCPU pool.** One locked thread per host CPU, each with its own vCPU.
    `Context.Switch` sends the switch to the pool over a channel. A
    `CANCELED` exit at EL1 (in a vector before its HVC, or in the flush
    stub) is handled from the EL1 state.
-   **Interrupts.** `interrupt.Forwarder`; the receiver calls
    `hv_vcpus_exit` on the vCPU that runs the context. The cancel is
    sticky, so a kick before `hv_vcpu_run` is not lost.
-   **Page tables.** `pagetables` with an `Allocator` that allocates from
    4 MiB anonymous chunks that the registry maps (requirement 9). Each
    address space shares the TTBR1 half. All address spaces use ASID 0.
-   **TLB.** Each change (`MapFile`, `Unmap`) bumps the generation of the
    address space, then stops each vCPU that is in the guest with that
    address space and waits for its run to return. A vCPU enters through
    the flush stub if the address space or its generation changed.
-   **Faults.** A stage-1 fault goes to the stub, then the Sentry gets
    `SIGSEGV` with the access type from `ESR_EL1`. A stage-2 fault exit
    goes to `ipa.Registry.Refault`; if that does not handle it, the
    application gets `SIGBUS`.
-   **System registers.** `SCTLR_EL1` lets EL0 use `DC ZVA`, `CTR_EL0`,
    cache maintenance, WFI and WFE. `TCR_EL1` is 48-bit VA, 16K granules,
    40-bit IPA, TBI0. `CNTKCTL_EL1` lets EL0 read the virtual counter.

`//pkg/hvf/hvftest` runs a test binary in an ad-hoc signed copy with the
hypervisor entitlement; `pkg/hvf` and the platform tests use it.

Checkpoint 2 (2026-11-07): `TestProgram` runs hand-written EL0 code through
`Context.Switch`: a system call, FP and TLS state, a store fault with the
write access type, the store after `MapFile`, an interrupt of a guest loop,
and a fault after `Unmap` (so the TLB was invalidated). 50 runs pass.
`BenchmarkSyscall`: 13.7 µs per system call through `Switch`, against
0.72 µs for an HVC round trip. The difference is the channel hand-off and
about 140 register calls per round trip (no lazy FP state yet).

Known gap: EL0 reads of ID registers get `SIGILL` (no emulation yet). The
gVisor KVM platform does not emulate them either, and `AT_HWCAP` does not
set `HWCAP_CPUID`.

Page tables (2026-11-07): each address space has its own allocator over a
shared pool of page-table pages. A page that `Unmap` frees goes back to the
pool only after the address space has bumped its generation and kicked the
vCPUs that run it, and `Release` returns all pages, including the root.
`TestPageTablePages` checks that 50 address spaces do not leak pages.

### System call cost

`BenchmarkSyscall` runs one system call through `Switch`: a hand-off to a
vCPU thread, the state load, `hv_vcpu_run` to the `SVC` and its `HVC` exit,
the state save, and a hand-off back. Apple M2 Max, 2026-11-07:

| Change | Per system call |
|---|---|
| Go channel hand-off, one call out of Go per register (about 140) | 13.6 µs |
| Spin-then-wait hand-off with vCPU affinity (`os_sync_wait_on_address`) | 6.2-9 µs |
| Idle vCPU threads block correctly, with the P released | 3.24 µs |
| Register state in two `hvf.Batch` calls instead of about 140 calls | 2.39 µs |

Of the 2.39 µs, `hv_vcpu_run` is about 0.8 µs and the two hand-offs are
about 0.45 µs. Each call from Go into libSystem costs 13-18 ns of runtime
overhead, more than the register access itself, so `hvf.Batch` makes a list
of register calls in one assembly loop (67 registers: 607 ns, against
about 950 ns with single calls). In the 6.2-9 µs row, a wrong argument
count made each idle vCPU thread spin on a failing
`os_sync_wait_on_address`. A raw (P-keeping) wait is also wrong: 12 blocked
vCPU threads hold all Ps, and the process deadlocks.

The Go CPU profiler (`-test.cpuprofile`) stops the guest: the vCPU stays in
`hv_trap` at the first instruction of the flush stub. Without the profiler
the same test passes. Use `sample <pid>` and `go tool addr2line` (load
address `0x100000000`) to profile instead.

## `hvfboot`

`//runsc/hvfboot` is a launcher for the Phase 1 demo, not `runsc`. It boots
a Sentry on the `hvf` platform, mounts an EROFS image as the root
filesystem, imports its own standard I/O, and runs one program. It signs a
copy of itself with the hypervisor entitlement (`//pkg/hvf/entitle`) and
runs that copy; no developer account is needed.

```
runsc/hvfboot/make_busybox_image.sh busybox.erofs   # needs Docker
bazelisk build //runsc/hvfboot
bazel-bin/runsc/hvfboot/hvfboot_/hvfboot -image busybox.erofs /bin/busybox echo hello
```

The image needs blocks of at least the page size (16 KiB) and no inline
file data (`mkfs.erofs -b 16384 -E noinline_data`), because the Sentry maps
file data directly from the image. Debian's `mkfs.erofs` allows blocks only
up to 4 KiB, so the script builds erofs-utils 1.8.6 with a larger
`MAX_BLOCK_SIZE`. `-strace` logs system calls; `-debug` logs at debug level.

Other changes for the launcher:

-   `sentry/time` reads host clocks with `clock_gettime(2)` on darwin; the
    vDSO-symbol implementation is Linux-only.
-   `strace` sets `seccomp.SyscallName` only on Linux.
-   `linuxerr.HostToLinux` on darwin inverts the full Linux errno range up to
    `MAX_ERRNO`. Before, the restart errnos (512-516) became `EIO`, so an
    interrupted system call was neither restarted nor converted to `EINTR`
    (busybox `sh` reported status 255 for every child).

Checkpoints 3 and 4 (2026-11-07): `echo hello`, `dmesg`, `uname -a`, `ls`,
and `sh -c '/bin/uname -m | /bin/cat; echo st=$?'` give the correct output
and exit status. 20 runs of the pipeline pass, at about 0.37 s each,
including the re-exec and signing.

## Python

`make_image.sh <out.erofs> <image>` makes an hvfboot image from any
linux/arm64 container image. `hvfboot` mounts `/proc`, `/dev` (memory
devices) and a tmpfs `/tmp` on the read-only root, and takes `-e KEY=VALUE`
and `-v host:guest[:ro]` (see "Host mounts").
procfs adds the nvproxy files only on Linux.

```
runsc/hvfboot/make_image.sh python.erofs python:3.13-slim
hvfboot -image python.erofs /usr/local/bin/python3 - < script.py
```

Result (2026-11-07, `python:3.13-slim`, Python 3.13, glibc 2.41): a smoke
test passes 14 of 14 checks: stdlib imports (including `ssl`, `ctypes`,
`sqlite3`), hashing, `os.urandom`, tempfiles in `/tmp`, 8 threads,
`subprocess` (fork and exec), `asyncio` over a Unix socket, `sqlite3`,
`setitimer` signals, `mmap`, `ctypes` calls into libc, `multiprocessing.Pool`,
and `decimal`. Most checks take the same time as in Docker on the same Mac;
`multiprocessing` took 78 ms against 51 ms, and takes 58-68 ms with the
system call changes above.

| Startup | Time per run |
|---|---|
| `python3 -c pass` in a Docker container (no container start) | 6 ms |
| `python3 -c pass` in hvfboot, binary signed in advance | 28 ms |
| `hvfboot /bin/true`, sign and re-exec on each run | 366 ms |
| `hvfboot python3 -c pass`, sign and re-exec on each run | 387 ms |
| `docker run python:3.13-slim python3 -c pass` | 193 ms |

`python3 -c pass` makes 428 system calls. Signing a copy of the binary and
starting it again cost about 330 ms of every run, so `//runsc/hvfboot:hvfboot`
is now signed at build time (`hvf_signed_binary` in
`//pkg/hvf/entitle:defs.bzl`), and signs and re-executes a copy only when
Hypervisor.framework returns `HV_DENIED`:

| Startup, signed at build time | Time per run | With 2.39 µs system calls |
|---|---|---|
| `hvfboot /bin/true` | 11 ms | 8.9 ms |
| `hvfboot python3 -c pass` | 28 ms | 22.0 ms |
| busybox `sh`, 200 times fork, exec `/bin/true`, wait | 181 ms | 110 ms |

To build and run:

```
bazelisk build //runsc/hvfboot
"$(bazelisk cquery --output=files //runsc/hvfboot)" -image python.erofs /usr/local/bin/python3 -c 'print(1)'
```

`bazel-bin` points to the configuration of the last build, so after a Linux
cross-build it has no `hvfboot`; `cquery --output=files` gives the path.

## Gofer (Phase 2)

Phase 2 starts with host files through a gofer: a separate process that
serves a host directory to the Sentry over lisafs. `pkg/lisafs`,
`pkg/flipcall` (on `os_sync_wait_on_address`) and `pkg/fdchannel` already
build on darwin.

The lisafs protocol carries Linux flags: the server keeps only
`linux.O_ACCMODE | linux.O_TRUNC` of the open flags, and unlink, rename and
xattr flags are Linux values. The Sentry sent `unix.O_TRUNC`, which equals
`linux.O_TRUNC` only on Linux; it now sends `linux.O_TRUNC`. Errnos on the
wire are Linux errnos too: the server converts host errnos with
`linuxerr.HostToLinux`, and the client converts them back with
`linuxerr.LinuxToHost`.

### Server (`runsc/gofer/fs`)

The Linux-only parts of `lisafs.go` moved unchanged to `lisafs_linux.go`,
behind small helpers; `lisafs_darwin.go` has the darwin versions. Probe
results on macOS 26.6.2 that set the design:

| Need | Linux | darwin |
|---|---|---|
| Control FD that does not read the file | `O_PATH` | `O_EVTONLY` (still needs read permission) |
| Control FD of a symlink | `O_PATH\|O_NOFOLLOW` | `O_EVTONLY\|O_SYMLINK`, without `O_NOFOLLOW` (with it: `ELOOP`) |
| Control FD of a FIFO, without blocking | `O_RDONLY\|O_NONBLOCK` | the same |
| Control FD of a socket | `O_PATH` | none: `open(2)` fails with `EOPNOTSUPP` |
| Open again with other flags | `/proc/self/fd/N` | the path with `O_NOFOLLOW_ANY`, then check device and inode (`/dev/fd/N` only duplicates) |
| Parent directory without symlinks | `openat2(RESOLVE_NO_SYMLINKS)` | `open(O_NOFOLLOW_ANY)` |
| stat, chown, utimes, readlink on an FD | `statx`, `fchownat`, `utimensat` and `readlinkat` with an empty path | `fstat`, `fchown`, `futimens`, `freadlink` (libSystem trampolines for the last two) |
| rename flags | `renameat2` | `renameatx_np` (`RENAME_EXCL`, `RENAME_SWAP`) |
| fallocate | `fallocate` | `F_PREALLOCATE` and `ftruncate`, mode 0 only |
| Directory entries | `getdents64` | `getdirentries` (x/sys emulation on `readdir`) |

`O_NOFOLLOW_ANY` rejects any symlink in the path, also above the mount
point, so a mount source must be a real path (`/private/tmp`, not `/tmp`).
The server holds the rename lock for reading in every operation that opens
a file again, so the path does not change.

Not supported on darwin:

-   Files that the gofer user cannot read (mode 000): no control FD.
-   Unix sockets in a host directory: the server cannot walk to them.
-   Open and set-attribute on deleted files (`OpenOnDeleted` and
    `SetAttrOnDeleted` are false): a deleted file has no path to open again.
-   Connecting to a host socket as another user: darwin has no per-thread
    credentials.
-   Overlay whiteouts (`mknod` of a 0:0 character device needs root).

Result (2026-11-07): the lisafs test suite (`//runsc/gofer/fs:lisafs_test`)
passes on darwin, and on Linux arm64 in Docker. `groups_test` is
Linux-only.

### Client (`pkg/sentry/fsimpl/gofer`)

directfs makes Linux file system calls from the Sentry (`O_PATH`, `statx`,
`AT_EMPTY_PATH`), so `directfs_inode.go` is Linux-only. On darwin the client
uses lisafs for every operation, and the `directfs` mount option fails with
`EINVAL`. `directfs_inode_darwin.go` has a `directfsInode` whose methods
panic; no value of it exists, it only completes the type switches in
`inode_impl.go`. Other darwin changes: `dup2` for `dup3`, `fsync` for
`fdatasync`, `fstat` for `statx`. The host named pipe writer check needs
`tee(2)` and fails with `EOPNOTSUPP` (host FIFOs are off by default).
`pkg/fsutil` has `Fallocate`, `RenameAt` and `RenameAt2` for darwin, which
the server uses too.

Result (2026-11-07): the client builds on darwin and Linux, and
`//pkg/sentry/fsimpl/gofer:gofer_test` passes on darwin.

### Host mounts (`hvfboot -v`)

`hvfboot -v host:guest[:ro]` (repeatable) mounts a host file or directory.
As with `runsc gofer`, one gofer process (`hvfboot gofer`, the same binary)
serves all host mounts:

-   One `SOCK_STREAM` socket pair per mount, in mount order. The gofer gets
    its ends as `-io-fds` and the host paths as `-mount ro:<path>` or
    `rw:<path>` in the same order.
-   The Sentry mounts each FD with the gofer file system: `trans=fd`,
    `rfdno`, `wfdno`, `cache=remote_revalidating`, `disable_fifo_open`, as
    `goferMountData` in `runsc/boot/vfs.go` does for a shared mount. The
    client owns that FD, so `hvfboot` gives it a duplicate.
-   Mount sources go through `realpath`: the darwin gofer allows no symlinks
    in a path that it opens again (`O_NOFOLLOW_ANY`).
-   `-fdlimit` sets `RLIMIT_NOFILE` of the gofer.
-   The gofer exits when the Sentry closes its connections.
-   The root file system is read-only, so a guest path must exist or be in
    a writable directory such as `/tmp`.

```
hvfboot -image python.erofs -v $HOME/src:/tmp/src:ro /usr/local/bin/python3 /tmp/src/main.py
```

IDs: the Sentry runs the program as root, and the gofer runs as the user.
darwin has no user namespaces, so `hvfboot gofer` calls
`fs.MapRootToEffectiveIDs`. The gofer then maps IDs as the user namespace of
rootless `runsc` does: sandbox UID/GID 0 is the gofer's EUID/EGID, other host
IDs show as 65534, and `chown` to other IDs fails with `EINVAL`. Without the
call (the lisafs test suite), sandbox IDs are host IDs.

With `cache=remote_revalidating`, the Sentry ignores a `SetStat` attribute
that the gofer could not set (`dentry.setStat` in
`pkg/sentry/fsimpl/gofer/gofer.go` returns before it checks `failureErr`). So
`chown` to an unmapped ID returns success in the sandbox and changes nothing.
Linux has the same code.

Result (2026-11-07, Python 3.13, rw and ro mounts of the same directory):
read, write, `mkdir`, `rename`, `symlink`, `readlink`, `unlink`, `rmdir`,
`stat` (owner 0:0), `EROFS` on the read-only mount, and a single-file mount
pass. No gofer process is left after the run. `python3 -c pass` takes 22 ms
without a mount and 31 ms with one; the gofer process start is most of the
difference.

### Confinement of the gofer

`runsc gofer` confines itself with a `chroot` into the mount sources, a
seccomp filter and dropped capabilities. `hvfboot gofer` uses a Seatbelt
profile instead (`//pkg/seatbelt`, `sandbox_init(3)` through a libSystem
trampoline, no cgo). It applies the profile before it serves a request:

```
(version 1)
(deny default)
(allow file-read* file-write* (subpath "<private TMPDIR>"))
(allow file-read* file-write* (subpath "<rw mount source>"))
(allow file-read* (subpath "<ro mount source>"))
```

-   `deny default` also denies exec, fork, network, Mach lookups and
    signals to other processes.
-   On darwin, `memutil.CreateMemFD` makes a file in `TMPDIR` and unlinks it
    at once; lisafs channels use it for their memory. The gofer makes a
    private, empty `TMPDIR` before the profile and removes it at exit, so
    the profile does not allow the files of other programs in `TMPDIR`.
-   Seatbelt matches real paths. Mount sources already go through
    `realpath`, and so does the `TMPDIR`.
-   Go reads `/etc/localtime` on first use, so the gofer loads it first.

Result (2026-11-07): `//pkg/seatbelt:seatbelt_test` and
`//runsc/hvfboot:gofer_test` confine a child process and check the
rules. Allowed: reads and writes under rw mounts, reads under ro mounts,
`CreateMemFD`. Denied: writes and renames out of ro mounts, reads and
directory listings outside the mounts, exec, TCP connect. The Python host
mount test passes under the profile. `python3 -c pass` with one mount takes
36 ms (31 ms without the profile).

Seatbelt denials are not in `log show` without more logging setup; the
tests check the result codes instead.

### Confinement of the Sentry

`runsc` confines the Sentry with `pivot_root` into an empty read-only tmpfs
in a new mount namespace (`setUpChroot`), namespaces and seccomp. darwin has
no mount namespaces, and `chroot(2)` needs root. `hvfboot` does this:

1.  Open all host resources: the EROFS image, the memory file, the gofer
    socket pairs (it starts the gofer here), and standard I/O (the import
    checks the host FDs).
2.  Apply the Seatbelt profile.
3.  Mount the image and the host mounts, and load the program. The Sentry
    parses untrusted data only when it is confined.

```
(version 1)
(deny default)
(allow file-read-metadata (literal "<path of a standard I/O file>"))
(allow file-read-metadata (subpath "<mount source>"))
```

Hypervisor.framework needs no rule, also for vCPUs that the Sentry creates
after the profile. FDs that the Sentry opened before keep working for reads
and writes.

Seatbelt checks `fstat` of an FD that is not open for reading as a
`file-read-metadata` of its path. FDs open for reading are not checked. Two
cases need `fstat` of a write-only FD:

-   A standard output redirected to a file. The host file system `fstat`s it
    for the size before each `O_APPEND` write (`>>`, and the output file of
    the Claude Code shell), and the program can `fstat` it.
-   A write-only host FD that the gofer gives for a file under a mount.

So the profile allows `file-read-metadata`, and nothing more, for the paths
of the standard I/O files (`seatbelt.FDPath`, `fcntl(F_GETPATH)`, before the
profile) and under the mount sources, which the gofer serves to the Sentry
already. If a standard I/O file is renamed, `fstat` of it fails.

Result (2026-11-07):

-   `//runsc/hvfboot:confine_test` runs a child whose standard output is a
    write-only `O_APPEND` file. Allowed: reads and writes on FDs opened
    before, `fstat` of the standard output, `socketpair`. Denied: opening
    any file, `fstat` of another write-only FD, listing `/`, exec, TCP
    connect.
-   The Python smoke test passes 14 of 14 checks; the host mount tests
    pass, including `O_APPEND` writes and `fstat` of write-only FDs.
-   40 runs each of `python3 -c pass`, with and without a host mount,
    complete with no process or `TMPDIR` left.
-   `python3 -c pass` takes 27 ms (22 ms without the profile); with one
    host mount, 37 ms (36 ms).

`hvfboot` does not wait for the gofer at exit. The gofer exits when its
connections close, at the latest when the Sentry exits. A gofer that is
killed (`SIGKILL`) leaves its empty `TMPDIR`.

## Networking

`runsc` runs netstack in the Sentry. The only network access of the Sentry
is an `AF_PACKET` socket on a veth in the network namespace of the sandbox,
and the host kernel routes and NATs. darwin has no network namespaces or
veth, and `vmnet` needs root or a restricted entitlement. So `hvfboot` uses
a network proxy process, as gvproxy (gvisor-tap-vsock) does for Podman:

-   The Sentry runs netstack with `lo` and `eth0` (10.0.2.15/24, default
    route via 10.0.2.2). A new network namespace has `lo` only, as in
    `runsc`. The netlink route socket is registered.
-   The link of `eth0` is a `SOCK_DGRAM` Unix socket pair: one IP packet per
    datagram, no link header, MTU 65520. darwin limits a Unix datagram to
    the send buffer size (2 KiB by default), so both ends set 1 MiB send and
    4 MiB receive buffers. The Sentry opens it before it is confined.
-   `hvfboot net` (the same binary) has its own netstack on the other end,
    in promiscuous and spoofing mode. Its TCP and UDP forwarders end each
    flow of the sandbox and make a host connection for it.
-   DNS: TCP and UDP to port 53 of any address go to the first nameserver of
    the host `/etc/resolv.conf`, which the proxy reads before it is
    confined. So the `nameserver` lines of the image do not matter, and the
    DNS configuration of the host applies. An image without
    `/etc/resolv.conf` can get one with `-v file:/etc/resolv.conf:ro`.
-   Refused: loopback and unspecified destinations, and the sandbox network
    (TCP gets a reset).
-   A read on a Unix datagram socket does not return when the peer closes,
    so the proxy also gets a `SOCK_STREAM` lifeline socket. It exits when
    the lifeline closes, which happens when the Sentry exits.
-   Only IPv4 TCP and UDP. No ICMP (`ping`) and no inbound port forwarding.
-   `-network=none` gives loopback only and starts no proxy.

The proxy runs under a Seatbelt profile:

```
(version 1)
(deny default)
(allow network-outbound (remote ip "*:*"))
(deny network-outbound (remote ip "localhost:*"))
```

For Seatbelt, `localhost` is every address of the host, also the addresses
of its network interfaces (checked with `sandbox-exec`: a connection to the
LAN address of the Mac is denied with this rule and allowed without it). So
the sandbox cannot connect to services on the Mac, also through its LAN
address.

Result (2026-11-07, Python 3.13):

-   DNS (about 15 ms), HTTP and HTTPS to public servers, `pip download
    requests` (5 wheels), UDP (an NTP query to `time.apple.com`), 200 HTTP
    requests on 50 threads, UDP datagrams up to 8000 bytes.
-   Throughput through the proxy: 1.5 GB/s from an HTTP server on the LAN
    address of the Mac (5.2 GB/s on the host), measured before the profile
    denied that address. Downloads from the internet run at the speed of the
    internet link.
-   `//runsc/hvfboot:confine_test` checks the proxy profile: connections to
    host loopback and Unix sockets, file access and exec are denied, and UDP
    sends are allowed.
-   40 runs each with and without a host mount, and 20 with
    `-network=none`, complete with no process left.
-   `python3 -c pass` takes 29 ms (28 ms with `-network=none`); with one host
    mount, 37 ms.

Next: see the Status section of RESUME.md. `runsc-comparison.md` compares
each layer with `runsc`.

## vDSO

The vDSO is a `-nostdlib` Linux ELF, built for both arm64 and amd64 by a
transition in `//pkg/sentry/loader/vdsodata`. On Linux the genrule uses the
GNU cross compilers from `@crosstool`, which a Mac does not have.

On macOS, `.bazelrc` sets `--//vdso:darwin_host` (through `build:macos` and
`--enable_platform_specific_config`). The genrule then uses:

-   Apple `clang` with `--target=aarch64-linux-gnu` or `x86_64-linux-gnu`.
-   `ld.lld`, found on `PATH`, `/opt/homebrew/bin` or `/usr/local/bin`.
-   `--sysroot` set to `@linux_sysroot`, a header-only sysroot that Bazel makes
    from three Debian packages pinned on snapshot.debian.org: `libc6-dev`
    2.41-12+deb13u4 for arm64 and amd64, and `linux-libc-dev` 6.12.107-1
    (`tools/bazeldefs/linux_sysroot.bzl`).

Both outputs pass `vdso/check_vdso.py --check-data`. With the `macos` config
off, the genrule command is the same as before (checked with `bazelisk
aquery`).
