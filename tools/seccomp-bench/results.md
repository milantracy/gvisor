## Environment

- kernel: 6.17.0-1010-gcp
- go: go1.27.1 linux/amd64, GOMAXPROCS=1
- libseccomp: 2.5.5 (runc libcontainer)
- profile: 18 rules, 383 syscall names, arches [SCMP_ARCH_X86_64 SCMP_ARCH_X86 SCMP_ARCH_X32], default SCMP_ACT_ERRNO

## Filter decisions and BPF instructions executed

Each cell: action / instructions executed, from gVisor's instrumented BPF interpreter.

| input | gvisor | elastic | runc |
|---|---|---|---|
| getpid | ALLOW / 8 | ALLOW / 86 | ALLOW / 22 |
| read | ALLOW / 8 | ALLOW / 194 | ALLOW / 19 |
| write | ALLOW / 8 | ALLOW / 307 | ALLOW / 18 |
| futex | ALLOW / 14 | ALLOW / 68 | ALLOW / 23 |
| personality(0) | ALLOW / 11 | ALLOW / 320 | ALLOW / 31 |
| personality(1) [deny] | ERRNO(EPERM) / 15 | ERRNO(EPERM) / 332 | ERRNO(EPERM) / 31 |
| socket(AF_UNIX) | ALLOW / 11 | ALLOW / 314 | ALLOW / 34 |
| socket(AF_VSOCK) [deny] | ERRNO(EPERM) / 23 | ERRNO(EPERM) / 333 | ERRNO(EPERM) / 34 |
| clone(SIGCHLD) | ALLOW / 8 | ALLOW / 331 | ALLOW / 28 |
| clone(CLONE_NEWUSER) [deny] | ERRNO(EPERM) / 8 | ERRNO(EPERM) / 332 | ERRNO(EPERM) / 28 |
| clone3 [ENOSYS] | ERRNO(EPERM) / 20 | ERRNO(ENOSYS) / 328 | ERRNO(ENOSYS) / 22 |
| acct [deny] | ERRNO(EPERM) / 18 | ERRNO(EPERM) / 328 | ERRNO(EPERM) / 24 |
| nr 1000 [unknown] | ERRNO(EPERM) / 22 | ERRNO(EPERM) / 328 | ERRNO(ENOSYS) / 7 |
| i386 getpid | KILL_THREAD / 3 | ERRNO(EPERM) / 4 | ALLOW / 22 |
| x32 getpid | ERRNO(EPERM) / 22 | ERRNO(ENOSYS) / 5 | ALLOW / 23 |

Instructions executed over the 309 unconditionally allowed x86_64 syscalls (the cost without the kernel action cache):

| variant | mean | median | max | not allowed |
|---|---|---|---|---|
| gvisor | 14.3 | 15 | 22 | 23 |
| elastic | 160.2 | 160 | 326 | 0 |
| runc | 21.4 | 22 | 24 | 11 |

- gvisor does not allow: [cachestat:ERRNO(EPERM) fchmodat2:ERRNO(EPERM) futex_requeue:ERRNO(EPERM) futex_wait:ERRNO(EPERM) futex_waitv:ERRNO(EPERM) futex_wake:ERRNO(EPERM) getxattrat:ERRNO(EPERM) landlock_add_rule:ERRNO(EPERM) landlock_create_ruleset:ERRNO(EPERM) landlock_restrict_self:ERRNO(EPERM) listmount:ERRNO(EPERM) listxattrat:ERRNO(EPERM) lsm_get_self_attr:ERRNO(EPERM) lsm_list_modules:ERRNO(EPERM) lsm_set_self_attr:ERRNO(EPERM) memfd_secret:ERRNO(EPERM) map_shadow_stack:ERRNO(EPERM) mseal:ERRNO(EPERM) process_mrelease:ERRNO(EPERM) removexattrat:ERRNO(EPERM) setxattrat:ERRNO(EPERM) statmount:ERRNO(EPERM) uretprobe:ERRNO(EPERM)]
- runc does not allow: [getxattrat:ERRNO(ENOSYS) listmount:ERRNO(ENOSYS) listxattrat:ERRNO(ENOSYS) lsm_get_self_attr:ERRNO(ENOSYS) lsm_list_modules:ERRNO(ENOSYS) lsm_set_self_attr:ERRNO(ENOSYS) mseal:ERRNO(ENOSYS) removexattrat:ERRNO(ENOSYS) setxattrat:ERRNO(ENOSYS) statmount:ERRNO(ENOSYS) uretprobe:ERRNO(EPERM)]

## Filter build (no install), 50 builds

| variant | instructions | median build | min build |
|---|---|---|---|
| gvisor | 168 | 24.781ms | 23.14ms |
| elastic | 402 | 224µs | 177µs |
| runc | 1269 | 31.663ms | 26.165ms |

## Syscall cost with filter installed

10 processes per variant, 5 rounds of 1000000 calls each. Median (min) of per-round ns/op.

| variant | setup (build+install) | getpid | personality(0) | acct(NULL) |
|---|---|---|---|---|
| none | 1µs | 848.0 (834.6) | 829.2 (816.7) | 848.8 (837.8) |
| gvisor | 25.616ms | 880.2 (867.8) | 927.8 (913.1) | 920.0 (908.1) |
| elastic | 1.133ms | 882.0 (874.5) | 1044.5 (1035.9) | 1037.9 (1026.1) |
| runc | 27.899ms | 880.4 (868.0) | 934.9 (923.1) | 922.9 (909.6) |

