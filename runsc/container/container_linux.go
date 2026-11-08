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

package container

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"

	specs "github.com/opencontainers/runtime-spec/specs-go"
	"golang.org/x/sys/unix"
	"gvisor.dev/gvisor/pkg/log"
	"gvisor.dev/gvisor/pkg/unet"
	"gvisor.dev/gvisor/pkg/urpc"
	"gvisor.dev/gvisor/runsc/boot/sentryapi"
	"gvisor.dev/gvisor/runsc/cgroup"
	"gvisor.dev/gvisor/runsc/config"
	"gvisor.dev/gvisor/runsc/donation"
	"gvisor.dev/gvisor/runsc/profile"
	"gvisor.dev/gvisor/runsc/sandbox"
	"gvisor.dev/gvisor/runsc/specutils"
	"gvisor.dev/gvisor/runsc/starttime"
)

// createLisafsSocketPair creates a socket pair for Lisafs communication and
// appends the sandbox end to sandEnds while donating the gofer end.
func createLisafsSocketPair(sandEnds *[]*os.File, donations *donation.Agency) error {
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return err
	}
	*sandEnds = append(*sandEnds, os.NewFile(uintptr(fds[0]), "sandbox IO FD"))

	goferEnd := os.NewFile(uintptr(fds[1]), "gofer IO FD")
	donations.DonateAndClose("io-fds", goferEnd)
	return nil
}

