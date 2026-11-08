![gVisor](g3doc/logo.png)

# gVisor on macOS (experimental)

**This is an experimental branch.** It runs `runsc`, the gVisor OCI runtime,
natively on macOS on Apple Silicon. It is a prototype: it is not part of a
gVisor release, it is not reviewed upstream, and it is not supported. Do not
use it to run untrusted code yet.

For gVisor itself, see the [upstream repository](https://github.com/google/gvisor)
and [gvisor.dev](https://gvisor.dev).

## How it works

`runsc` on macOS has the same process model as on Linux:

-   The Sentry (the gVisor kernel) runs in its own process. It uses the new
    `hvf` platform: the application runs at EL0 in a virtual machine of
    Hypervisor.framework, and the Sentry handles its system calls on the host.
-   The gofer serves the root filesystem and the bind mounts of the container
    to the Sentry, over lisafs.
-   `runsc-netgofer` is the network proxy for `--network=sandbox`.
-   Seatbelt (`sandbox_init`) profiles confine the Sentry, the gofer and the
    network proxy, in place of seccomp, namespaces and `pivot_root`.

`runsc` needs no root and no developer account. The Sentry binary is signed
at build time with the `com.apple.security.hypervisor` entitlement.

## Requirements

-   A Mac with Apple Silicon. Tested on macOS 26.6.
-   [Bazelisk](https://github.com/bazelbuild/bazelisk):
    `brew install bazelisk`.
-   Docker, only to get container images. Images must be `arm64`, which is
    the default on Apple Silicon.

## Quick start

This follows the
[OCI quick start](https://gvisor.dev/docs/user_guide/quick_start/oci/) of
gVisor. The differences on macOS: no `sudo`, and `--root` must be a
directory that you can write.

### Build `runsc`

`runsc` looks for the Sentry and the network proxy in `gvisor-bin/` next to
itself. In the root of this repository:

```bash
bazelisk build //runsc:runsc //runsc/cmd/sentry:gvisor_sentry_hvf //runsc/cmd/netgofer:runsc-netgofer

mkdir -p ~/runsc-mac/gvisor-bin
for f in $(bazelisk cquery --output=files '//runsc:runsc + //runsc/cmd/sentry:gvisor_sentry_hvf + //runsc/cmd/netgofer:runsc-netgofer' 2>/dev/null); do
  case $f in
    */runsc) cp -f "$f" ~/runsc-mac/ ;;
    *)       cp -f "$f" ~/runsc-mac/gvisor-bin/ ;;
  esac
done
chmod -R u+w ~/runsc-mac
export PATH=~/runsc-mac:$PATH
```

### Run an OCI container

Make a bundle with the root filesystem of the `hello-world` image:

```bash
mkdir bundle
cd bundle
mkdir -m 0755 rootfs
docker export $(docker create hello-world) | tar -xf - -C rootfs
```

Make an OCI spec that runs `/hello`, and run the container:

```bash
runsc spec -- /hello
runsc --root=$HOME/.runsc run hello
```

You see "Hello from Docker!".

### Run `dmesg`

`hello-world` has only `/hello`. For `dmesg`, use the `busybox` image:

```bash
cd ..
mkdir -p busybox/rootfs
cd busybox
docker export $(docker create busybox) | tar -xf - -C rootfs
runsc spec -- dmesg
runsc --root=$HOME/.runsc run busybox
```

The output is the gVisor kernel log:

```
[   0.000000] Starting gVisor...
[   0.335682] Daemonizing children...
...
[   2.622120] Ready!
```

### Run a shell

```bash
rm config.json
runsc spec -- sh
runsc --root=$HOME/.runsc run shell
```

The shell is interactive (line editing, window size, Ctrl-C). Try `uname -a`,
`ps` and `wget -qO- http://example.com`. `exit 3` makes `runsc` exit with
status 3.

### The container lifecycle

The other OCI commands work as on Linux. Make the container run a long
process, and then manage it:

```bash
rm config.json
runsc spec -- sh -c 'while true; do sleep 1; done'

runsc --root=$HOME/.runsc create --bundle . c1
runsc --root=$HOME/.runsc start c1
runsc --root=$HOME/.runsc list
runsc --root=$HOME/.runsc ps c1
runsc --root=$HOME/.runsc exec c1 sh -c 'echo hi; exit 7'; echo $?   # 7
runsc --root=$HOME/.runsc exec c1 sh          # interactive shell in the sandbox
runsc --root=$HOME/.runsc wait c1 &           # prints {"id":"c1","exitStatus":137} after the kill
runsc --root=$HOME/.runsc kill c1 KILL
runsc --root=$HOME/.runsc delete c1
```

As on Linux, start `wait` before the container exits: when the sandbox is
gone, `wait` cannot get its exit status.

## Defaults and flags

Three flags have other defaults on macOS, the only values that work there:
`--platform=hvf`, `--directfs=false` and `--ignore-cgroups=true`.

-   `--debug --debug-log=/tmp/runsc/` writes one log for each process
    (`runsc`, Sentry, gofer, network proxy).
-   `--network=none` gives the container only a loopback interface.
    `--network=sandbox` (the default) gives it TCP, UDP and DNS through
    `runsc-netgofer`.

## Not supported on macOS yet

-   Checkpoint and restore.
-   `--network=host` and `--network-proxy-path`.
-   cgroups and resource limits.
-   Device gofers, ID-mapped mounts, `nvproxy`, TPU and RDMA.
-   `amd64` images.
-   The default `--root` (`/var/run/runsc`) is not writable without root.
-   In the guest, `tty` prints "not a tty" on a terminal.

## License

Apache 2.0. See [LICENSE](LICENSE).
