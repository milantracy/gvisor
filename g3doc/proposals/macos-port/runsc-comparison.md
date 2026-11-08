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
| Launcher | `runsc` (OCI runtime) creates the sandbox and the gofer, and passes FDs to them (`runsc/container/container.go`, `runsc/sandbox/sandbox.go`) | `runsc`, the same code; darwin files start the processes without namespaces, cgroups or user namespaces. `hvfboot` (`runsc/hvfboot`) is the earlier launcher for one program | Done |
| Sentry process | `runsc boot`, one per sandbox, from the `gvisor_sentry` sidecar; it runs `boot.Loader` (`runsc/boot/loader.go`) | The same: `gvisor-bin/gvisor_sentry boot`, signed with the hypervisor entitlement. With `--attached` it watches its parent with kqueue (no `PR_SET_PDEATHSIG`) | Done |
| Gofer process | `runsc gofer`, one per container, serves all its mounts (`container.go`: "One gofer process serves all of a container's mounts") | `runsc gofer`, the same; it serves the real host paths and has no parent death signal | Done |
| Application isolation from the Sentry | Platform: systrap, ptrace or KVM | `hvf` platform on Hypervisor.framework; the application runs at EL0 in a VM, the Sentry on the host | Done |

## File systems

| Layer | runsc on Linux | macOS port | Status |
|---|---|---|---|
| Sentry-to-gofer connection | One `SOCK_STREAM` socket pair per lisafs mount (`createLisafsSocketPair`); the gofer gets `--io-fds`, the sandbox the other ends | The same | Done |
| Mount order | `--gofer-mount-confs` and `--io-fds`: root first, then the spec mounts in order | The same | Done |
| lisafs server | `runsc/gofer/fs` (`lisafs.go`); `Gofer.serve` makes one `lisafs.Server` and one connection per io FD | The same package; darwin host calls in `lisafs_darwin.go` | Done |
| lisafs wire format | Linux flags and Linux errnos | The same; the darwin server translates flags and errnos | Done |
| Host file access from the Sentry | directfs: the gofer donates host FDs and the Sentry makes file system calls on them | lisafs only; directfs uses `O_PATH`, `statx`, `AT_EMPTY_PATH` and fails with `EINVAL` if asked for | Done (lisafs only) |
| FDs given to a running Sentry (`runsc exec` stdio) | Sent with `SCM_RIGHTS`; the Sentry imports any FD. seccomp allows `fstat` on any FD and `statx`/`newfstatat` only with `AT_EMPTY_PATH`, and the Sentry has no host paths after `pivot_root`. An FD is a capability; a path gives nothing | Seatbelt checks `fstat` of a write-only FD against the path of its file, and the profile allows only the paths open at confinement. `runsc exec` replaces each write-only stdio file that has a path with a pipe and copies from it. Pipes, sockets, ttys and FDs open for reading pass unchanged. The application sees a pipe, so `lseek` and `ftruncate` on it fail | Done |
| Root file system | Gofer-served rootfs with an overlay, or an EROFS image opened by the Sentry (no gofer for `/`) | The same | Done |
| Bind mounts | Spec mounts, served by the gofer | The same | Done |
| `/dev` | devtmpfs in the Sentry, optional `/dev` gofer (`--dev-io-fd`) | devtmpfs and memory devices in the Sentry; no `/dev` gofer | Done (devtmpfs) |
| Terminals | `process.terminal` with `--console-socket`: `runsc` creates the pty, sends the master on the socket and makes the replica the controlling TTY of the sandbox; `fsimpl/host` passes the termios `ioctl`s of host TTYs to the host unchanged | The same in `runsc`; `fsimpl/host` translates the termios layout, flags, control characters, speeds and requests (`TIOCGETA`, `TIOCSETA{,W,F}`) to darwin. Seatbelt does not restrict TTY `ioctl`s | Done |

## Confinement of the Sentry

