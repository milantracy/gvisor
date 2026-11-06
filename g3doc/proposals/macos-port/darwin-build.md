# Building gVisor on macOS (darwin/arm64)

Status: in progress. Apple M2 Max, macOS 26.6.2, Go 1.27.3.

## Bazel

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
| `//vdso` | `/usr/bin/aarch64-linux-gnu-gcc: No such file` (and the x86_64 one) | Needs a Linux-targeting C toolchain. |
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

## vDSO

The `//vdso` genrule uses the C++ toolchain, which resolves to a Linux GNU
cross compiler that is not on the Mac. The output is a `-nostdlib` Linux ELF,
so any compiler that targets `aarch64-linux-gnu` and an ELF linker can build
it. Homebrew LLVM 19 has `clang` but no `ld.lld` (Homebrew ships `lld` as a
separate formula). Options:

1.  A darwin branch in the genrule that calls `clang --target=aarch64-linux-gnu
    -fuse-ld=lld`. Needs `lld` on the Mac.
2.  A vendored prebuilt `vdso.so` for darwin builds, checked by
    `//vdso:vdso_test`.