// startGoferProcess starts the gofer process of c, and returns the same
// values as createGoferProcess.
func (c *Container) startGoferProcess(conf *config.Config, specFile *os.File, mountHints *sentryapi.PodMountHints, attached bool, cloneIntoCgroupFD *os.File, cg cgroup.Cgroup, cpuLimits *sandbox.CPULimits, rootfsHint *sentryapi.RootfsHint) ([]*os.File, []*os.File, *os.File, *os.File, error) {
	// Ensure we don't leak FDs to the gofer process.
	if err := sandbox.SetCloExeOnAllFDs(); err != nil {
		return nil, nil, nil, nil, fmt.Errorf("setting CLOEXEC on all FDs: %w", err)
	}

	donations := donation.Agency{}
	defer donations.Close()

	if err := donations.DonateLogFile("log-fd", conf.LogFilename, os.O_CREATE|os.O_WRONLY|os.O_APPEND, &log.DefaultFileOpts{}); err != nil {
		return nil, nil, nil, nil, err
	}

	// The startTime here can mean one of two things:
	// - If this is the first gofer started at the same time as the sandbox,
	//   then this starttime will exactly match the one used by the sandbox
	//   itself (i.e. `Sandbox.StartTime`). This is desirable, such that the
	//   first gofer's log filename will have the exact same timestamp as
	//   the sandbox's log filename timestamp.
	// - If this is not the first gofer, then this starttime will be later
	//   than the sandbox start time; this is desirable such that we can
	//   distinguish the gofer log filenames between each other.
	// In either case, `starttime.Get` gets us the timestamp we want.
	lfOpts := &specutils.LogFileOpts{
		SandboxID: c.sandboxID(),
		CID:       c.ID,
		Command:   "gofer",
		Timestamp: starttime.Get(),
		Test:      specutils.TestName(conf, c.Spec),
	}

	if specutils.IsDebugCommand(conf, "gofer") {
		if err := donations.DonateDebugLogFile("debug-log-fd", conf.DebugLog, lfOpts); err != nil {
			return nil, nil, nil, nil, err
		}
	}

	// Start with the general config flags.
	cmd := exec.Command(specutils.ExePath, conf.ToFlags()...)
	cmd.Env = goferEnv(conf, os.Environ(), goferGOMAXPROCS(cg, cpuLimits))
	cmd.SysProcAttr = &unix.SysProcAttr{
		// Detach from session. Otherwise, signals sent to the foreground process
		// will also be forwarded by this process, resulting in duplicate signals.
		Setsid: true,
	}
	if cloneIntoCgroupFD != nil {
		cmd.SysProcAttr.UseCgroupFD = true
		cmd.SysProcAttr.CgroupFD = int(cloneIntoCgroupFD.Fd())
	}

	// Set Args[0] to make easier to spot the gofer process. Otherwise it's
	// shown as `exe`.
	cmd.Args[0] = "runsc-gofer"

	// Transfer FDs that need to be present before the "gofer" command.
	// Start at 3 because 0, 1, and 2 are taken by stdin/out/err.
	nextFD := donations.Transfer(cmd, 3)

	cmd.Args = append(cmd.Args, "gofer", "--bundle", c.BundleDir)
	cmd.Args = append(cmd.Args, "--gofer-mount-confs="+c.GoferMountConfs.String())

	donations.Donate("spec-fd", specFile)

	// Donate any profile FDs to the gofer.
	if err := profile.DonateProfileFDs(conf, &donations, true /* isGofer */, lfOpts); err != nil {
		return nil, nil, nil, nil, fmt.Errorf("donating gofer profile fds: %w", err)
	}

	// Create pipe that allows gofer to send mount list to sandbox after all paths
	// have been resolved.
	mountsSand, mountsGofer, err := os.Pipe()
	if err != nil {
		return nil, nil, nil, nil, err
	}
	donations.DonateAndClose("mounts-fd", mountsGofer)

	willNeedHostRPC, err := goferWillNeedHostRPC(c.Spec)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("cannot determine whether the gofer needs gofer-to-host RPCs: %w", err)
	}

	rpcPidCh := make(chan int, 1)
	defer close(rpcPidCh)
	if willNeedHostRPC {
		rpcServ, rpcClnt, err := unet.SocketPair(false)
		if err != nil {
			return nil, nil, nil, nil, fmt.Errorf("failed to create an rpc socket pair: %w", err)
		}
		rpcClntFD, _ := rpcClnt.Release()
		donations.DonateAndClose("rpc-fd", os.NewFile(uintptr(rpcClntFD), "gofer-rpc"))
		goferRPCDone := make(chan struct{})
		c.goferRPCDone = goferRPCDone
		go func() {
			defer close(goferRPCDone)
			pid := <-rpcPidCh
			if pid == 0 {
				rpcServ.Close()
				return
			}
			s := urpc.NewServer()
			s.Register(&goferToHostRPC{goferPID: pid})
			s.Handle(rpcServ)
		}()
	}

	// Count the number of mounts that needs an IO file.
	ioFileCount := 0
	for _, cfg := range c.GoferMountConfs {
		if cfg.ShouldUseLisafs() || cfg.ShouldUseErofs() {
			ioFileCount++
		}
	}

	sandEnds := make([]*os.File, 0, ioFileCount)

	// Handle rootfs (index 0)
	switch {
	case c.GoferMountConfs[0].ShouldUseLisafs():
		if err := createLisafsSocketPair(&sandEnds, &donations); err != nil {
			return nil, nil, nil, nil, err
		}

	case c.GoferMountConfs[0].ShouldUseErofs():
		f, err := os.Open(rootfsHint.Mount.Source)
		if err != nil {
			return nil, nil, nil, nil, fmt.Errorf("opening rootfs image %q: %v", rootfsHint.Mount.Source, err)
		}
		sandEnds = append(sandEnds, f)
	}

	// Handle sub mounts
	cfgIdx := 1
	for _, m := range c.Spec.Mounts {
		if !specutils.HasMountConfig(m) {
			continue
		}

		switch {
		case c.GoferMountConfs[cfgIdx].ShouldUseLisafs():
			if err := createLisafsSocketPair(&sandEnds, &donations); err != nil {
				return nil, nil, nil, nil, err
			}

		case c.GoferMountConfs[cfgIdx].ShouldUseErofs():
			f, err := os.Open(m.Source)
			if err != nil {
				return nil, nil, nil, nil, fmt.Errorf("opening EROFS image %q: %v", m.Source, err)
			}
			sandEnds = append(sandEnds, f)
		}
		cfgIdx++
	}
	var devSandEnd *os.File
	if shouldCreateDeviceGofer(c.Spec, conf) {
		fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
		if err != nil {
			return nil, nil, nil, nil, err
		}
		devSandEnd = os.NewFile(uintptr(fds[0]), "sandbox dev IO FD")
		donations.DonateAndClose("dev-io-fd", os.NewFile(uintptr(fds[1]), "gofer dev IO FD"))
	}

	if attached {
		// The gofer is attached to the lifetime of this process, so it
		// should synchronously die when this process dies.
		cmd.SysProcAttr.Pdeathsig = unix.SIGKILL
	}

	// Enter new namespaces to isolate from the rest of the system. Don't unshare
	// cgroup because gofer is added to a cgroup in the caller's namespace.
	nss := []specs.LinuxNamespace{
		{Type: specs.IPCNamespace},
		{Type: specs.MountNamespace},
		{Type: specs.PIDNamespace},
		{Type: specs.UTSNamespace},
	}
	goferNetNS, goferNetNSFile, pinGoferNetNS := goferNetworkNamespace(conf)
	if goferNetNSFile != nil {
		// goferNetNS.Path must remain open until the gofer has started.
		defer goferNetNSFile.Close()
	}
	if goferNetNS != nil {
		nss = append(nss, *goferNetNS)
	}

	rootlessEUID := unix.Geteuid() != 0
	setUserMappings := false
	// Setup any uid/gid mappings, and create or join the configured user
	// namespace so the gofer's view of the filesystem aligns with the
	// users in the sandbox.
	if !rootlessEUID {
		if userNS, ok := specutils.GetNS(specs.UserNamespace, c.Spec); ok {
			nss = append(nss, userNS)
			specutils.SetUIDGIDMappings(cmd, c.Spec)
			// We need to set UID and GID to have capabilities in a new user namespace.
			cmd.SysProcAttr.Credential = &syscall.Credential{Uid: 0, Gid: 0}
			if conf.GetHostUDS().AllowOpen() && len(cmd.SysProcAttr.GidMappings) > 0 && specutils.SetgroupsAllowed() {
				// Connecting to a host socket applies the application's supplementary
				// groups, which setgroups(2) refuses in a user namespace that denies
				// it. Allowing it also makes os/exec start the gofer with no
				// supplementary groups.
				cmd.SysProcAttr.GidMappingsEnableSetgroups = true
			}
		}
	} else {
		userNS, ok := specutils.GetNS(specs.UserNamespace, c.Spec)
		if !ok {
			return nil, nil, nil, nil, fmt.Errorf("unable to run a rootless container without userns")
		}
		nss = append(nss, userNS)
		if sandbox.CanUseUnprivilegedMapping(c.Spec) {
			specutils.SetUIDGIDMappings(cmd, c.Spec)
			cmd.SysProcAttr.GidMappingsEnableSetgroups = false
		} else {
			setUserMappings = true
		}
		syncFile, err := sandbox.ConfigureCmdForRootless(cmd, &donations)
		if err != nil {
			return nil, nil, nil, nil, err
		}
		defer syncFile.Close()
		uid, gid := sandbox.SandboxUserGroupIDs(c.Spec)
		if uid != 0 {
			cmd.Args = append(cmd.Args, fmt.Sprintf("--uid=%d", uid))
		}
		if gid != 0 {
			cmd.Args = append(cmd.Args, fmt.Sprintf("--gid=%d", gid))
		}
	}

	// Create synchronization FD for chroot.
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	chrootSyncSandEnd := os.NewFile(uintptr(fds[0]), "chroot sync runsc FD")
	chrootSyncGoferEnd := os.NewFile(uintptr(fds[1]), "chroot sync gofer FD")
	donations.DonateAndClose("sync-chroot-fd", chrootSyncGoferEnd)
	defer chrootSyncSandEnd.Close()

	donations.Transfer(cmd, nextFD)

	// Add container ID as the last argument.
	cmd.Args = append(cmd.Args, c.ID)
	log.Infof("Starting gofer with command: %v", cmd.Args)

	// Start the gofer in the given namespace.
	donation.LogDonations(cmd)

	log.Debugf("Starting gofer: %s %v", cmd.Path, cmd.Args)
	if err := specutils.StartInNS(cmd, nss); err != nil {
		return nil, nil, nil, nil, fmt.Errorf("gofer: %v", err)
	}
	log.Infof("Gofer started, PID: %d", cmd.Process.Pid)
	c.GoferPid.Store(cmd.Process.Pid)
	c.goferIsChild = true
	if c.GoferPidIdentity, err = specutils.ProcessIdentity(cmd.Process.Pid); err != nil {
		return nil, nil, nil, nil, err
	}
	rpcPidCh <- cmd.Process.Pid

	if pinGoferNetNS {
		// The gofer was started in a new empty network namespace.
		// Pin it, future gofers will join it instead of creating a new one.
		if err := pinNullNetNS(conf, cmd.Process.Pid); err != nil {
			log.Warningf("Unable to pin the gofer's network namespace at %q (%v); future gofers will create new network namespaces. This slows down gVisor startup.", nullNetNSPath(conf), err)
		} else {
			log.Infof("Pinned null network namespace at %q", nullNetNSPath(conf))
		}
	}

	// Set up and synchronize rootless mode userns mappings.
	if setUserMappings {
		if err := sandbox.SetUserMappings(c.Spec, cmd.Process.Pid); err != nil {
			return nil, nil, nil, nil, err
		}
	}

	// Set up nvproxy with the Gofer's mount namespaces while chrootSyncSandEnd is
	// is still open.
	if err := nvproxySetup(c.Spec, conf, c.GoferPid.Load()); err != nil {
		return nil, nil, nil, nil, fmt.Errorf("setting up nvproxy for gofer: %w", err)
	}

	// Create gofer filestore files with the Gofer's mount namespaces while
	// chrootSyncSandEnd is still open.
	goferFilestores, err := c.createGoferFilestores(conf.GetOverlay2(), mountHints)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("creating gofer filestore files: %w", err)
	}

	return sandEnds, goferFilestores, devSandEnd, mountsSand, nil
}