| Layer | runsc on Linux | macOS port | Status |
|---|---|---|---|
| File system view | New mount namespace; `pivot_root` into a read-only tmpfs with `/etc/localtime` and a minimal procfs (`runsc/cmd/sentry/sentrycmd/chroot.go`, `setUpChroot`). The Sentry keeps only FDs that it opened before | No mount namespace, `pivot_root` or bind mounts; `chroot(2)` needs root. A Seatbelt (`sandbox_init`) profile denies all file access; the Sentry opens the EROFS image, gofer sockets and `/etc/localtime` first, and those FDs keep working. The profile allows only `fstat` (`file-read-metadata`) of the standard I/O files and under the mount sources, because Seatbelt checks `fstat` of FDs that are not open for reading | Done |
| System call filter | seccomp-bpf (`runsc/boot/filter`, installed in `loader.go`) | No seccomp. `filter.Install` applies the Seatbelt profile at the same point in `loader.go`. The profile denies process exec, network and IPC; it filters by operation and path, not by system call | Done |
| Namespaces | New IPC, mount, UTS, PID and (if asked) network and user namespaces (`sandbox.go`, the `nss` list) | No namespaces on macOS. Seatbelt covers file, network, IPC and exec access | No equivalent |
| User | Optional user namespace with UID/GID mappings; can run as nobody | Runs as the calling user. A separate user needs root to set up | Later |
| Sandbox IDs on host files | Rootless: the user namespace maps sandbox root to the user; other IDs are unmapped | The darwin gofer does the same mapping itself (`MapRootToEffectiveIDs`): 0 is the user, other host IDs are 65534, `chown` to other IDs fails | Done |
| Capabilities | Dropped to a minimal set | macOS has no capabilities | No equivalent |
| Resource limits | cgroups (CPU, memory, PIDs) | No cgroups. `setrlimit` only, per process | Later |

## Confinement of the gofer

| Layer | runsc on Linux | macOS port | Status |
|---|---|---|---|
| File system view | New mount namespace; the rootfs and each mount source are bind-mounted under a tmpfs at `/proc/fs`, then `chroot("/root")` (`runsc/cmd/sandboxsetup/gofer_mount.go`, `runsc/cmd/gofer.go`) | A Seatbelt profile allows file access only under the real path (`subpath`) of each mount source, read-only for read-only mounts, plus a private `TMPDIR` for lisafs channel memory | Done |
| Symlinks in served paths | `openat2(RESOLVE_NO_SYMLINKS)` for parents; `O_NOFOLLOW` per component | `O_NOFOLLOW_ANY` for parents and reopen; `O_NOFOLLOW` or `O_SYMLINK` per component; mount sources go through `realpath` | Done |
| System call filter | seccomp-bpf (`runsc/gofer/filter`) | The Seatbelt profile denies process exec, network and IPC (`deny default`); host Unix sockets are not supported | Done |
| Capabilities | Dropped (`--apply-caps`) | No capabilities | No equivalent |
| FD limit | `RLIMIT_NOFILE` from `--fdlimit` | `setrlimit`, the same (`-fdlimit`) | Done |
| Supplementary groups | Dropped, or set per thread for host socket connections | Not changed; connecting to a host socket as another user is not supported (darwin has no per-thread credentials) | No equivalent |

## Networking

| Layer | runsc on Linux | macOS port | Status |
|---|---|---|---|
| Default network | netstack in the Sentry; its only link is an `AF_PACKET` socket on a veth in the network namespace of the sandbox (`fdbased`); the host kernel routes and NATs | netstack in the Sentry; its only link is the `fdbased` link of a `runsc` external network proxy (`IsProxy`), over a `SOCK_DGRAM` socket pair (darwin has no Unix `SOCK_SEQPACKET`) to `runsc-netgofer`, a proxy process that `runsc start` starts and that ends TCP and UDP flows in its own netstack and makes host connections | Done (IPv4 TCP and UDP) |
| DNS | The `resolv.conf` of the container | Port 53 to any address goes to the host DNS server | Done |
| Confinement of the network path | The host kernel, with namespaces | The proxy has a Seatbelt profile: outbound IP only, not to the host itself; no files, exec or IPC | Done |
| New network namespaces | Loopback only | Loopback only | Done |
| ICMP, IPv6, inbound ports | Supported | Not supported | Later |
| Host network | `--network=host` (hostinet) | Not planned | Later |
| None | `--network=none`: loopback only | The same | Done |

## Known differences that remain

-   `runsc exec` stdio redirected to a file reaches the application as a
    pipe (see "FDs given to a running Sentry"). This is a decision
    (2026-11-07): the other option, `file-read-metadata` for all paths in
    the Sentry profile, gives the exact Linux behavior but lets a
    compromised Sentry read the metadata of any host path, which Linux
    does not allow.
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

1.  Done: `hvfboot gofer` and `hvfboot -v host:guest`: one gofer process,
    one socket pair per mount in mount order, `realpath` of mount sources,
    FD limit.
2.  Done: Seatbelt profile for the gofer (`//pkg/seatbelt`).
3.  Done: Seatbelt profile for the Sentry, in place of `pivot_root`.
4.  Done: networking through a proxy process with its own Seatbelt profile.
5.  Done: `runsc/boot` builds on darwin; `hvfboot` runs the Sentry with
    `boot.Loader`.
6.  Done: `runsc boot` and `runsc gofer` on darwin, with the Seatbelt
    profiles in place of `filter.Install`, namespaces and `pivot_root`.
7.  Done: `runsc/sandbox` and `runsc/container` on darwin, `runsc-netgofer`,
    and the OCI commands.

None of these steps needs root.
