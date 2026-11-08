# Compatibility of gVisor on macOS (experimental)

This document lists the container images that we ran with `runsc` on macOS,
and the results. It also lists the differences from Docker that you must
know to run an image.

Read the [security model](SECURITY-MACOS.md) first. Do not run untrusted
code.

## Test setup

| Item | Value |
|---|---|
| Date | 2026-11-08 |
| Branch and commit | `macos-port/prototype`, `84b8fae50` |
| Mac | Apple M2 Max, 12 CPUs, 64 GB memory |
| macOS | 26.6.2 |
| Network | `--network=sandbox` (the default) |
| Root filesystem | `docker export` of the image, as a directory (`rootfs/`) or as a tar file (see [File owners](#file-owners)) |
| Capabilities | The default capabilities of Docker (see [Capabilities](#capabilities)) |

All images are `linux/arm64`. The client of each server test runs in the
same container, because `runsc` on macOS cannot accept connections from the
Mac yet.

## Results

| Image (digest) | Test | Result |
|---|---|---|
| `alpine:3.20` (`d9e853e87e55`) | `apk update`, `apk add curl jq git`, HTTPS request with `curl` | Pass |
| `debian:bookworm-slim` (`734a72b3d614`) | `apt-get update`, `apt-get install curl ca-certificates python3`, HTTPS request | Pass |
| `python:3.13-slim` (`09a17cad0266`) | `pip install numpy pandas requests`; numpy linear algebra, pandas, HTTPS with `requests`, `sqlite3`, `ssl`, `multiprocessing` with 4 processes | Pass |
| `python:3.13-slim` (`09a17cad0266`) | `ThreadingHTTPServer` with 32 client threads, 3000 requests | Pass. 422 requests/s (Docker Desktop on the same Mac: 955 requests/s) |
| `node:22-slim` (`5ec23b7c622b`) | `npm install express`; Express server with 200 requests, `worker_threads`, `crypto`, `zlib`, HTTPS with `fetch` | Pass |
| `golang:1.23-alpine` (`383395b794df`) | `go get` of a module, `go build`, a program with 1000 goroutines and HTTPS, `go vet`, `go test strings sort` | Pass |
| `nginx:alpine` (`713fc2ebfd92`) | Start `nginx` (workers run as user `nginx`), 200 requests with `wget` | Pass |
| `redis:alpine` (`71885d8cc6e4`) | `redis-server`, `redis-cli`, `redis-benchmark` (SET, GET, INCR, LPUSH) | Pass. About 27000 SET and 31000 GET requests/s |
| `postgres:16-alpine` (`aa6cb33cf4c1`) | `initdb`, `pg_ctl start`, SQL with 100000 rows, `pgbench` (4 clients, 10 s), `pg_ctl stop -m fast` | Pass, with the root filesystem as a tar file. About 3900 transactions/s, no failed transactions |
| `busybox` | `dmesg`, interactive `sh`, OCI lifecycle (see [README](README.md)) | Pass |
| `hello-world` | `/hello` | Pass |

## Problems found and fixed

The tests found these problems in the macOS port. All of them are fixed in
the commit above.

| Problem | Effect | Fix |
|---|---|---|
| The IPA registry called back into the page tables with its lock held. | `npm install` hung (a task waited for a lock that it held). | `fb0dad194` |
| The guest virtual counter and the host counter were different. | The vDSO clocks moved only once each second. In Go, `time.Now()` did not change, and `go test` showed `0.000s`. | `bb9a3d1bf` |
| Hypervisor.framework sometimes returns an undocumented "unknown" exit. | The task got `SIGKILL`. About one in three `initdb` runs failed, and a fast shutdown of PostgreSQL could hang. | `84b8fae50` |
| The gofer used relative paths when the bundle path was relative. | `runsc create --bundle .` failed. | `efaed680e` |

## Differences from Docker

These items are not bugs in gVisor. Docker does them for you; with `runsc`
you must do them yourself.

### DNS

`docker export` writes an empty `/etc/resolv.conf`. Docker writes this file
when the container starts. Write it yourself before you run the container:

```bash
echo "nameserver 10.0.2.2" > rootfs/etc/resolv.conf
```

The network proxy sends all DNS requests to the DNS server of the Mac, so
any address is correct.

### Capabilities

`runsc spec` gives the container only three capabilities
(`CAP_AUDIT_WRITE`, `CAP_KILL`, `CAP_NET_BIND_SERVICE`). Docker gives more.
Without them, some images fail. For example, `nginx` fails with
`chown(...) failed (1: Operation not permitted)`. To get the capabilities of
Docker, set `process.capabilities` in `config.json` to:

```
CAP_CHOWN CAP_DAC_OVERRIDE CAP_FSETID CAP_FOWNER CAP_MKNOD CAP_NET_RAW
CAP_SETGID CAP_SETUID CAP_SETFCAP CAP_SETPCAP CAP_NET_BIND_SERVICE
CAP_SYS_CHROOT CAP_KILL CAP_AUDIT_WRITE
```

### Writable root filesystem

`runsc spec` makes the root filesystem read-only (`"readonly": true`).
Docker makes it writable. Set `root.readonly` to `false` in `config.json`
for images that write files, for example `apk add` or `pip install`.

Changes go to a layer in memory and are lost when the container stops.

### File owners

On macOS, `tar` cannot keep the owners of the files when you are not root.
After `docker export | tar -x`, the files belong to you, and the container
sees them as owned by `root`. `tar` can also remove mode bits, for example
the setgid and sticky bits.

Most images work. Images that need files owned by other users do not work.
For example, PostgreSQL fails with
`could not create lock file "/var/run/postgresql/.s.PGSQL.5432.lock": Permission denied`.

To keep the owners, give the image to `runsc` as a tar file. The Sentry then
makes the files in memory, with the owners and modes of the tar file:

1.  Export the image, and change the names in the tar file to start with
    `./`, as the Sentry needs:

    ```python
    # tarfix.py IN OUT
    import sys, tarfile
    with tarfile.open(sys.argv[1], "r|") as tin, tarfile.open(sys.argv[2], "w|", format=tarfile.PAX_FORMAT) as tout:
        root = tarfile.TarInfo("./")
        root.type, root.mode = tarfile.DIRTYPE, 0o755
        tout.addfile(root)
        for m in tin:
            name = m.name.lstrip("/")
            if name in ("", "."):
                continue
            m.name = "./" + name
            if m.islnk():
                m.linkname = "./" + m.linkname.lstrip("/")
            tout.addfile(m, tin.extractfile(m) if m.isreg() else None)
    ```

    ```bash
    docker export $(docker create postgres:16-alpine) > raw.tar
    python3 tarfix.py raw.tar image.tar
    mkdir -m 0755 rootfs        # Empty.
    ```

2.  In `config.json`, add the annotation
    `"dev.gvisor.tar.rootfs.upper": "/full/path/to/image.tar"`.

3.  Run with `runsc --allow-rootfs-tar-annotation ... run`.

The Sentry keeps all of the files of the image in memory. A large image
uses as much memory as its size.

### Ports

`runsc` on macOS has no port forwarding. A server in the container cannot
get connections from the Mac. Programs in the same container can connect to
it.

## Performance

These numbers are for information only. They are not benchmarks with
statistics.

-   The Python HTTP server test was 2.3 times slower than in Docker Desktop
    on the same Mac (422 against 955 requests/s). Each system call of the
    application stops the VM, and a stop costs some microseconds.
-   Startup of `runsc do` was about 24 ms for busybox and 37 ms for Python
    (median of 20 runs, measured on 2026-11-07).

## Not tested

-   `amd64` images. They do not run on macOS: the VM can run only `arm64`
    code.
-   Images that need a GPU, `--network=host`, or checkpoint and restore.
    These features are not available on macOS.
