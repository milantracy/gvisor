# Seccomp filter generators: gVisor, elastic and libseccomp (runc)

Each generator compiles the containerd default OCI profile (383 syscalls)
into a BPF filter:

- **gvisor**: `runsc/specutils/seccomp.BuildProgram`, without changes.
- **elastic**: `go-seccomp-bpf` v1.6.0, with a converter in `variants.go`.
- **runc**: runc v1.5.2 `InitSeccomp`, libseccomp 2.5.5.

Full output: [results.md](results.md).

## Results

GCP VM, Xeon 2.20GHz, Linux 6.17, one CPU. PTI and IBRS make a bare syscall
approximately 850 ns. Compare with the `none` row.

| variant | getpid | personality(0) | acct (denied) | build+install |
|---|---|---|---|---|
| none | 848 ns | 829 ns | 849 ns | - |
| gvisor | +32 | +99 | +71 | 25.6 ms |
| elastic | +34 | +215 | +189 | 1.1 ms |
| runc | +32 | +106 | +74 | 27.9 ms |

| variant | program size | instructions executed, allowed syscalls (mean / max) | personality(0) |
|---|---|---|---|
| gvisor | 168 | 14.3 / 22 | 11 |
| elastic | 402 | 160.2 / 326 | 320 |
| runc | 1269 (3 arches) | 21.4 / 24 | 31 |

## Findings

1. Linux 5.11+ caches syscalls that the filter always allows and does not run
   the filter for them.
   Thus, `getpid` costs the same with all generators.
2. When the filter runs, gvisor is only 7 ns faster than runc. A fixed cost
   of 70-100 ns per filter run is larger than the instruction cost.
3. elastic examines rules in sequence. This adds approximately 110 ns.
4. gvisor and runc optimize the filter. Thus, the build takes approximately
   25 ms. elastic takes less than 1 ms.

## Feature gaps: gVisor compared to libseccomp in runc

| feature | gVisor | libseccomp (runc) |
|---|---|---|
| Architectures per filter | One, set at build time. Other arches go to one action. | Many. x86_64, x86 and x32 each get a section. |
| 32-bit syscalls (i386/x32 on amd64, arm on arm64) | KILL_THREAD | Examined with their own numbers |
| Supported arches | amd64, arm64 | Also ppc64le, s390x, riscv64, loong64, mips |
| `socketcall` and `ipc` (i386, s390x) | No | Rules changed automatically |
| Syscall tables | From the Sentry. 23 profile syscalls are missing and give EPERM. | Own tables |
| KILL_PROCESS, LOG, NOTIFY | `pkg/seccomp` yes, OCI converter no | Yes |
| `errnoRet` | Ignored, always EPERM (`clone3` gives EPERM) | Yes |
| Filter flags | TSYNC | TSYNC, LOG, SPEC_ALLOW, WAIT_KILLABLE_RECV |
| Unknown syscalls | Default action (EPERM) | ENOSYS (`patchbpf`) |
| API | Internal packages, no stable Go API | Stable C API through cgo |

Architecture support is the largest gap.

## Reproduce

```sh
go build -tags seccomp -o /tmp/seccomp-bench .   # needs libseccomp-dev
taskset -c 3 /tmp/seccomp-bench -reps 10 -iters 1000000 -rounds 5 -builds 50
```

Run as non-root. Without `-tags seccomp`, the runc variant is left out.
