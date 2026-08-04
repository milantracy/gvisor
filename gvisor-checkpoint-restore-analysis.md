# gVisor Checkpoint/Restore: A Technical Analysis

*Written 2026-08-04*

---

## Contents

1. [The core problem and gVisor's answer](#0-the-core-problem-and-gvisors-answer)
2. [The serializer: `pkg/state`](#1-the-serializer-pkgstate)
3. [The statefile container format](#2-the-statefile-container-format)
4. [Checkpoint: quiescing and the save driver](#3-checkpoint-quiescing-and-the-save-driver)
5. [Memory: the hard part](#4-memory-the-hard-part)
6. [Re-attaching to the outside world](#5-re-attaching-to-the-outside-world)
7. [Restore orchestration](#6-restore-orchestration)
8. [Compatibility gates](#7-compatibility-gates)
9. [Beyond the basic flow](#8-beyond-the-basic-flow)
10. [Assessment](#9-assessment)
11. [File index](#appendix-file-index)

---

## 0. The core problem and gVisor's answer

Checkpoint/restore for a normal container (CRIU) means reaching *into* the host kernel and
extracting state that was never designed to be extracted: task registers, page tables, socket
send/receive queues, epoll sets, futex waiters, pending signals. It's an archaeology problem.

gVisor doesn't have that problem, because the kernel *is* the userspace process. All the state
CRIU has to dig out of `/proc` and parse is already sitting in Go heap objects owned by the
Sentry. So gVisor's C/R reduces to a different problem:

> **Serialize an arbitrary Go object graph, then re-attach it to a fresh set of host resources.**

That framing explains essentially every design decision in the implementation. The bulk of the
code is (a) a general-purpose object-graph serializer, and (b) per-subsystem hooks for the parts
of the graph that are *not* pure data — file descriptors, mappings, goroutines, network routes.

The system has five layers, taken in order below:

| Layer | Package |
|---|---|
| 1. Object-graph serializer + codegen | `pkg/state`, `tools/go_stateify` |
| 2. Container format, integrity, compression | `pkg/state/statefile` |
| 3. Quiescing and the save/load driver | `pkg/sentry/kernel`, `pkg/sentry/state` |
| 4. Subsystem hooks and external-resource reattachment | `pkg/sentry/pgalloc`, `mm`, `vfs`, `pkg/tcpip` |
| 5. Orchestration and control plane | `runsc/{control,boot,sandbox,container}` |

---

## 1. The serializer: `pkg/state`

### 1.1 Why not gob/json/protobuf

The Sentry's state is a cyclic pointer graph with properties no off-the-shelf Go serializer
handles:

- **Cycles everywhere.** `Task → ThreadGroup → TaskSet → Task`.
- **Intrusive pointers.** gVisor's intrusive lists and segment sets embed link structs *inside*
  the element, so a pointer frequently points at a *field* of an object, not the object head.
- **Interior pointers into arrays and slices.** A slice header may point into the middle of a
  backing array that is itself reachable by another path.
- **Interfaces.** Needs dynamic type resolution at load.
- **Aliasing that must be preserved.** Two pointers to the same object must remain two pointers
  to the *same* object after restore, or locking invariants break.

`pkg/state/README.md` is the authoritative design doc. The encoder maintains an **address map**
(`addrSet`, a segment set over host address ranges) mapping every byte range it has seen to an
`objectEncodeState`. When a new pointer arrives, `resolve()` (`pkg/state/encode.go`) looks it up:

- **Exact match** → emit a `wire.Ref` to the existing object ID.
- **Contained within a known object** → emit a `wire.Ref` with a **traversal path**. `traverse()`
  walks down the containing object's type structure emitting `wire.Dot` accessors (field index /
  array index), built in reverse order. This is how "pointer to field 3 of the struct at address
  X" is encoded.
- **Superset of known objects** (the new object *contains* previously-encoded ones) → the encoder
  **rewrites** the existing refs to become interior refs into the new, larger object. This is the
  trickiest part of the encoder: object identity is discovered incrementally, so earlier decisions
  must be revised.

Traversal itself is **deferred, not recursive**: `encodeState` keeps a work list (`deferred`) so
the encoder doesn't blow the Go stack on a graph millions of objects deep. Types are emitted
before objects; objects are then emitted **sorted by object ID**, and IDs are *implied by stream
order* rather than written explicitly. The stream distinguishes object headers from raw byte-run
headers with a top bit, `objectFlag = 1<<63`.

The README's worked example (`g0r1` / `g0r2` / `g0r3` with `g0r3.c` pointing at an interior
field) is the canonical illustration of interior-pointer encoding.

### 1.2 The decoder and dependency ordering

`pkg/state/decode.go` does a **single pass** over the stream, but object construction is not
enough — objects have *semantic* dependencies. `LoadWait` and `AfterLoad` callbacks must fire only
once the objects they depend on are fully materialized.

The decoder models this as a dependency graph with reference counting: each `objectDecodeState`
has a `blockedBy` counter and lives on either the `pending` list or the `leaves` list. As leaves
complete, their dependents' counters drop, and callbacks fire in dependency order. `walkChild()`
resolves the `wire.Dot` traversal paths produced by the encoder.

Two things worth calling out:

- **Self-references and the root object (`id == 1`) are special-cased** — otherwise the graph
  would never have a leaf to start from.
- **Cycle detection is explicit and diagnostic.** If the pending set is non-empty and no leaves
  remain, `findCycle()` reconstructs the actual cycle and `Failf("incomplete graph: %s", ...)`
  prints it. This matters because an `afterLoad` cycle is a real, reachable programming error when
  someone adds a new `afterLoad` to a subsystem.

### 1.3 Error handling: panic/recover on purpose

`pkg/state/state.go`'s `safely()` documents the rationale: reflection panics anyway on type
mismatch, and threading `error` returns through thousands of generated `StateSave`/`StateLoad`
methods would be enormous boilerplate for no benefit. So the serializer uses `state.Failf` +
panic, recovers at the API boundary, and attaches the captured stack trace. A deliberate
deviation from Go norms, scoped to one package.

### 1.4 `go_stateify`: the codegen

Hand-writing serialization for ~1100 types is not viable, so `tools/go_stateify` generates it.
Census of the current tree:

| Marker | Count |
|---|---|
| `// +stateify savable` | 1092 |
| `state:"nosave"` | 493 |
| `state:"wait"` | 16 |
| `state:"manual"` | 10 |
| `state:"zerovalue"` | 6 |
| hand-written `beforeSave()` | 26 |
| hand-written `afterLoad(ctx)` | 14 |

The generator emits `StateTypeName`, `StateFields`, `StateSave`, `StateLoad`, plus no-op
`beforeSave`/`afterLoad` defaults that a type can shadow with its own. Field tags (see
`scanField` in `tools/go_stateify/main.go`):

| Tag | Meaning |
|---|---|
| `nosave` | Don't serialize; must be reconstructible. ~500 uses — the primary mechanism for excluding runtime scaffolding. |
| `zerovalue` | Assert the field is zero at save time. |
| `wait` | Loader must block on this field being complete before this object's `afterLoad` runs. |
| `manual` / `ignore` | The type handles it itself. |
| `.(Type)` | Save/load through a converting value type (e.g. persist an unexported handle as an int). |

**The ratio matters: roughly a third of the design work in gVisor C/R is deciding what *not* to
save.** A `Task` marks `nosave` on its platform context `p`, all mutexes, `interruptChan`,
`stopCount`/`endStopCond`, `goroutineStopped`, `futexWaiter`, blocking timers, trace context, and
`copyScratchBuffer`. `Task.afterLoad` (`pkg/sentry/kernel/task.go:718`) rebuilds all of it:

```go
func (t *Task) afterLoad(gocontext.Context) {
	t.updateInfoLocked()
	if ts := t.seccomp.Load(); ts != nil { ts.populateCache(t) }
	t.interruptChan = make(chan struct{}, 1)
	t.gostate.Store(uint32(TaskGoroutineNonexistent))
	if t.stop != nil { t.stopCount = atomicbitops.FromInt32(1) }
	t.endStopCond.L = &t.tg.signalHandlers.mu
	t.rseqPreempted = true
	t.futexWaiter = futex.NewWaiter()
	t.p = t.k.Platform.NewContext(t.AsyncContext())
}
```

Note `t.p = t.k.Platform.NewContext(...)`: the *platform* context (systrap stub process / KVM
vCPU) is never serialized. Only the architectural register + FPU state is (`pkg/sentry/arch`,
with `afterLoadFPState` calling into the FPU state's own `AfterLoad` for XSAVE-area fixups). The
mechanism for *executing* those registers is rebuilt from scratch — which is exactly why gVisor
can checkpoint under systrap and restore under KVM.

Similarly, `pkg/sentry/kernel/kernel_state.go` holds a family of `save*`/`load*` shims that
flatten `atomic.Pointer` fields (`vforkParent`, `ptraceTracer`, `seccomp`, `fsContext`,
`appCPUClockLast`, `oldRSeqCritical`, …) into plain saved fields, plus
`saveDanglingEndpoints`/`loadDanglingEndpoints`, which bridge to netstack's package-global
`tcpip.GetDanglingEndpoints()` — a rare case of process-global state smuggled through the object
graph.

---

## 2. The statefile container format

`pkg/state/statefile/statefile.go`:

```
"gVisorSF"                (8 bytes magic)
uint64 big-endian         (metadata length)
JSON metadata             (internal keys prefixed with "_", e.g. "_timestamp")
data...
```

- **Integrity:** optional HMAC-SHA256 keyed by `SaveOpts.Key`, computed over header + metadata and
  interleaved with the data stream. The reader verifies before handing bytes up.
- **Compression:** `compressio` with `flate-best-speed` in 1 MiB chunks, or `NewSimpleWriter` for
  `none`. **`none` is the default** — see §4.1 for why.

Metadata is a general side channel; e.g. `pkg/sentry/state/state.go` stashes `GvisorCPUUsageKey`
so restore-side tooling can see how much CPU the sandbox burned before the checkpoint.

---

## 3. Checkpoint: quiescing and the save driver

### 3.1 Getting to a consistent point

You cannot serialize a live object graph. `pkg/sentry/state/state.go` `Save()`:

```go
k.Pause()
k.ReceiveTaskStates()
defer func() { k.Unpause() }()
w.Stop(); defer w.Start()          // watchdog

wc, err := statefile.NewWriter(opts.Destination, opts.Key, opts.Metadata)
err = k.SaveTo(ctx, wc, opts.PagesMetadata, opts.PagesFile,
                opts.AppMFExcludeCommittedZeroPages, opts.Resume)

if opts.Resume { k.BeforeResume(ctx) } else { k.Kill(linux.WaitStatusExit(0)) }
```

`Kernel.Pause()` (`pkg/sentry/kernel/kernel.go:1718`) issues `TaskSet.BeginExternalStop()` and
then **waits on two WaitGroups**: `runningGoroutines` and `aioGoroutines`. Both are
`state:"nosave"` and documented as "required to be zero at the time of save" — the pause protocol
is what makes that assertion true. Every task goroutine parks at a checkpoint-safe point; the
`// S/R-SAFE:` comments scattered through the kernel mark where blocking is legal across a save.

`ReceiveTaskStates()` then calls `PullFullState()` per task, pulling registers and FPU state out
of the platform (stub process / vCPU) into the Go-side `arch.State` that will actually be
serialized. Without this, the saved registers would be stale.

`quiescePausedAnd` (`kernel.go:669`) adds the rest of the freeze under `extMu`:

```go
fdnotifier.Pause()                                // stop host epoll delivery
k.pauseTimeLocked(ctx)                            // stop Timekeeper + all timers
k.mf.StartEvictions(); k.mf.WaitForEvictions()    // drain MemoryFile eviction
f()
```

### 3.2 `Kernel.SaveTo`

`kernel.go:717`. Ordered steps, each with a reason:

1. **Reject non-4K page size.** Page-granular memory metadata assumes it.
2. **`invalidateUnsavableMappings`** (`kernel.go:914`) — walk every task's `MemoryManager` and call
   `InvalidateUnsavable`, dropping mappings that cannot be re-established (e.g. host-file mappings
   with no stable identity).
3. **`vfs.PrepareSave`** with `context.WithValue(ctx, pgalloc.CtxMemoryFileMap, mfsToSave)` —
   filesystems flush and quiesce (§5).
4. **`MarkSavable()`** on the MemoryFile.
5. **Parallel memory save**: if a separate `pagesMetadata` file was given, spawn
   `go k.saveMemoryFiles(...)` to run *concurrently with* the kernel object-graph save.
6. **Save `k.featureSet` first**, standalone, before anything else. The comment is explicit: *"so
   we can verify its compatibility on restore before attempting to restore the entire kernel."*
   This is the CPUID gate (§7).
7. **Pause netstack** with `SetRemoveConf(!resume)` — if this is a real checkpoint (not
   save-and-continue), tear down NIC/route configuration so it gets rebuilt on the destination
   host.
8. **`state.Save(ctx, stateFile, k)`** — the object graph.
9. **Close the state file early**, so its flush/fsync latency overlaps with the still-running
   MemoryFile save.

The `Resume` flag threads through everything: with `Resume=true` the sandbox keeps running after
the checkpoint (`k.BeforeResume(ctx)`); otherwise every process is killed with exit status 0.

---

## 4. Memory: the hard part

Application memory dominates checkpoint size and time.
`pkg/sentry/pgalloc/save_restore.go` (~2000 lines) is where most of the performance engineering
lives.

### 4.1 The three-file layout

`runsc/sandbox/sandbox.go` `createSaveFiles` produces **three** files:

| File | Contents |
|---|---|
| state file | kernel object graph |
| pages-metadata file | `memoryFileSaved` — which ranges are allocated, their accounting kind |
| pages file | raw page contents |

This split is what enables (a) saving kernel state and memory in parallel, and (b) **async page
loading** on restore. It is only produced when `compression=none`, and `O_DIRECT` is only used
when both `direct` is requested *and* compression is none.

> **This is the concrete reason compression defaults to off.** Compression forces a single serial
> stream and forfeits both optimizations. It's a large behavioral cliff behind one config flag.

### 4.2 Save-side reductions

- **Zero-page exclusion** (`ExcludeCommittedZeroPages` / `AppMFExcludeCommittedZeroPages`): scan
  committed pages, skip the all-zero ones. Applications routinely have large zeroed heaps/BSS;
  this is often the single biggest size win.
- **Transient-commit decommit batching within huge pages**: avoid writing out pages that exist
  only because of THP-driven over-commit.
- **`memAcct` segment-set batching**: memory accounting is stored as ranges, not per page.

Then `state.Save(memoryFileSaved)` writes the metadata and page contents stream to the pages file.

### 4.3 Restore-side: async loading

`LoadFrom` does *not* read pages up front. It:

1. `mmap`s the whole pages file once and splits it into the MemoryFile's chunk mappings.
2. Runs a concurrent `madvise` goroutine.
3. Then either reads each segment synchronously, or — in the async path — **records the ranges as
   unloaded** and returns immediately.

`AsyncPagesFileLoad` then loads in the background. The critical piece is `awaitLoad(fr)`:

- A **lockless fast path** on `minUnloaded`: if the requested range is entirely below the
  low-water mark, return with no synchronization at all. This is the common case once loading is
  mostly done, and it keeps the hook nearly free.
- Otherwise, push the range onto a **priority deque** so a faulting application thread jumps ahead
  of the sequential background loader.

`awaitLoad` is hooked into exactly two places in `pkg/sentry/pgalloc/pgalloc.go`: `MapInternal`
(~`:1446`) and `DataFD` (~`:1810`). Every path that can observe page contents goes through one of
them, so demand paging is complete without instrumenting callers.

`cancelWasteLoad` (~`:1244`) is the complement: if a range is freed or overwritten before it's
loaded, don't bother loading it.

`AsyncMFLoader` (`pkg/sentry/kernel/kernel_restore.go`) sequences this at the kernel level: a
background goroutine loads the **main** MemoryFile first, then per-container **private**
MemoryFiles fed over `privateMFsChan`, with staged barriers
`WaitMainMFStart` → `WaitMetadata` → `Wait`. The restore path only blocks on the earliest stage it
truly needs, so the application starts running while its pages are still streaming in.

### 4.4 The async I/O substrate

`pkg/sentry/state/stateio` abstracts `AsyncReader`/`AsyncWriter`. The backend choice is
asymmetric, and the comments explain why:

- **`FDReader` prefers `aio.GoQueue`** over Linux AIO — *"since it can allocate and zero
  destination pages in parallel."* On read, page allocation/zeroing is the bottleneck, not the
  syscall, so goroutine parallelism wins.
- **`FDWriter` prefers `aio.LinuxQueue`** (real Linux AIO, `maxParallel`), falling back to
  `aio.NewSerialGoQueue`. On write there are no pages to allocate, so kernel-side AIO wins.

---

## 5. Re-attaching to the outside world

Serializing the graph is the easy half. Every handle to something *outside* the Sentry has to be
re-acquired.

### 5.1 The key abstraction: `checkpoint.ResourceID`

```go
type ResourceID struct {
	ContainerName string
	Path          string
}
```

This is the stable, cross-restore name for host FDs, gofer mounts, and private MemoryFiles. Host
FD numbers are meaningless after restore; `host.MakeResourceID(containerName, fd)` produces
`host:%d`, and `runsc/boot/restore.go` builds an fdmap/mfmap keyed by `ResourceID` that the
restore-side hooks look themselves up in. `pkg/sentry/mm/save_restore.go` uses the same key on
`pma.saveFile`/`loadFile`.

### 5.2 VFS extension contract

`pkg/sentry/vfs/save_restore.go`:

```go
type FilesystemImplSaveRestoreExtension interface {
	PrepareSave(ctx) error
	BeforeResume(ctx)
	CompleteRestore(ctx, CompleteRestoreOptions) error
}
```

with `CompleteRestoreOptions{ValidateFileSizes, ValidateFileModificationTimestamps}` and a
sentinel `vfs.ErrCorruption`. Also notable: `epollInterest.afterLoad` re-notifies, forcing
readiness to be recomputed rather than trusted from the checkpoint — the world may have changed.

### 5.3 Gofer/lisafs: the most involved case

`pkg/sentry/fsimpl/gofer/save_restore.go`.

**PrepareSave:**

- Evict cached dentries and **delete negative dentries** (`prepareSaveRecursive`) — stale negative
  cache entries would be wrong after restore.
- **Buffer readable FIFO/pipe data** into the checkpoint. In-flight pipe bytes live in the gofer,
  not the Sentry, so they'd otherwise be lost.
- **Handle deleted-but-open files**: buffer their contents. A file unlinked but still open has no
  path to re-walk.
- `fs.Sync()`.

**CompleteRestore:**

- Look up the new gofer connection by `fs.iopts.UniqueID` in the fdmap.
- `restoreRoot`, then `restoreDescendantsRecursive` — **re-walk the entire saved dentry tree**
  against the fresh connection.
- **inoKey remapping** (`pkg/sentry/fsimpl/gofer/lisafs_inode.go:610-665`): the comment states
  plainly that gofers do not preserve `inoKey` across C/R. So the *new* inoKey from the re-walk is
  mapped onto the *existing* `i.ino`, preserving the inode identity the application already
  observed.
- **Validate stability**: size and mtime are checked against saved values; mismatch →
  `vfs.ErrCorruption`. This is the guard against "someone modified the backing filesystem while
  the sandbox was checkpointed."
- Re-open handles from `savedDentryRW` via `ensureSharedHandle`.
- **Resurrect deleted-but-open files**: recreate under random temp names, reopen, then unlink —
  reproducing the "open but unlinked" state. There's a cleanup unlink loop for nested directories.
- Re-open `specialFileFD`s, with **write-only pipes deferred to a second pass** (opening the write
  end of a FIFO blocks until a reader exists, so readers must be restored first).

### 5.4 Host FDs

`pkg/sentry/fsimpl/host/save_restore.go`: `inode.beforeSave` drains pipes (tolerating `EBADF`);
`inode.afterLoad` remaps through the fdmap, re-applies `SetNonblock`, and re-registers with
`fdnotifier.AddFD`. If the FD isn't restorable, `hostFD = -1` — the object survives, operations on
it fail.

### 5.5 Netstack / TCP

`pkg/tcpip/stack/save_restore.go`: `Stack.beforeSave` removes NICs and routes when `removeConf`;
`Stack.afterLoad` **reseeds the RNGs**. (Restoring a saved PRNG state across N restores from one
checkpoint would give N identical sequences — a real security issue given ISN generation.)

`pkg/tcpip/transport/tcp/endpoint_state.go` is the most subtle file in the whole system. TCP
endpoints cannot be restored in arbitrary order; the file uses three **package-global WaitGroups**
— `connectedLoading`, `listenLoading`, `connectingLoading` — with the comment:

> *"Endpoint loading must be done in the following ordering by their state, to avoid dangling
> connecting w/o listening peer, and to avoid conflicts in port reservation"*

So: connected endpoints first, then listeners, then connecting endpoints. Each endpoint's
`afterLoad` resets state to `StateInitial` and calls `RegisterRestoredEndpoint`; the real work
happens in `Restore(s *stack.Stack)`, which per-state reconstructs the endpoint — including
calling `e.connect(..., false /* handshake */)` for already-established connections (re-establish
the 4-tuple and route without redoing the handshake), rebuilding every timer, and
`scoreboard.Reset()` with the explicit note *"we do not restore SACK information."*

Dropping SACK state is safe (it costs at most some retransmission) and avoids a large, fragile
serialization surface. `terminateAtRestore` marks endpoints that can't survive; `beforeSave`
freezes the queue.

---

## 6. Restore orchestration

`runsc/boot/restore.go`, `restorer.restore(l *Loader)`:

1. **Validate the spec** — `specutils.RestoreValidateSpec` compares the restore-time OCI spec
   against the checkpoint's. Deliberately lenient in places: `cloneMount` **drops `Source`** before
   comparing mounts (the host path legitimately differs on a new machine), and devices are compared
   on Path/Type only.
2. Create the platform.
3. **Replace the kernel object wholesale** — restore doesn't mutate a running kernel, it swaps in a
   freshly loaded one.
4. **Apply seccomp filters *before* parsing the state file.** The state file is untrusted input
   being fed to a reflection-driven deserializer; it gets parsed inside the reduced syscall
   surface.
5. Build the fdmap and mfmap from `ResourceID`s.
6. `KickoffPrivate` — start async loading of private MemoryFiles.
7. **`Kernel.LoadFrom`** (`kernel.go:939`):
   - Load `FeatureSet`, run **`CheckHostCompatible()`** — abort here if the destination CPU lacks
     features the guest already observed via CPUID.
   - `fdnotifier.Pause()`.
   - `state.Load(ctx, r, k)` — the object graph.
   - `k.loadMemoryFiles` (sync) or `asyncMFLoader.WaitMainMFStart()` (async).
   - `SetClocks`.
   - Network: `ResetConfig` → `ConfigureNetwork` → `Restore`.
   - `k.vfs.CompleteRestore(ctx, vfsOpts)`.
   - Check application-visible core count if `useHostCores`.
8. Replace the watchdog; remap container IDs and UTS namespaces; kill `OriginExec` thread groups
   (processes that existed only to drive the checkpoint).
9. `cm.onStart()`, and fork a `postRestore` goroutine.
10. **`Kernel.Start()`** (`kernel.go:1448`) resumes timers via `resumeTimeLocked` and calls
    `t.Start()` on every task. `Task.Start` (`task_start.go:518`) begins:

    ```go
    // If the task was restored, it may be "starting" after having already exited.
    if t.runState == nil { return }
    ```

    — a restored zombie has no run state and must not get a goroutine.

**Time continuity** is handled by `Timekeeper` (`pkg/sentry/kernel/timekeeper_state.go`):
`beforeSave` panics if updates weren't paused, then captures `saveMonotonic` and `saveRealtime`;
`afterLoad` creates a fresh `restored` channel that gates readers until `SetClocks` establishes
the new offsets. Monotonic time is preserved as an *offset* so it never goes backwards across a
restore.

---

## 7. Compatibility gates

C/R across machines is where correctness gets hard. gVisor's gates:

| Gate | Mechanism | Why |
|---|---|---|
| **CPU features** | FeatureSet saved first; `CheckHostCompatible()` before graph load | The app has already executed CPUID; silently losing AVX-512 means SIGILL later. Fail early and loudly. Users can restrict advertised features via annotations. |
| **Page size** | `SaveTo` rejects non-4K | Memory metadata granularity. |
| **OCI spec** | `RestoreValidateSpec`, Source-insensitive mounts | Catch structural mismatch, tolerate host-path differences. |
| **File stability** | size + mtime validation → `vfs.ErrCorruption` | Detect backing-store mutation while checkpointed. |
| **Network mode** | checked at restore | Host-networking sockets aren't serializable state. |
| **NVIDIA driver version** | nvproxy check | Driver ABI must match. |

---

## 8. Beyond the basic flow

**Application-driven C/R.** `g3doc/user_guide/checkpoint_restore.md` documents a protocol where
the application itself triggers and observes checkpoints: writes to `/proc/gvisor/checkpoint`,
plus `/proc/gvisor/spec_environ`, driven by `dev.gvisor.internal.checkpoint.*` annotations. This
lets an app flush its own state before the freeze and re-derive environment-dependent config after
the thaw.

**Save/restore exec hook.** `pkg/sentry/control/state.go` `SaveRestoreExec` spawns an in-sandbox
process with `GVISOR_SAVE_RESTORE_AUTO_EXEC_MODE` set to `save`, `restore`, or `resume`, with
piped output and a timeout backed by SIGKILL. Arbitrary in-sandbox logic at each phase transition.

**GPU.** CUDA state is checkpointed by shelling out to NVIDIA's `cuda-checkpoint`
(`CudaCheckpointPath`, `CudaCheckpointSequential`), with nvproxy/TPU pre- and post-hooks around it.

**Checkpoint gofer.** Instead of local files, a urpc/`stateipc` path can stream checkpoint data
over a Unix socket to remote storage (GCS et al.), so the sandbox never needs local disk for a
checkpoint.

**Filesystem-only checkpoint.** `pkg/sentry/kernel/fscheckpoint.go` + `pkg/sentry/fscheckpoint`
implement `FSSave` — manifest + multi-tar + pages metadata + pages, scoped to a set of
`checkpoint.ResourceID` paths (defaulting to `/`). A distinct product from full sandbox C/R:
capture container filesystem state without capturing execution state.

---

## 9. Assessment

### What's genuinely strong

- **The architectural bet pays off.** Building a general object-graph serializer and putting *all*
  of the kernel behind it means new subsystems get C/R almost for free — add
  `// +stateify savable` and a couple of `nosave` tags. Compare to CRIU, where every new kernel
  feature is a new extraction problem. The 1092-to-40 ratio of savable types to hand-written hooks
  is the evidence.
- **The `nosave` discipline *is* the real design.** The interesting engineering isn't in the
  encoder; it's in the ~500 decisions about which state is derivable and the ~40 `afterLoad`s that
  derive it. That's what makes checkpoint-on-systrap / restore-on-KVM work.
- **Async page loading is well-targeted.** Hooking only `MapInternal` and `DataFD`, with a lockless
  `minUnloaded` fast path and fault-driven priority, gets demand paging with minimal invasiveness
  and near-zero steady-state cost.
- **Failing loudly on incompatibility.** The FeatureSet-first ordering and the file size/mtime
  validation both choose "abort at restore" over "corrupt silently later." Given the failure mode
  (an application that mysteriously misbehaves hours after restore), that's the right call.

### Where the complexity concentrates — and the risks

- **Encoder `resolve()`.** Superset-rewriting of already-emitted refs is the subtlest code in
  `pkg/state`. It's correct, but it's the kind of correct that depends on invariants no type system
  enforces.
- **Global ordering in TCP restore.** Three package-global WaitGroups sequencing endpoint restore
  is a coordination mechanism *outside* the serializer's dependency graph — a second, weaker
  ordering system layered on the first. Adding a new endpoint state means remembering it exists.
- **Gofer restore surface.** Dentry re-walk + inoKey remapping + deleted-file resurrection via
  random temp names is a lot of machinery reproducing filesystem states that have no representation
  on disk. Every step is a place where a slightly different gofer or backing filesystem breaks
  things.
- **`nosave` is unverifiable.** Nothing checks that an `afterLoad` actually reconstructs a field
  equivalently to what was dropped. A subtly wrong reconstruction produces a sandbox that restores
  "successfully" and then misbehaves. `zerovalue` (6 uses) is the only assertion mechanism, and
  it's barely used.
- **Compression vs. speed is a hard fork in the design.** Choosing compression disables the
  three-file layout, parallel save, `O_DIRECT`, and async restore. That's a large cliff hidden
  behind one config flag, and the default (`none`) reflects that the fast path is the supported
  path.

The dropped-SACK decision is a good template for how the rest of the system reasons: identify
state that is *reconstructible or merely optimizing*, drop it, and accept a bounded performance
cost in exchange for a much smaller correctness surface. Most of gVisor's C/R quality comes from
applying that judgment consistently, several hundred times.

---

## Appendix: file index

### Serializer

| File | Role |
|---|---|
| `pkg/state/README.md` | Authoritative encoder/decoder design doc |
| `pkg/state/state.go` | `Save`/`Load` API, `Sink`/`Source`, `safely()` |
| `pkg/state/encode.go` | `encodeState`, `resolve()`, `traverse()`, `encodeStruct` |
| `pkg/state/decode.go` | `objectDecodeState`, dependency lists, cycle detection |
| `pkg/state/statefile/statefile.go` | Container format, HMAC, compression |
| `tools/go_stateify/main.go` | Codegen; `scanField` tag dispatch |

### Kernel driver

| File | Role |
|---|---|
| `pkg/sentry/state/state.go` | High-level `Save()`, `SaveOpts` |
| `pkg/sentry/kernel/kernel.go` | `quiescePausedAnd`:669, `SaveTo`:717, `invalidateUnsavableMappings`:914, `LoadFrom`:939, `Start`:1448, `Pause`:1718 |
| `pkg/sentry/kernel/kernel_restore.go` | `AsyncMFLoader`, `Saver`, `loadPrivateMemoryFiles` |
| `pkg/sentry/kernel/kernel_state.go` | `TaskSet.afterLoad`, dangling endpoints, atomic-pointer shims |
| `pkg/sentry/kernel/task.go` | `Task.afterLoad`:718, `nosave` field census |
| `pkg/sentry/kernel/task_start.go` | `Task.Start`:518 |
| `pkg/sentry/kernel/timekeeper_state.go` | Time continuity |
| `pkg/sentry/kernel/fscheckpoint.go` | Filesystem-only checkpoint (`FSSave`) |
| `pkg/sentry/arch/arch_state_x86.go` | FPU state `afterLoad` |

### Memory

| File | Role |
|---|---|
| `pkg/sentry/pgalloc/save_restore.go` | `SaveTo`, `LoadFrom`, `AsyncPagesFileLoad`, `awaitLoad` |
| `pkg/sentry/pgalloc/pgalloc.go` | `MapInternal`:~1446, `DataFD`:~1810, `cancelWasteLoad`:~1244 |
| `pkg/sentry/mm/save_restore.go` | `InvalidateUnsavable`, `MemoryManager.afterLoad`, pma save/load file |
| `pkg/sentry/state/stateio/{stateio,fdreader,fdwriter}.go` | Async I/O backends |

### Subsystems

| File | Role |
|---|---|
| `pkg/sentry/vfs/save_restore.go` | `FilesystemImplSaveRestoreExtension`, VFS drivers |
| `pkg/sentry/fsimpl/gofer/save_restore.go` | Gofer `PrepareSave`/`CompleteRestore` |
| `pkg/sentry/fsimpl/gofer/lisafs_inode.go:610-665` | `restoreInode`, inoKey remapping |
| `pkg/sentry/fsimpl/host/save_restore.go` | `MakeResourceID`, host FD remap |
| `pkg/tcpip/stack/save_restore.go` | Stack `beforeSave`/`afterLoad` |
| `pkg/tcpip/transport/tcp/endpoint_state.go` | TCP restore ordering, `Restore()` |

### Orchestration

| File | Role |
|---|---|
| `pkg/sentry/control/state.go` | urpc `SaveOpts`, `SaveRestoreExec`, `PostResume`/`PostRestore` |
| `runsc/boot/restore.go` | `restorer.restore` |
| `runsc/sandbox/sandbox.go` | `Checkpoint`/`Restore`, `createSaveFiles` (536-660, 1628-1760) |
| `runsc/specutils/restore.go` | `RestoreValidateSpec`, `cloneMount` |
| `g3doc/user_guide/checkpoint_restore.md` | User-facing docs, application-driven C/R protocol |
