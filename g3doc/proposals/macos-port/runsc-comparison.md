# runsc on Linux and the macOS port, layer by layer

This file compares how `runsc` runs a container on Linux with how the macOS
port does the same job. The macOS port follows the `runsc` process model
and its defense in depth. Where a Linux mechanism does not exist on macOS,
the table gives the replacement and what it does not cover.

Code references are to `master` at `f51a205a8` or later, and to
`macos-port/prototype` for darwin files. Status is as of 2026-11-07.

Status values:

-   **Done**: on `macos-port/prototype` and tested.
-   **Planned**: the design is agreed; the step is in the order below.
-   **Later**: not in the current plan.
-   **No equivalent**: macOS has no mechanism for it; the table says what
    covers the risk instead.

## Processes

| Layer | runsc on Linux | macOS port | Status |
|---|---|---|---|
| Launcher | `runsc` (OCI runtime) creates the sandbox and the gofer, and passes FDs to them (`runsc/container/container.go`, `runsc/sandbox/sandbox.go`) | `hvfboot` (`runsc/hvfboot`), a launcher for one program, not an OCI runtime | Done (launcher); OCI is Later |
| Sentry process | `runsc boot`, one per sandbox | `hvfboot`, one per run | Done |
| Gofer process | `runsc gofer`, one per container, serves all its mounts (`container.go`: "One gofer process serves all of a container's mounts") | `hvfboot gofer`, one per run, serves all host mounts | Planned (step 1) |
| Application isolation from the Sentry | Platform: systrap, ptrace or KVM | `hvf` platform on Hypervisor.framework; the application runs at EL0 in a VM, the Sentry on the host | Done |

## File systems

| Layer | runsc on Linux | macOS port | Status |
|---|---|---|---|
| Sentry-to-gofer connection | One `SOCK_STREAM` socket pair per lisafs mount (`createLisafsSocketPair`); the gofer gets `--io-fds`, the sandbox the other ends | The same | Planned (step 1) |
| Mount order | `--gofer-mount-confs` and `--io-fds`: root first, then the spec mounts in order | The same order: root (if served by the gofer), then each `-v` mount | Planned (step 1) |
| lisafs server | `runsc/gofer/fs` (`lisafs.go`); `Gofer.serve` makes one `lisafs.Server` and one connection per io FD | The same package; darwin host calls in `lisafs_darwin.go` | Done (server); Planned (process) |
| lisafs wire format | Linux flags and Linux errnos | The same; the darwin server translates flags and errnos | Done |
| Host file access from the Sentry | directfs: the gofer donates host FDs and the Sentry makes file system calls on them | lisafs only; directfs uses `O_PATH`, `statx`, `AT_EMPTY_PATH` and fails with `EINVAL` if asked for | Done (lisafs only) |
| Root file system | Gofer-served rootfs with an overlay, or an EROFS image opened by the Sentry (no gofer for `/`) | EROFS image opened by the Sentry | Done (EROFS); gofer-served root is Later |
| Bind mounts | Spec mounts, served by the gofer | `-v host:guest`, served by the gofer | Planned (step 1) |
| `/dev` | devtmpfs in the Sentry, optional `/dev` gofer (`--dev-io-fd`) | devtmpfs and memory devices in the Sentry; no `/dev` gofer | Done (devtmpfs) |

## Confinement of the Sentry

| Layer | runsc on Linux | macOS port | Status |
|---|---|---|---|
| File system view | New mount namespace; `pivot_root` into a read-only tmpfs with `/etc/localtime` and a minimal procfs (`runsc/cmd/sentry/sentrycmd/chroot.go`, `setUpChroot`). The Sentry keeps only FDs that it opened before | No mount namespace, `pivot_root` or bind mounts; `chroot(2)` needs root. A Seatbelt (`sandbox_init`) profile denies all file access; the Sentry opens the EROFS image, gofer sockets and `/etc/localtime` first, and those FDs keep working | Planned (step 3) |
| System call filter | seccomp-bpf (`runsc/boot/filter`, installed in `loader.go`) | No seccomp. The Seatbelt profile denies process exec, network and IPC; it filters by operation and path, not by system call | Planned (step 3) |
| Namespaces | New IPC, mount, UTS, PID and (if asked) network and user namespaces (`sandbox.go`, the `nss` list) | No namespaces on macOS. Seatbelt covers file, network, IPC and exec access | No equivalent |
| User | Optional user namespace with UID/GID mappings; can run as nobody | Runs as the calling user. A separate user needs root to set up | Later |
| Capabilities | Dropped to a minimal set | macOS has no capabilities | No equivalent |
| Resource limits | cgroups (CPU, memory, PIDs) | No cgroups. `setrlimit` only, per process | Later |

## Confinement of the gofer

| Layer | runsc on Linux | macOS port | Status |
|---|---|---|---|
| File system view | New mount namespace; the rootfs and each mount source are bind-mounted under a tmpfs at `/proc/fs`, then `chroot("/root")` (`runsc/cmd/sandboxsetup/gofer_mount.go`, `runsc/cmd/gofer.go`) | A Seatbelt profile allows file access only under the real path (`subpath`) of each mount source, read-only for read-only mounts | Planned (step 2) |
| Symlinks in served paths | `openat2(RESOLVE_NO_SYMLINKS)` for parents; `O_NOFOLLOW` per component | `O_NOFOLLOW_ANY` for parents and reopen; `O_NOFOLLOW` or `O_SYMLINK` per component; mount sources go through `realpath` | Done (server); Planned (`realpath` in launcher) |
| System call filter | seccomp-bpf (`runsc/gofer/filter`) | The Seatbelt profile denies process exec, network (except host Unix sockets if allowed) and IPC | Planned (step 2) |
| Capabilities | Dropped (`--apply-caps`) | No capabilities | No equivalent |
| FD limit | `RLIMIT_NOFILE` from `--fdlimit` | `setrlimit`, the same | Planned (step 1) |
| Supplementary groups | Dropped, or set per thread for host socket connections | Not changed; connecting to a host socket as another user is not supported (darwin has no per-thread credentials) | No equivalent |

## Networking

| Layer | runsc on Linux | macOS port | Status |
|---|---|---|---|
| Default network | netstack in the Sentry with an `fdbased` link endpoint in a network namespace (veth) | netstack with a user-space proxy or vmnet | Later (Phase 2) |
| Host network | `--network=host` (hostinet) | Not planned | Later |
| None | `--network=none`: loopback only | Loopback only (the current state) | Done |

## Known differences that remain

-   Seatbelt restricts access by path rules; it is not a separate view of the
    file system. A confined process can still learn that a denied path
    exists, if the profile allows `stat` of the parents of its mount paths.
-   Apple marks `sandbox_init` deprecated. It works on macOS 26 and is used by
    Chrome and Bazel, but Apple does not document the profile language.
-   No cgroups: the Sentry and gofer have no CPU, memory or PID limits beyond
    `setrlimit`.
-   No user separation without root: the Sentry and gofer run as the calling
    user.
-   The darwin gofer cannot serve files that the user cannot read (no
    `O_PATH`), Unix sockets, or operations on deleted files (see "Gofer" in
    `darwin-build.md`).

## Order of work

1.  `hvfboot gofer` and `hvfboot -v host:guest`: one gofer process, one
    socket pair per mount in mount order, `realpath` of mount sources, FD
    limit.
2.  Seatbelt profile for the gofer.
3.  Seatbelt profile for the Sentry, in place of `pivot_root`.

None of these steps needs root.
