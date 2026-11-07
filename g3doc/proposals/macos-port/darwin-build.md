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
-   With no flags, `hostarch` selects the `arm64_4k` variant. A
    `//tools/bazeldefs:page_size` flag exists (values include `64k`), which
    is where a `16k` value belongs.

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
    (such as `EAUTH`) becomes `EIO`. Still to do: `task_syscall.go` returns a
    `unix.Errno` to the application as a Linux errno.
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
-   Tests print "host page size mismatch - running on non-4K host", because
    `hostarch` uses the 4K variant on a 16K host.

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
