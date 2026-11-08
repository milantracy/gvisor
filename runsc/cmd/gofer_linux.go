// Copyright 2018 The gVisor Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//go:build linux

package cmd

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"runtime/debug"
	"syscall"

	"github.com/google/subcommands"
	specs "github.com/opencontainers/runtime-spec/specs-go"
	"golang.org/x/sys/unix"
	"gvisor.dev/gvisor/pkg/log"
	"gvisor.dev/gvisor/pkg/unet"
	"gvisor.dev/gvisor/pkg/urpc"
	"gvisor.dev/gvisor/runsc/boot/sentryapi"
	"gvisor.dev/gvisor/runsc/cmd/sandboxsetup"
	"gvisor.dev/gvisor/runsc/cmd/util"
	"gvisor.dev/gvisor/runsc/config"
	"gvisor.dev/gvisor/runsc/container"
	"gvisor.dev/gvisor/runsc/flag"
	fsgofer "gvisor.dev/gvisor/runsc/gofer/fs"
	"gvisor.dev/gvisor/runsc/gofer/fs/extension"
	"gvisor.dev/gvisor/runsc/gofer/fs/filter"
	"gvisor.dev/gvisor/runsc/gvisorbinaries"
	"gvisor.dev/gvisor/runsc/profile"
	"gvisor.dev/gvisor/runsc/specutils"
)

