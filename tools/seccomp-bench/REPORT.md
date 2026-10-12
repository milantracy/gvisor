# Seccomp filter generators: gVisor vs elastic vs libseccomp (runc)

All three generators compile the containerd default OCI seccomp profile
(`contrib/seccomp.DefaultProfile`, default capabilities; 383 syscall names) into
a classic BPF filter:

- **gvisor**: `runsc/specutils/seccomp.BuildProgram` on top of `pkg/seccomp`,
  unchanged.
- **elastic**: `github.com/elastic/go-seccomp-bpf` v1.6.0, with a small
  OCI-to-Policy converter (`variants.go`).
- **runc**: runc v1.5.2 `libcontainer/seccomp.InitSeccomp` (libseccomp 2.5.5
  via cgo, binary tree optimization, `patchbpf` ENOSYS stub).

Full output: [results.md](results.md).

## Environment

GCP VM, Intel Xeon @ 2.20GHz, Linux 6.17, Go 1.27.1, benchmark pinned to one
CPU. PTI and IBRS mitigations are on, so a bare syscall costs about 850 ns.
Compare the deltas against `none`, not the absolute values.

## Results

Syscall cost, ns/op (median over 10 processes x 5 rounds x 1M calls):

| variant | getpid | personality(0) | acct(NULL), denied | setup (build+install) |
|---|---|---|---|---|
| none | 848 | 829 | 849 | - |
| gvisor | 880 (+32) | 928 (+99) | 920 (+71) | 25.6 ms |
| elastic | 882 (+34) | 1045 (+215) | 1038 (+189) | 1.1 ms |
| runc | 880 (+32) | 935 (+106) | 923 (+74) | 27.9 ms |

BPF instructions executed (gVisor's instrumented interpreter):

| variant | program size | allowed syscalls without arg checks (mean / max) | personality(0) | acct |
|---|---|---|---|---|
| gvisor | 168 | 14.3 / 22 | 11 | 18 |
| elastic | 402 | 160.2 / 326 | 320 | 328 |
| runc | 1269 | 21.4 / 24 | 31 | 24 |

## Findings

1. **The kernel action cache hides the filter for most syscalls.** On Linux
   5.11+, syscalls that are allowed without argument checks skip the filter.
   `getpid` costs the same +32 ns with all three generators.
2. **For syscalls that do run the filter, gvisor and runc are about equal.**
   gvisor executes fewer instructions (11 vs 31), but it measures only about
   7 ns faster. A fixed cost of about 70-100 ns to run any filter dominates.
3. **elastic's straight list of rules costs more.** Rules near the end of the
   profile execute over 300 instructions, which adds about 110 ns compared to
   gvisor and runc. Without the action cache (kernels before 5.11), every
   allowed syscall pays an average of 160 instructions.
4. **Build time:** gvisor and runc both take about 25 ms (optimizer or
   libseccomp). elastic takes under 1 ms. This cost happens once per
   container start.

## Behavior differences (not performance)

- **gvisor** checks only native x86_64 and kills i386 syscalls with
  KILL_THREAD. It ignores `errnoRet`, so `clone3` returns EPERM instead of
  ENOSYS (glibc falls back to `clone` only on ENOSYS). Syscalls that are not
  in the Sentry syscall table are dropped and return EPERM. That affects 23
  syscalls, for example `futex_waitv`, `landlock_*`, `mseal` and
  `map_shadow_stack`. Unknown syscalls return EPERM, not ENOSYS.
- **elastic** checks only native x86_64; it denies i386 and x32 syscalls. Its
  `Policy` rejects action data on the default action, so the ERRNO(EPERM)
  default needs a patch after assembly.
- **runc** allows x86_64, i386 and x32, as the profile lists. Syscalls newer
  than libseccomp 2.5.5 return ENOSYS through `patchbpf` (10 of them, for
  example `mseal` and `listmount`).

## Conclusion

Performance is not a reason to replace libseccomp in runc with gVisor
`pkg/seccomp`: on modern kernels the measured difference is about 7 ns per
filtered syscall. To replace libseccomp, `pkg/seccomp` would first need
multi-arch filters, `errnoRet`, NOTIFY and its listener fd, more flags, and
its own syscall tables. runc also keeps cgo for `nsenter`.

## Reproduce

```sh
go build -tags seccomp -o /tmp/seccomp-bench .   # needs libseccomp-dev
taskset -c 3 /tmp/seccomp-bench -reps 10 -iters 1000000 -rounds 5 -builds 50
```

Run as non-root. Without `-tags seccomp`, the runc variant is left out.
