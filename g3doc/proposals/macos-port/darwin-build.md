# Building gVisor on macOS (darwin/arm64)

Status: in progress. Apple M2 Max, macOS 26.6.2, Go 1.27.3.

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