// goferNetworkNamespace returns the network namespace that the gofer process
// should be started in.
// A nil namespace means to stay in the current network namespace.
// The returned `os.File` keeps the namespace referred to by the returned
// namespace's Path alive, so it must be kept open until gofer start.
// If `pinNetNS` is true, the gofer should be started in a new network
// namespace, which should then be pinned as the shared "null" network
// namespace (see `pinNullNetNS`) once the gofer starts.
func goferNetworkNamespace(conf *config.Config) (ns *specs.LinuxNamespace, nsFile *os.File, pinNetNS bool) {
	switch conf.GoferNetworkNamespace {
	case config.GoferNetworkNamespaceNew:
		return &specs.LinuxNamespace{Type: specs.NetworkNamespace}, nil, false
	case config.GoferNetworkNamespaceHost:
		return nil, nil, false
	case config.GoferNetworkNamespaceNull:
		nsFile, err := openNullNetNS(nullNetNSPath(conf))
		if err != nil {
			if !os.IsNotExist(err) {
				// Unexpected error.
				log.Warningf("No usable null network namespace at %q (%v); creating a new one. This issue is slowing down your sandboxes' startup.", nullNetNSPath(conf), err)
			} else if conf.SharedRootDir == "" && conf.RootDir != config.DefaultRootDir() {
				// User has customized `--root` but not `--shared-root`, so they are likely missing out on
				// the performance benefits of a shared root directory.
				log.Warningf("gVisor startup is slower if `--root` (or `--shared-root`) is not reused across multiple sandboxes. You can speed up sandbox startup by setting `--shared-root` to a shared system-wide directory so that the null gofer network namespace is reused across sandboxes.")
			} else {
				// Informational-only as this is expected on first sandbox creation.
				log.Infof("No usable null network namespace at %q (%v); creating a new one. This is expected if this sandbox is the first sandbox to start with this `--shared-root` directory (%v). If this is not the first sandbox, this issue is slowing down your sandboxes' startup.", nullNetNSPath(conf), err, conf.SharedRoot())
			}
			return &specs.LinuxNamespace{Type: specs.NetworkNamespace}, nil, true
		}
		return &specs.LinuxNamespace{
			Type: specs.NetworkNamespace,
			// Refer to the namespace through our own open FD so that
			// it stays valid even if racing with an unmount.
			Path: fmt.Sprintf("/proc/self/fd/%d", nsFile.Fd()),
		}, nsFile, false
	default:
		return &specs.LinuxNamespace{
			Type: specs.NetworkNamespace,
			Path: string(conf.GoferNetworkNamespace),
		}, nil, false
	}
}

// goferRootfsPath returns the host path of the root filesystem of the gofer
// process pid.
func goferRootfsPath(pid int) string {
	return fmt.Sprintf("/proc/%d/root", pid)
}

// createFileBeneath creates the file name in the directory dirFD, with mode
// 0666 and the open(2) flags. It does not follow symlinks or cross mounts.
func createFileBeneath(dirFD int, name string, flags int) (int, error) {
	return unix.Openat2(dirFD, name, &unix.OpenHow{
		Flags:   uint64(flags),
		Mode:    0666,
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS | unix.RESOLVE_NO_XDEV,
	})
}
