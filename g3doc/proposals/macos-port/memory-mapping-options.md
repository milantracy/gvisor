# HVF platform: whole-VA mapping (A) or on-demand IPA registry (B)

Status: draft recommendation. The decision belongs to the design doc owner.
All numbers come from [phase0-results.md](phase0-results.md) (Apple M2 Max,
macOS 26.6.2).

## The question

`AddressSpace.MapFile(addr, f memmap.File, ...)` accepts any `memmap.File`.
The KVM platform implements it as `f.MapInternal` → host VA →
`translateToPhysical` → `pageTables.Map`. This works because the whole Sentry
VA is guest-physical memory
([address_space.go:146](https://github.com/google/gvisor/blob/f51a205a8/pkg/sentry/platform/kvm/address_space.go#L146),
[physical_map.go:58](https://github.com/google/gvisor/blob/f51a205a8/pkg/sentry/platform/kvm/physical_map.go#L58)).
An HVF platform must pick one of two models
([feasibility-report.md](feasibility-report.md), "The `hvf` platform itself"):

-   **(A) Whole-VA.** As KVM: map the Sentry address space into the IPA space
    once, and translate host VA to IPA with a fixed function.
-   **(B) On-demand registry.** `MapFile` calls `hv_vm_map` for the backing
    range of each `memmap.File` when it is first needed. A registry allocates
    IPA, tracks mappings, and unmaps them before the host memory goes away.

## What KVM relies on and HVF does not give

KVM memslots name a host VA range. On a stage-2 fault, KVM resolves the
*current* host page at that VA, and Linux MMU notifiers remove stage-2 entries
when the host mapping changes. So the host Sentry can `mmap` and `munmap`
freely, and the guest always sees what the host sees.

HVF does not do this. Phase 0 measured:

| Fact | Source |
|---|---|
| `hv_vm_map` fails (`HV_ERROR`) if any page of the host range has no mapping. | Probe 5, finding 8 |
| A `PROT_NONE` reservation can be mapped. A later `mmap(MAP_FIXED)` over it is **not** seen by the guest; `mprotect` is. | Probe 5, finding 8 |
| After host `munmap`, the guest keeps the old pages, also after the host maps new memory at the same VA. | Probe 5, finding 3 |
| File truncate does reach stage 2 (DFSC 0x7 past the new EOF). | Probe 5, finding 4 |
| Overlapping `hv_vm_map` gives `HV_ERROR`. Partial unmap and remap of single 16 KiB pages work. | Probe 5, finding 7 |
| `O_RDONLY` + `MAP_SHARED` file mappings cannot be mapped (`HV_ERROR`). | Probe 5, finding 6 |
| IPA is 36 bits by default, 40 bits maximum. | Probe 10 |
| The process can reserve 127.6 TiB of `PROT_NONE` VA; user VA ends at 2^47. | One-off `mmap` loop, 2026-11-06; not kept as a probe |

So stage 2 holds the VM object that was at the host VA when `hv_vm_map` ran.
It does not follow the VA.

## Option (A) on HVF

The IPA space part of (A) is possible. KVM's `fillAddressSpace` already
reserves `PROT_NONE` VA until the rest fits in physical space. On macOS that
means reserving about 127 TiB to leave 1 TiB (40-bit IPA), and the check above
shows macOS allows it.

The mapping part is not possible as KVM does it:

1.  The Sentry address space has holes, and `hv_vm_map` rejects holes. A
    single mapping of the whole range cannot be made.
2.  Mapping each host region that exists at startup does not cover regions
    that appear later: Go heap growth, `pgalloc` chunks, `fsutil` file
    caches, EROFS and host-file mmaps.
3.  Regions that go away leave stale stage-2 mappings that the guest can
    still read and write. That is a correctness and isolation bug, not only a
    leak: guest memory and Sentry memory silently diverge, and a freed page
    stays reachable from the guest.

To fix 2 and 3, (A) must observe every host `mmap` and `munmap` in the
process and call `hv_vm_map` or `hv_vm_unmap` for it. The Go runtime's own
`mmap` calls are not visible to the Sentry. The Sentry's own calls include at
least:

-   `pgalloc` chunk unmap
    ([pgalloc.go:522](https://github.com/google/gvisor/blob/f51a205a8/pkg/sentry/pgalloc/pgalloc.go#L522)).
-   `fsutil` cached and precise file mappings
    ([mmap_cached_file.go:241](https://github.com/google/gvisor/blob/f51a205a8/pkg/sentry/fsutil/mmap_cached_file.go#L241),
    [mmap_precise_file.go:249](https://github.com/google/gvisor/blob/f51a205a8/pkg/sentry/fsutil/mmap_precise_file.go#L249),
    [mmap_precise_file.go:271](https://github.com/google/gvisor/blob/f51a205a8/pkg/sentry/fsutil/mmap_precise_file.go#L271)).

A version of (A) that hooks every one of these is a registry keyed by host VA,
which is (B) with a fixed VA-to-IPA function. It also maps the whole Go heap
into the guest, which (B) does not need.

## Option (B) on HVF

(B) maps only `memmap.File` ranges that `MapFile` is asked for. The mapping
owner is the code that also owns the host mapping, so it can unmap stage 2
before the host memory goes away.

Costs, from Phase 0:

| Operation | Cost |
|---|---|
| `hv_vm_map`, any size (lazy) | 330 ns |
| `hv_vm_unmap`, `hv_vm_protect` | ~200 ns |
| First guest touch of a mapped page | +1.15 µs |
| Stage-2 refault: fault exit, `hv_vm_map`, re-run, 16 KiB | ~3.0 µs |
| Release of 16 KiB (`hv_vm_unmap` + release) | ~0.9 µs |

The common case is `pgalloc` memory. Its chunks are 1 GiB and live until the
`MemoryFile` is destroyed, so one `hv_vm_map` per chunk covers almost all
application memory, and `MapFile` on a chunk is a lookup. The map cost is
per chunk, not per page, because `hv_vm_map` is lazy.

## Comparison

| Criterion | (A) whole-VA | (B) on-demand registry |
|---|---|---|
| Correct under host VA changes | Only with hooks at every host `mmap`/`munmap`, including the Go runtime's. Not possible for the runtime. | Yes. The owner of each host mapping unmaps stage 2 first. |
| IPA space (40 bits) | Needs ~127 TiB of reserved VA. Possible on macOS. | Only mapped `memmap.File` ranges use IPA. 1 TiB is a limit on mapped application memory, not on Sentry VA. |
| Guest isolation | The guest can reach any Sentry page that stage 2 maps, so the whole Sentry heap. | The guest can reach only application memory. |
| Map cost | One-time, but repeated for every new host region anyway. | 330 ns per region, once per `pgalloc` chunk. |
| `phys_footprint` double count | Every page touched by both host and guest. | Same, but only for mapped ranges. |
| Memory release | Anonymous pages need `hv_vm_unmap`; file pages can use `F_PUNCHHOLE`. | Same. |
| Code reuse from KVM | `physical_map.go`, `virtual_map.go` (needs a `mach_vm_region_recurse` port). | New registry and a guest-memory `pagetables.Allocator`. The fork's `ipa_allocator.go` is a reference. |

## Recommendation

**(B), on-demand IPA registry.** The decisive point is correctness: HVF
stage 2 does not follow host VA changes, and (A) has no way to observe the Go
runtime's own `mmap` calls. (B) also limits the guest to application memory,
which (A) cannot do.

What would change this: an HVF API that makes stage 2 follow host VA (such as
an MMU-notifier equivalent), or a measurement that shows `hv_vm_map` follows
`mmap(MAP_FIXED)` on a newer macOS. Neither exists on macOS 26.6.2.

## Registry requirements from Phase 0

1.  Call `hv_vm_unmap` before the host `munmap` or reuse of any range that is
    mapped into stage 2 (probe 5, finding 3). This applies to `pgalloc`
    chunk destroy and to `fsutil` file mappings.
2.  Align host VA, IPA and size to 16 KiB (probe 5, finding 7).
3.  Never map over a mapped IPA range; `HV_ERROR` (probe 5, finding 7).
4.  Never map a host range with a hole; `HV_ERROR` (probe 5, finding 8).
5.  Handle a stage-2 fault (EC 0x24 exit, IPA in `physical_address`) as a
    registry refault. A stage-1 fault goes to the guest EL1 vector and never
    exits as a stage-2 fault (probe 10).
6.  Map host files that are open `O_RDONLY` with `MAP_PRIVATE`, or open them
    `O_RDWR`; `O_RDONLY` + `MAP_SHARED` fails (probe 5, finding 6). This
    applies to the EROFS image
    ([erofs.go:254](https://github.com/google/gvisor/blob/f51a205a8/pkg/erofs/erofs.go#L254)).
7.  For a `pgalloc` memory file on APFS, decommit with `F_PUNCHHOLE` without
    unmapping; the guest then reads zeros (probe 6). `shm_open` memory cannot
    be punched (`EBADF`), so it needs `hv_vm_unmap`, release, and refault.
8.  Call `hv_vm_config_set_ipa_size(40)` at VM creation (probe 10).
9.  Page-table pages must be in IPA space, so `pagetables` needs an
    `Allocator` that allocates from registry-mapped memory.