// Execute implements subcommands.Command.
func (g *Gofer) Execute(_ context.Context, f *flag.FlagSet, args ...any) subcommands.ExitStatus {
	if g.bundleDir == "" || len(g.ioFDs) < 1 || g.specFD < 0 {
		f.Usage()
		return subcommands.ExitUsageError
	}
	if f.NArg() != 1 {
		f.Usage()
		return subcommands.ExitUsageError
	}
	containerID := f.Arg(0)

	conf := args[0].(*config.Config)

	// Set traceback level
	debug.SetTraceback(conf.Traceback)

	// Whether this process will re-execute itself to apply credential and
	// capability changes, rather than applying capabilities in-process.
	// Computed before the sync FDs are consumed below.
	willReexec := g.applyCaps && (config.CgoEnabled || g.syncFDs.usernsFD >= 0)

	specFile := os.NewFile(uintptr(g.specFD), "spec file")
	spec, err := specutils.ReadSpecFromFile(g.bundleDir, specFile, specutils.SpecOpts{Conf: conf})
	if err != nil {
		util.Fatalf("reading spec: %v", err)
	}
	mountHints, err := sentryapi.NewPodMountHints(spec)
	if err != nil {
		util.Fatalf("parsing mount hints: %v", err)
	}
	rootfsHint, err := sentryapi.NewRootfsHint(spec)
	if err != nil {
		util.Fatalf("parsing rootfs hint: %v", err)
	}
	lisafsNeeded := lisafsNeededForDirectFSSuppression(spec, mountHints, rootfsHint, g.mountConfs)

	g.syncFDs.syncChroot()
	g.syncFDs.syncUsernsForRootless(uint32(g.uid), uint32(g.gid))

	// Only pass the FD if the gofer will actually need it.
	var goferToHostRPC *urpc.Client
	var mountOpener sandboxsetup.MountOpener
	if g.goferToHostRPCFD >= 0 {
		goferToHostRPCSock, err := unet.NewSocket(g.goferToHostRPCFD)
		if err != nil {
			util.Fatalf("creating rpc socket: %v", err)
		}
		goferToHostRPC = urpc.NewClient(goferToHostRPCSock)
		mountOpener = makeRPCMountOpener(goferToHostRPC)
	}

	var umounter *os.File
	spawnUnmounter := g.setUpRoot && willReexec && !conf.TestOnlyAllowRunAsCurrentUserWithoutChroot
	if g.setUpRoot {
		// The /proc unmounter binary must be opened before SetupRootFS pivots
		// away from the host filesystem.
		if spawnUnmounter {
			umounter = openProcUmounter()
		}
		if err := sandboxsetup.SetupRootFS(spec, conf, g.mountConfs, g.devIoFD, mountOpener, containerID, g.bundleDir); err != nil {
			util.Fatalf("Error setting up root FS: %v", err)
		}
	}
	// No longer needed past rootfs setup.
	if goferToHostRPC != nil {
		goferToHostRPC.Close()
	}
	if spawnUnmounter {
		cleanupUnmounter := g.syncFDs.spawnProcUnmounter(umounter)
		umounter.Close()
		defer cleanupUnmounter()
	}
	extensionPrepare, err := extension.PrepareGofer(extension.GoferPrepareContext{
		Spec:        spec,
		ContainerID: containerID,
		BundleDir:   g.bundleDir,
	})
	if err != nil {
		util.Fatalf("preparing gofer extensions: %v", err)
	}
	capsToApply := goferCaps
	if conf.GetHostUDS().AllowOpen() {
		capsToApply = specutils.MergeCapabilities(capsToApply, goferUdsOpenCaps)
	}
	if g.applyCaps && willReexec {
		if config.CgoEnabled {
			log.Warningf("Need to re-exec in order to drop capabilities, due to cgo build. This slows down gVisor startup. Use a pure-Go gVisor build for faster startup.")
		} else {
			log.Infof("Re-execing. FYI, this step isn't necessary when running with root. gVisor will start up faster (and is just as secure) when started as root, as it has to do fewer hoops to get to its as-sandboxed-as-possible state.")
		}
		overrides := g.syncFDs.flags()
		overrides["apply-caps"] = "false"
		overrides["setup-root"] = "false"
		overrides["rpc-fd"] = "-1" // Skip the `goferToHostRPCFD >= 0` branch on re-exec.
		for key, value := range extensionPrepare.FlagOverrides {
			overrides[key] = value
		}
		args := sandboxsetup.PrepareArgs(g.Name(), f, overrides)
		util.Fatalf("setCapsAndCallSelf(%v, %v): %v", args, capsToApply, sandboxsetup.SetCapsAndCallSelf(args, capsToApply))
		// Prevent the specFile finalizer from closing specFD before re-exec.
		runtime.KeepAlive(specFile)
		panic("unreachable")
	}
	// No re-exec from here on out. Close specFile so the spec memfd is not
	// held open inside the chroot for the lifetime of the container.
	if err := specFile.Close(); err != nil {
		util.Fatalf("closing specFile: %v", err)
	}

	// Start profiling. This will be a noop if no profiling arguments were passed.
	// This *must* happen while procfs is still mounted (need `/proc/self/maps`).
	profileOpts := profile.MakeOpts(&g.profileFDs, conf.ProfileGCInterval)
	g.stopProfiling = profile.Start(profileOpts)

	// Find what path is going to be served by this gofer.
	root := spec.Root.Path
	if !conf.TestOnlyAllowRunAsCurrentUserWithoutChroot {
		root = "/root"
	}

	// Resolve mount points paths, then replace mounts from our spec and send the
	// mount list over to the sandbox, so they are both in sync.
	//
	// Note that all mount points have been mounted in the proper location in
	// setupRootFS().
	cleanMounts, err := sandboxsetup.ResolveMounts(conf, spec.Mounts, root, g.mountConfs)
	if err != nil {
		util.Fatalf("Failure to resolve mounts: %v", err)
	}
	spec.Mounts = cleanMounts
	go func() {
		if err := sandboxsetup.WriteMounts(g.mountsFD, cleanMounts); err != nil {
			panic(fmt.Sprintf("Failed to write mounts: %v", err))
		}
	}()

	specutils.LogSpecDebug(spec, conf.OCISeccomp)

	// fsgofer should run with a umask of 0, because we want to preserve file
	// modes exactly as sent by the sandbox, which will have applied its own umask.
	unix.Umask(0)

	// Open `/proc/self/fd` (needs procfs).
	procFDPath := sandboxsetup.ProcFDBindMount
	if conf.TestOnlyAllowRunAsCurrentUserWithoutChroot {
		procFDPath = "/proc/self/fd"
	}
	if err := fsgofer.OpenProcSelfFD(procFDPath); err != nil {
		util.Fatalf("failed to open /proc/self/fd: %v", err)
	}

	canSetGroups := specutils.SetgroupsAllowed()

	// Look up our own caps (needs procfs).
	var resolvedCaps *sandboxsetup.ResolvedThreadCaps
	if g.applyCaps {
		var err error
		resolvedCaps, err = sandboxsetup.ResolveThreadCaps(capsToApply, nil)
		if err != nil {
			util.Fatalf("resolving capabilities: %v", err)
		}
	}

	// Unmount procfs.
	if g.setUpRoot && !conf.TestOnlyAllowRunAsCurrentUserWithoutChroot && g.syncFDs.procMountFD < 0 {
		sandboxsetup.UmountProcInProcess()
	} else {
		g.syncFDs.unmountProcfs()
	}

	if g.applyCaps {
		if err := resolvedCaps.Apply(nil); err != nil {
			util.Fatalf("applying caps to all threads: %v", err)
		}
	}

	// At this point we won't re-execute, so it's safe to limit via rlimits. Any
	// limit >= 0 works. If the limit is lower than the current number of open
	// files, then Setrlimit will succeed, and the next open will fail.
	if conf.FDLimit > -1 {
		rlimit := unix.Rlimit{
			Cur: uint64(conf.FDLimit),
			Max: uint64(conf.FDLimit),
		}
		switch err := unix.Setrlimit(unix.RLIMIT_NOFILE, &rlimit); err {
		case nil:
		case unix.EPERM:
			log.Warningf("FD limit %d is higher than the current hard limit or system-wide maximum", conf.FDLimit)
		default:
			util.Fatalf("Failed to set RLIMIT_NOFILE: %v", err)
		}
	}

	if err := unix.Chroot(root); err != nil {
		util.Fatalf("failed to chroot to %q: %v", root, err)
	}
	if err := unix.Chdir("/"); err != nil {
		util.Fatalf("changing working dir: %v", err)
	}
	log.Infof("Process chroot'd to %q", root)

	// Drop all supplementary groups (syscall.Setgroups applies to every thread)
	// so that an application with none matches ours and connects to host
	// sockets without a dedicated thread. If that is not permitted, e.g. rootless
	// with a non-zero sandbox UID, keep them and stop offering
	// ConnectWithGroups, whose per-thread setgroups(2) would fail the same way.
	if canSetGroups && conf.GetHostUDS().AllowOpen() {
		switch err := syscall.Setgroups(nil); err {
		case nil:
		case unix.EPERM:
			log.Warningf("Not permitted to drop supplementary groups, keeping them and not offering ConnectWithGroups")
			canSetGroups = false
		default:
			util.Fatalf("dropping supplementary groups: %v", err)
		}
	}

	ruid := unix.Getuid()
	euid := unix.Geteuid()
	rgid := unix.Getgid()
	egid := unix.Getegid()
	groups, err := unix.Getgroups()
	if err != nil {
		util.Fatalf("reading supplementary groups: %v", err)
	}
	log.Debugf("Process running as uid=%d euid=%d gid=%d egid=%d groups=%v canSetGroups=%t", ruid, euid, rgid, egid, groups, canSetGroups)

	// Initialize filters.
	opts := filter.Options{
		UDSOpenEnabled:   conf.GetHostUDS().AllowOpen(),
		UDSCreateEnabled: conf.GetHostUDS().AllowCreate(),
		ProfileEnabled:   profileOpts.Enabled(),
		DirectFS:         conf.DirectFS,
		LisafsNeeded:     lisafsNeeded,
		CgoEnabled:       config.CgoEnabled,
		VendorSyscalls:   filter.HostVendorSyscalls(),
	}
	for _, e := range extension.Registered() {
		opts.ExtraRules = append(opts.ExtraRules, e.SeccompRules())
	}
	if err := filter.Install(opts); err != nil {
		util.Fatalf("installing seccomp filters: %v", err)
	}

	// The gofer is chroot()ed into root, so it serves "/" and the mount
	// destinations, at which the mounts are bind-mounted.
	mountPath := func(m *specs.Mount) string { return m.Destination }
	return g.serve(spec, conf, root, "/", mountPath, ruid, euid, rgid, egid, groups, canSetGroups)
}

