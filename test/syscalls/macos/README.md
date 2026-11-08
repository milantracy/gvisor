# System call tests on macOS

These scripts run the gVisor system call tests (`//test/syscalls/linux`)
with `runsc` on macOS, on the `hvf` platform. They do not use the Bazel test
runner (`//test/runner`), which runs only on Linux.

The results are in [COMPATIBILITY-MACOS.md](../../../COMPATIBILITY-MACOS.md).

## What the scripts do

-   `mkrootfs.sh OUT` builds the tests in `subset.txt` for Linux arm64, in the
    Docker build container of `make`. Then it makes `OUT/rootfs`: the
    `ubuntu:22.04` image, with the runfiles of the tests in `/runfiles`. The
    tests are linked against the glibc of the build container, which is
    also Ubuntu 22.04.
-   `run.py RUNSC ROOTFS OUTDIR TEST...` runs each test binary in its own
    sandbox, and writes `OUTDIR/results.json` and a log for each binary in
    `OUTDIR/logs`. If a binary does not finish, the script runs each case
    that has no result in its own sandbox.

The sandbox is the same as the sandbox of `//test/runner` where the
runner's options apply on macOS:

-   The same OCI spec as `testutil.NewSpecWithArgs`: all capabilities, the
    same host name and domain name, a writable root, and a `tmpfs` at `/tmp`.
-   The same `runsc` flags: `--allow-suid`, `--net-raw`,
    `--allow-packet-socket-write`, `--watchdog-action=panic`.
-   The same environment: `TEST_ON_GVISOR=hvf`, `GVISOR_NETWORK=none`, and
    the platform capabilities of the `kvm` platform in
    `GVISOR_PLATFORM_SUPPORT`.

Differences from `//test/runner`:

-   All cases of a binary run in one sandbox, not one sandbox for each case.
-   Only `--network=none`, and only the default root overlay.
-   The variants of `syscall_test` (for example `add_overlay`, `fuse`,
    `add_host_uds`) do not run.

## Run the tests

In the root of the repository, on the Mac:

```bash
test/syscalls/macos/mkrootfs.sh /tmp/sct
cd /tmp/sct
JOBS=6 ~/path/to/repo/test/syscalls/macos/run.py ~/runsc-mac/runsc rootfs out \
    $(cat ~/path/to/repo/test/syscalls/macos/subset.txt)
```

`mkrootfs.sh` needs Docker and about 1 GB of disk space.
