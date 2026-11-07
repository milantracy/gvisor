# Hypervisor.framework probes

These programs measure Hypervisor.framework (HVF) behavior that the design of
a gVisor platform on macOS depends on. They are research tools; gVisor does not
use them.

The results, the test machine, and the conclusions are in
[`phase0-results.md`](https://github.com/milantracy/gvisor/blob/macos-port/research/g3doc/proposals/macos-port/phase0-results.md)
on the `macos-port/research` branch. This branch has only the sources.

## Run

macOS on Apple Silicon only. Needs the Xcode Command Line Tools. Ad-hoc signing
with the `com.apple.security.hypervisor` entitlement is enough; no developer
account and no root are needed.

```
cd tools/hvf-probe
make                  # build and sign every probe into out/
make run-vcpu_run     # build, sign and run one probe
make run              # run every probe
```

Some probes have modes; `make run-<probe>` runs each mode in a new process
(see `MODES_*` in the `Makefile`). To run one mode: `out/vm_map eof`.

## Probes

| # | Probe | Question |
|---|---|---|
| 1 | `vcpu_run` | What does an `hv_vcpu_run` round trip (guest `hvc`, host exit, re-entry) cost? |
| 2 | `vcpu_lifecycle` | How many vCPUs can a VM have, what do create and destroy cost, and what happens to vCPUs that are not destroyed? Modes: `cost`, `leak`, `leak-one`. |
| 3 | `vcpu_cancel` | How does `hv_vcpus_exit` behave for a running guest and for a vCPU that is not running, and how fast is it? |
| 4 | `vcpu_signal` | What happens when a host signal arrives on a thread that runs a vCPU? |
| 5 | `vm_map` | Which host memory can `hv_vm_map` map, and does stage 2 follow later host `mmap`, `munmap` and truncate? Modes: `main`, `eof`, `truncate`, `munmap`, `reserve`. |
| 6 | `mem_release` | How can memory that is mapped into the guest be released, and how is it accounted? Modes: `anon`, `file`, `shm`, `acct`, `cost`. |
| 7 | `tlb_bounce` | What does a TLB shootdown cost: kick N vCPUs, run TLBI on re-entry, wait for all N? |
| 8 | `handoff` | What does a spin-then-wait hand-off between two host threads cost? No HVF calls. |
| 9 | `fp_trap` | What does a lazy-FP trap (`CPACR_EL1.FPEN`) taken as a host exit cost? |
| 10 | `sanity` | Which stage-1 granules, IPA size and IPA granule does HVF support? |

`probe.c`, `probe.h`, `vectors.S` and `guest_loops.S` are shared: VM setup,
timing, and the guest code.

## KVM counterpart

`kvm/` has the KVM version of probe 1, to compare HVF with KVM on an arm64
Linux host. It needs read and write access to `/dev/kvm`.

```
cd tools/hvf-probe/kvm
make run              # CPU=n pins the probe to CPU n
```