// openProcUmounter opens the /proc unmounter sidecar binary with O_PATH,
// which is sufficient to exec it.
func openProcUmounter() *os.File {
	b := &gvisorbinaries.ProcUmounter
	p, err := b.Path()
	if err != nil {
		util.Fatalf("sidecar %q not usable: %v", b.Name, err)
	}
	f, err := os.OpenFile(p, unix.O_PATH, 0)
	if err != nil {
		util.Fatalf("cannot open %q: %v", p, err)
	}
	return f
}

// spawnProcUnmounter executes the /proc unmounter process from the binary
// `umounter`, for gofers that will re-exec and would lose the capability to
// umount /proc on their own.
// It returns a function to clean up the unmounter process, which
// should be called (via defer) in case of errors.
func (g *goferSyncFDs) spawnProcUnmounter(umounter *os.File) func() {
	if g.procMountFD != -1 {
		util.Fatalf("procMountFD is set")
	}
	cmd, w := sandboxsetup.ExecProcUmounter(umounter)
	// Clear FD_CLOEXEC so procMountFD survives re-exec.
	if _, _, errno := unix.RawSyscall(unix.SYS_FCNTL, w.Fd(), unix.F_SETFD, 0); errno != 0 {
		util.Fatalf("error clearing CLOEXEC: %v", errno)
	}
	g.procMountFD = int(w.Fd())
	return func() {
		g.procMountFD = -1
		w.Close()
		cmd.Wait()
	}
}

