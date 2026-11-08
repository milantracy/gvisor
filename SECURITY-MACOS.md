# Security model of gVisor on macOS (experimental)

This document compares the security of this experimental macOS branch with
gVisor on Linux. Read it before you use the branch.

## Summary

-   **Do not use this branch to run untrusted code.**
-   The first layer of protection is the same as on Linux. The application
    cannot use the host kernel. It can only use the gVisor kernel (the
    Sentry).
-   The layers after the first layer are weaker than on Linux. On Linux, an
    attacker who controls the Sentry must also defeat seccomp, namespaces and
    an unprivileged user. On macOS, the attacker must defeat only one
    Seatbelt profile, and the Sentry runs as your user.
-   Nobody has reviewed this code for security. Nobody has fuzzed it.

## Words in this document

| Word | Meaning |
|---|---|
| Application | The program in the container. gVisor does not trust it. |
| Sentry | The gVisor kernel. It runs the system calls of the application. |
| Gofer | A process that gives the Sentry access to the files of the container. |
| Network proxy | On macOS, the `runsc-netgofer` process. It connects the network of the container to the host network. |
| Host | The computer that runs gVisor: Linux, or macOS. |
| Host kernel | The kernel of the host: Linux, or XNU on macOS. |
| seccomp | A Linux feature. It limits the system calls that a process can make. |
| Namespaces | A Linux feature. It gives a process its own, empty view of files, processes and network. |
| Seatbelt | A macOS feature (`sandbox_init`). It limits the operations that a process can do, for example file access or network access. |
| Hypervisor.framework | A macOS feature. It runs code in a virtual machine (VM). |
| Escape | An attack that gets more access to the host than gVisor gives. |

## The layers of protection

gVisor uses layers. To escape, an attacker must defeat each layer. The
table compares each layer on Linux and on macOS.

| Layer | Linux | macOS | Result |
|---|---|---|---|
| 1. Application to host kernel | The application cannot make system calls to the host kernel. The platform (systrap or KVM) sends each system call to the Sentry. | The application runs in a Hypervisor.framework VM. Each system call stops the VM and goes to the Sentry. The memory of the Sentry is not in the VM. | **Same** |
| 2. The Sentry | The Sentry is written in Go, a memory-safe language. It does not give the application access to the host kernel. | The same Sentry code. | **Same** |
| 3. Sentry to host kernel | seccomp lets the Sentry use only about 70 system calls, many with limits on their arguments. Most of the host kernel is not available to the Sentry. | Seatbelt stops operations, not system calls. The Sentry can still make every XNU system call and every Mach call. Seatbelt stops many of these calls after the host kernel starts to process them. | **Weaker** |
| 4. Sentry user and namespaces | The Sentry runs as an unprivileged user, in empty namespaces, with no capabilities, and with an empty root directory. | The Sentry runs as your user. There are no namespaces. Only Seatbelt stops it. | **Weaker** |
| 5. Files | A separate gofer process gives access to files. It has its own seccomp filter. It sees only the files of the container. | A separate gofer process gives access to files. Its Seatbelt profile lets it use only the directories of the container, and a private temporary directory. It runs as your user. | **Almost the same** |
| 6. Network | The network stack is in the Sentry. The host side is in its own network namespace. | The network stack is in the Sentry. The Sentry has no network access. The network proxy makes the connections. Its Seatbelt profile lets it make only outgoing connections. It cannot connect to the addresses of the Mac (`localhost` and the addresses of the network interfaces). | **Almost the same** |
| 7. Resource limits | cgroups limit memory, CPU and the number of processes. | There are no limits. A container can use all of the memory and CPU of the Mac. | **Weaker** |
| 8. Review and tests | Many years of security review, fuzzing and tests. | New code. No security review. No fuzzing. Only 60 of the gVisor system call test binaries run on macOS (see [Compatibility](COMPATIBILITY-MACOS.md#system-call-tests)). | **Weaker** |

## What an attacker must do

On Linux:

1.  Find a bug in the Sentry, and use it to control the Sentry.
2.  Find a bug in one of the few host system calls that seccomp allows.
3.  Defeat the namespaces and the unprivileged user.

On macOS:

1.  Find a bug in the Sentry, and use it to control the Sentry. This is as
    difficult as on Linux.
2.  Find a bug in XNU or in Seatbelt. All of XNU is available to the
    Sentry, not only a few system calls.
3.  After step 2, the attacker has the access of your user account: your
    files, your keys and your network.

## Processes and their limits on macOS

All of the processes run as your user. None of them needs root.

| Process | Seatbelt profile |
|---|---|
| Sentry (`gvisor-bin/gvisor_sentry`) | Denies all operations by default. Allows only reading the metadata (for example the size) of the files that it has open and of the directories that the gofer serves. It cannot open files by path, and it has no network access. |
| Gofer (`runsc gofer`) | Denies all operations by default. Allows reading the directories of the container, and writing the read-write mounts. Allows a private temporary directory. |
| Network proxy (`gvisor-bin/runsc-netgofer`) | Denies all operations by default. Allows only outgoing IP connections, but not to the addresses of the Mac. |
| `runsc` | Not confined. It runs only the commands that you give it. It does not run the application. |

## Known limits of Seatbelt

-   Seatbelt does not check all operations. For example, it does not check
    terminal `ioctl` calls, also when the profile denies all operations by
    default.
-   Seatbelt checks some operations by path. For a file that is open only
    for writing, `fstat` is checked against the path of the file. Because of
    this, `runsc exec` sends output that goes to a file through a pipe.
-   Apple does not document the Seatbelt profile language. It can change in
    a new version of macOS.

## Other differences

-   The Sentry binary has the `com.apple.security.hypervisor` entitlement.
    It needs this entitlement to make VMs. A Sentry that an attacker controls
    can also make VMs. This does not give access to more of the host.
-   macOS can ask for permission (TCC) when the gofer opens files in
    `~/Desktop`, `~/Documents` or `~/Downloads`. If you do not give
    permission, the container cannot use these files.
-   Checkpoint and restore, host network (`--network=host`) and device
    access are not available on macOS. They are not part of the security
    model.

## Possible improvements

These items can make the macOS layers stronger. They are not done.

1.  **Limit the system calls of the Sentry.** The Seatbelt profiles of
    Apple use the operations `syscall-unix`, `syscall-mach` and
    `syscall-mig` to allow only some system calls. If a profile from
    `sandbox_init` can use these operations, the Sentry can have an
    allow-list like seccomp on Linux. This is not tested yet.
2.  **Run the Sentry and the gofer as a different user.** This needs a new
    user account on the Mac, which needs administrator access one time.
    Then an escape does not get the access of your user account.
3.  **Limit resources.** The Sentry can limit the memory that the
    application uses. `setrlimit` can limit the number of files and
    processes.
4.  **Review and test.** Run all of the gVisor system call tests on macOS,
    not only 60 binaries. Fuzz
    the code that handles VM exits and VM memory. Do a security review of
    the Hypervisor.framework platform before it goes into gVisor.

## How to report a problem

This branch is not part of gVisor. Do not send problems with this branch to
the gVisor security team. Open an issue in
[milantracy/gvisor](https://github.com/milantracy/gvisor) instead. For
problems with gVisor itself, see [SECURITY.md](SECURITY.md).