// unmountProcfs signals the proc unmounter process that procfs is no longer
// needed. No-op when no unmounter process was spawned.
func (g *goferSyncFDs) unmountProcfs() {
	if g.procMountFD < 0 {
		return
	}
	sandboxsetup.UmountProc(g.procMountFD)
	g.procMountFD = -1
}

// syncUsernsForRootless waits on usernsFD to be closed and then sets
// UID/GID to uid/gid. Note that this function calls runtime.LockOSThread().
// This function is a no-op if usernsFD is -1.
//
// Postcondition: All callers must re-exec themselves after this returns,
// unless usernsFD was -1.
func (g *goferSyncFDs) syncUsernsForRootless(uid, gid uint32) {
	if g.usernsFD < 0 {
		return
	}
	sandboxsetup.SyncUsernsForRootless(g.usernsFD, uid, gid)
	g.usernsFD = -1
}

// syncChroot waits on chrootFD to be closed.
// Used for synchronization during container filesystem setup which is done
// from the non-gofer process.
// This function is a no-op if chrootFD is -1.
func (g *goferSyncFDs) syncChroot() {
	if g.chrootFD < 0 {
		return
	}
	if err := sandboxsetup.WaitForFD(g.chrootFD, "chroot sync FD"); err != nil {
		util.Fatalf("failed to sync on chroot FD: %v", err)
	}
	g.chrootFD = -1
}

// makeRPCMountOpener returns a MountOpener that opens mount sources via the
// gofer-to-host RPC channel.
func makeRPCMountOpener(goferToHostRPC *urpc.Client) sandboxsetup.MountOpener {
	return func(m *specs.Mount, flags uint32) (*os.File, error) {
		req := container.OpenMountArgs{
			Mount: m,
			Flags: flags,
		}
		var res container.OpenMountResult
		if err := goferToHostRPC.Call("goferToHostRPC.OpenMount", &req, &res); err != nil {
			return nil, fmt.Errorf("opening %s: %w", m.Source, err)
		}
		return res.Files[0], nil
	}
}
