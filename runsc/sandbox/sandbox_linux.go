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

// Package sandbox creates and manipulates sandboxes.
//go:build linux

package sandbox

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"

	"github.com/moby/sys/capability"
	specs "github.com/opencontainers/runtime-spec/specs-go"
	"golang.org/x/sys/unix"
	"gvisor.dev/gvisor/pkg/abi/linux"
	"gvisor.dev/gvisor/pkg/cleanup"
	"gvisor.dev/gvisor/pkg/coverage"
	"gvisor.dev/gvisor/pkg/hostos"
	"gvisor.dev/gvisor/pkg/log"
	"gvisor.dev/gvisor/pkg/pinring"
	"gvisor.dev/gvisor/pkg/sentry/platform/platformdesc"
	"gvisor.dev/gvisor/pkg/sentry/state/checkpointfiles"
	"gvisor.dev/gvisor/pkg/state/statefile"
	"gvisor.dev/gvisor/runsc/cgroup"
	"gvisor.dev/gvisor/runsc/config"
	"gvisor.dev/gvisor/runsc/console"
	"gvisor.dev/gvisor/runsc/donation"
	"gvisor.dev/gvisor/runsc/gvisorbinaries"
	"gvisor.dev/gvisor/runsc/profile"
	"gvisor.dev/gvisor/runsc/specutils"
	"gvisor.dev/gvisor/runsc/starttime"
)

// openReadOnlyTree returns an FD for a detached, read-only copy of the mount
// of dir, rooted at dir. Files opened through it refer to a read-only mount,
// so they can not be opened for writing again, by anyone, through any path.
// The copy stays alive as long as any file opened through it does.
func openReadOnlyTree(dir string) (int, error) {
	treeFD, err := unix.OpenTree(unix.AT_FDCWD, dir, unix.OPEN_TREE_CLONE|unix.OPEN_TREE_CLOEXEC)
	if err != nil {
		return -1, err
	}
	attr := unix.MountAttr{Attr_set: unix.MOUNT_ATTR_RDONLY}
	if err := unix.MountSetattr(treeFD, "", unix.AT_EMPTY_PATH, &attr); err != nil {
		unix.Close(treeFD)
		return -1, err
	}
	return treeFD, nil
}

// openRestoreDir returns an FD for the checkpoint directory dir through which
// the files handed to the sandbox for a restore are opened. The sandbox only
// reads them and must not be able to modify them, which file permissions do
// not guarantee since it may run as root with CAP_DAC_OVERRIDE. So this is a
// detached, read-only copy of dir's mount (see openReadOnlyTree) when the
// kernel and runsc's privileges allow it, and dir itself otherwise.
func openRestoreDir(dir string) (int, error) {
	dirFD, err := openReadOnlyTree(dir)
	if err == nil {
		return dirFD, nil
	}
	log.Warningf("Checkpoint files in %q are not protected from modification by the sandbox: %v", dir, err)
	return unix.Open(dir, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
}

// openCheckpointFile opens the checkpoint file name in dir, relative to dirFD,
// an FD for dir. The path can not lead out of dir, so the file is on dirFD's
// mount.
func openCheckpointFile(dirFD int, dir, name string, flags int) (*os.File, error) {
	how := unix.OpenHow{
		Flags:   uint64(flags | unix.O_CLOEXEC),
		Resolve: unix.RESOLVE_BENEATH,
	}
	if flags&unix.O_CREAT != 0 {
		how.Mode = 0644
	}
	fd, err := unix.Openat2(dirFD, name, &how)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), filepath.Join(dir, name)), nil
}

// openPagesFile opens the checkpoint pages file for reading. Its contents are
// page-aligned, so it can be opened with O_DIRECT.
func openPagesFile(dirFD int, imagePath string, directIO DirectIO) (*os.File, error) {
	if directIO != DirectIOOff {
		pf, err := openCheckpointFile(dirFD, imagePath, checkpointfiles.PagesFileName, unix.O_RDONLY|unix.O_DIRECT)
		if err == nil || directIO == DirectIOOn || !errors.Is(err, unix.EINVAL) {
			return pf, err
		}
		// EINVAL means that the filesystem does not support O_DIRECT.
		log.Infof("Reading pages file %q through the page cache: O_DIRECT is not supported", path.Join(imagePath, checkpointfiles.PagesFileName))
	}
	return openCheckpointFile(dirFD, imagePath, checkpointfiles.PagesFileName, unix.O_RDONLY)
}

// sentryExeFD is the FD at which the prewarmer inherits the Sentry binary.
// Must match `SENTRY_EXE_FD` in `runsc/prewarmer/prewarmer.c`.
// LINT.IfChange
const sentryExeFD = 3

// LINT.ThenChange(../prewarmer/prewarmer.c)

// createSandboxProcess starts the sandbox as a subprocess by running the "boot"
// command, passing in the bundle dir.
func (s *Sandbox) createSandboxProcess(conf *config.Config, args *Args) error {
	// Ensure we don't leak FDs to the sandbox process.
	if err := SetCloExeOnAllFDs(); err != nil {
		return fmt.Errorf("setting CLOEXEC on all FDs: %w", err)
	}

	donations := donation.Agency{}
	defer donations.Close()

	// pgalloc.MemoryFile (which provides application memory) sometimes briefly
	// mlock(2)s ranges of memory in order to fault in a large number of pages at
	// a time. Try to make RLIMIT_MEMLOCK unlimited so that it can do so. runsc
	// expects to run in a memory cgroup that limits its memory usage as
	// required.
	// This needs to be done before exec'ing the Sentry, as the Sentry
	// runs as an unprivileged user that will not be able to call `setrlimit`
	// by itself. Calling `setrlimit` here will have the side-effect of setting
	// the limit on the currently-running `runsc` process as well, but that
	// should be OK too.
	var rlim unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_MEMLOCK, &rlim); err != nil {
		log.Warningf("Failed to get RLIMIT_MEMLOCK: %v", err)
	} else if rlim.Cur != unix.RLIM_INFINITY || rlim.Max != unix.RLIM_INFINITY {
		rlim.Cur = unix.RLIM_INFINITY
		rlim.Max = unix.RLIM_INFINITY
		if err := unix.Setrlimit(unix.RLIMIT_MEMLOCK, &rlim); err != nil {
			// We may not have CAP_SYS_RESOURCE, so this failure may be expected.
			log.Infof("Failed to set RLIMIT_MEMLOCK: %v", err)
		}
	}

	//
	// These flags must come BEFORE the "boot" command in cmd.Args.
	//

	// Open the log files to pass to the sandbox as FDs.
	if err := donations.DonateLogFile("log-fd", conf.LogFilename, os.O_CREATE|os.O_WRONLY|os.O_APPEND, &log.DefaultFileOpts{}); err != nil {
		return fmt.Errorf("failed to opening or donating log file %q: %w", conf.LogFilename, err)
	}
	lfOpts := &specutils.LogFileOpts{
		SandboxID: s.ID,
		CID:       s.ID,
		Command:   "boot",
		Timestamp: s.StartTime,
		Test:      specutils.TestName(conf, args.Spec),
	}
	if specutils.IsDebugCommand(conf, "boot") {
		if err := donations.DonateDebugLogFile("debug-log-fd", conf.DebugLog, lfOpts); err != nil {
			return fmt.Errorf("donating debug log file: %w", err)
		}
	}
	lfOpts.Command = "panic"
	if err := donations.DonateDebugLogFile("panic-log-fd", conf.PanicLog, lfOpts); err != nil {
		return fmt.Errorf("donating panic log file: %w", err)
	}
	covFilename := conf.CoverageReport
	if covFilename == "" {
		covFilename = os.Getenv("GO_COVERAGE_FILE")
	}
	if covFilename != "" && coverage.Available() {
		lfOpts.Command = "cov"
		if err := donations.DonateDebugLogFile("coverage-fd", covFilename, lfOpts); err != nil {
			return fmt.Errorf("donating coverage log file: %w", err)
		}
	}
	lfOpts.Command = "boot" // Revert command to "boot".

	sentryBin := sentryBinary(conf)
	// runsc cannot tell whether the Sentry sidecar uses cgo, so assume it
	// does if runsc does: race builds pair a race runsc with a race Sentry.
	// Caveats: a cgo runsc booting a pure Sentry (e.g. a cgo test binary)
	// sets GLIBC_TUNABLES needlessly, which is harmless. A pure runsc
	// booting a cgo Sentry other than the plugin stack does not set it,
	// so that Sentry's stubs would crash; no build pairs them today.
	sentryUsesCgo := config.CgoEnabled
	if conf.Network == config.NetworkPlugin {
		sentryUsesCgo = true
	}
	bootBinPath, err := sentryBin.Path()
	if err != nil {
		return fmt.Errorf("sidecar %q not usable: %w", sentryBin.Name, err)
	}
	log.Infof("Sidecar %q found: booting sandbox with %s", sentryBin.Name, bootBinPath)
	// Open with `O_PATH`, which is sufficient for exec and makes the FD
	// effectively execute-only (no read/write).
	bootBin, err := os.OpenFile(bootBinPath, unix.O_PATH, 0)
	if err != nil {
		return fmt.Errorf("cannot open boot binary %q: %w", bootBinPath, err)
	}
	defer bootBin.Close()
	prewarmerPath, err := gvisorbinaries.GvisorSentryPrewarmer.Path()
	if err != nil {
		return fmt.Errorf("sidecar %q not usable: %w", gvisorbinaries.GvisorSentryPrewarmer.Name, err)
	}
	log.Infof("Sidecar %q found: prepending Sentry boot command with %s", gvisorbinaries.GvisorSentryPrewarmer.Name, prewarmerPath)

	// Relay all the config flags to the sandbox process.
	cmd := exec.Command(prewarmerPath, conf.ToFlags()...)
	cmd.ExtraFiles = []*os.File{bootBin} // Gets FD number `sentryExeFD`.
	cmd.SysProcAttr = &unix.SysProcAttr{
		// Detach from this session, otherwise cmd will get SIGHUP and SIGCONT
		// when re-parented.
		Setsid: true,
	}
	if args.CloneIntoCgroupFD != nil {
		cmd.SysProcAttr.UseCgroupFD = true
		cmd.SysProcAttr.CgroupFD = int(args.CloneIntoCgroupFD.Fd())
	}

	// Set Args[0] to make easier to spot the sandbox process.
	cmd.Args[0] = "runsc-sandbox"

	// Transfer FDs that need to be present before the "boot" command.
	nextFD := donations.Transfer(cmd, sentryExeFD+1)

	// Add the "boot" command to the args.
	//
	// All flags after this must be for the boot command
	cmd.Args = append(cmd.Args, "boot", "--bundle="+args.BundleDir)
	if args.NoRootContainer {
		cmd.Args = append(cmd.Args, "--no-root-container")
	}

	cmd.Env = sandboxProcessEnv(conf, sandboxProcessEnvOptions{
		sentryUsesCgo: sentryUsesCgo,
	})

	// If there is a gofer, sends all socket ends to the sandbox.
	donations.DonateAndClose("io-fds", args.IOFiles...)
	donations.DonateAndClose("dev-io-fd", args.DevIOFile)
	donations.DonateAndClose("gofer-filestore-fds", args.GoferFilestoreFiles...)
	donations.DonateAndClose("mounts-fd", args.MountsFile)
	var pinRing *os.File
	if _, err := gvisorbinaries.FDParking.Path(); err != nil {
		log.Warningf("Sidecar %q not found or usable (%v). This slows down gVisor sandbox teardown.", gvisorbinaries.FDParking.Name, err)
	} else if ring, err := pinring.NewDisabledIOURing(); err != nil {
		log.Warningf("Cannot create disabled io_uring ring: %v. This slows down gVisor sandbox teardown.", err)
	} else {
		pinRing = ring
		defer pinRing.Close()
		donations.Donate("pin-ring-fd", pinRing)
	}
	if err := donations.DonateLogFile("user-log-fd", args.UserLog, os.O_CREATE|os.O_WRONLY|os.O_APPEND, lfOpts); err != nil {
		return err
	}
	if err := profile.DonateProfileFDs(conf, &donations, false /* isGofer */, lfOpts); err != nil {
		return fmt.Errorf("donating profile FDs: %w", err)
	}
	if err := donations.DonateLogFile("final-metrics-log-fd", conf.FinalMetricsLog, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, lfOpts); err != nil {
		return fmt.Errorf("donating final metrics log file: %w", err)
	}
	if err := donations.DonateLogFile("boot-times-log-fd", conf.BootTimesLog, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, lfOpts); err != nil {
		return fmt.Errorf("donating boot times log file: %w", err)
	}
	rootfsTarPath := specutils.RootfsTarUpperPath(args.Spec)
	if err := donations.DonateLogFile("rootfs-upper-tar-fd", rootfsTarPath, rootfsUpperTarFlags(rootfsTarPath), lfOpts); err != nil {
		return fmt.Errorf("donating rootfs tar file: %w", err)
	}

	// Pass gofer mount configs.
	if len(args.GoferMountConfs) > 0 {
		cmd.Args = append(cmd.Args, "--gofer-mount-confs="+args.GoferMountConfs.String())
	}

	// Create a socket for the control server and donate it to the sandbox.
	controlSocketPath, sockFD, err := createControlSocket(conf.RootDir, s.ID)
	if err != nil {
		return fmt.Errorf("failed to create control socket: %v", err)
	}
	s.ControlSocketPath = controlSocketPath
	log.Infof("Control socket path: %q", s.ControlSocketPath)
	donations.DonateAndClose("controller-fd", os.NewFile(uintptr(sockFD), "control_server_socket"))

	donations.Donate("spec-fd", args.SpecFile)

	if err := donations.DonateLogFile("pod-init-config-fd", conf.PodInitConfig, os.O_RDONLY, lfOpts); err != nil {
		return err
	}
	donations.DonateAndClose("sink-fds", args.SinkFiles...)

	if len(conf.TestOnlyAutosaveImagePath) != 0 {
		files, err := createSaveFiles(conf.TestOnlyAutosaveImagePath, false, statefile.CompressionLevelFlateBestSpeed)
		if err != nil {
			return fmt.Errorf("failed to create auto save files: %w", err)
		}
		donations.DonateAndClose("save-fds", files...)
	}

	fsRestorePaths := slices.DeleteFunc(slices.Clone(args.FSRestoreImagePaths), func(p string) bool { return p == "" })
	if len(fsRestorePaths) > 0 {
		s.FSRestore = true
		files, err := s.openFSRestoreFiles(conf, fsRestorePaths, args.FSRestoreDirect, cmd)
		if err != nil {
			return fmt.Errorf("failed to open filesystem checkpoint files: %w", err)
		}
		donations.DonateAndClose("fs-restore-fds", files...)
	}

	if err := s.maybeConfigureSandboxProcessForWorkloadTriggerSave(conf, args, cmd, &donations); err != nil {
		return err
	}
	if err := s.maybeConfigureSandboxProcessForWorkloadTriggerFSSave(conf, args, cmd, &donations); err != nil {
		return err
	}

	gPlatform, err := platformdesc.Lookup(conf.Platform)
	if err != nil {
		return fmt.Errorf("cannot look up platform: %w", err)
	}
	if deviceFile, err := gPlatform.OpenDevice(conf.PlatformDeviceFor(conf.Platform)); err != nil {
		return fmt.Errorf("opening device file for platform %q: %v", conf.Platform, err)
	} else if deviceFile != nil {
		donations.DonateAndClose("device-fd", deviceFile.ReleaseToFile("device file"))
	}

	// Go only reads the first GODEBUG variable in the environment.
	cmd.Env = append(cmd.Env, "GODEBUG="+strings.Join(sentryGODEBUG(conf), ","))

	// nss is the set of namespaces to join or create before starting the sandbox
	// process. Mount, IPC and UTS namespaces from the host are not used as they
	// are virtualized inside the sandbox. Be paranoid and run inside an empty
	// namespace for these. Don't unshare cgroup because sandbox is added to a
	// cgroup in the caller's namespace.
	log.Infof("Sandbox will be started in new mount, IPC and UTS namespaces")
	nss := []specs.LinuxNamespace{
		{Type: specs.IPCNamespace},
		{Type: specs.MountNamespace},
		{Type: specs.UTSNamespace},
		{Type: specs.PIDNamespace},
	}

	if specutils.NVProxyEnabled(args.Spec, conf) {
		version, err := getNvproxyDriverVersion(conf)
		if err != nil {
			return fmt.Errorf("failed to get Nvidia driver version: %w", err)
		}
		cmd.Args = append(cmd.Args, "--nvidia-driver-version="+version)
	}

	// Joins the network namespace if network is enabled. the sandbox talks
	// directly to the host network, which may have been configured in the
	// namespace.
	if ns, ok := specutils.GetNS(specs.NetworkNamespace, args.Spec); ok && conf.Network != config.NetworkNone {
		log.Infof("Sandbox will be started in the container's network namespace: %+v", ns)
		nss = append(nss, ns)
	} else if conf.Network == config.NetworkHost {
		log.Infof("Sandbox will be started in the host network namespace")
	} else {
		log.Infof("Sandbox will be started in new network namespace")
		nss = append(nss, specs.LinuxNamespace{Type: specs.NetworkNamespace})
	}

	// These are set to the uid/gid that the sandbox process will use. May be
	// overridden below.
	s.UID = os.Getuid()
	s.GID = os.Getgid()

	// User namespace depends on the network type or whether access to the host
	// filesystem is required. These features require to run inside the user
	// namespace specified in the spec or the current namespace if none is
	// configured.
	rootlessEUID := unix.Geteuid() != 0
	setUserMappings := false
	if conf.Network == config.NetworkHost || conf.DirectFS {
		if userns, ok := specutils.GetNS(specs.UserNamespace, args.Spec); ok {
			log.Infof("Sandbox will be started in container's user namespace: %+v", userns)
			nss = append(nss, userns)
			if rootlessEUID {
				if CanUseUnprivilegedMapping(args.Spec) {
					specutils.SetUIDGIDMappings(cmd, args.Spec)
					cmd.SysProcAttr.GidMappingsEnableSetgroups = false
				} else {
					setUserMappings = true
				}
				syncFile, err := ConfigureCmdForRootless(cmd, &donations)
				if err != nil {
					return err
				}
				defer syncFile.Close()
				uid, gid := SandboxUserGroupIDs(args.Spec)
				if uid != 0 {
					cmd.Args = append(cmd.Args, fmt.Sprintf("--uid=%d", uid))
				}
				if gid != 0 {
					cmd.Args = append(cmd.Args, fmt.Sprintf("--gid=%d", gid))
				}
			} else {
				specutils.SetUIDGIDMappings(cmd, args.Spec)
				// We need to set UID and GID to have capabilities in a new user namespace.
				cmd.SysProcAttr.Credential = &syscall.Credential{Uid: 0, Gid: 0}
			}
		} else {
			if rootlessEUID {
				return fmt.Errorf("unable to run a rootless container without userns")
			}
			log.Infof("Sandbox will be started in the current user namespace")
		}
		// When running in the caller's defined user namespace, apply the same
		// capabilities to the sandbox process to ensure it abides to the same
		// rules.
		cmd.Args = append(cmd.Args, "--apply-caps=true")

		// If we have CAP_SYS_ADMIN, we can create an empty chroot and
		// bind-mount the executable inside it.
		if conf.TestOnlyAllowRunAsCurrentUserWithoutChroot {
			log.Warningf("Running sandbox in test mode without chroot. This is only safe in tests!")
		} else if specutils.HasCapabilities(capability.CAP_SYS_ADMIN) || rootlessEUID {
			log.Infof("Sandbox will be started in minimal chroot")
			cmd.Args = append(cmd.Args, "--setup-root")
			if err := donateProcUmounter(conf, &donations); err != nil {
				return err
			}
		} else {
			return fmt.Errorf("can't run sandbox process in minimal chroot since we don't have CAP_SYS_ADMIN")
		}
	} else {
		// If we have CAP_SETUID and CAP_SETGID, then we can also run
		// as user nobody.
		if conf.TestOnlyAllowRunAsCurrentUserWithoutChroot {
			log.Warningf("Running sandbox in test mode as current user (uid=%d gid=%d). This is only safe in tests!", os.Getuid(), os.Getgid())
			log.Warningf("Running sandbox in test mode without chroot. This is only safe in tests!")
		} else if rootlessEUID || specutils.HasCapabilities(capability.CAP_SETUID, capability.CAP_SETGID) {
			log.Infof("Sandbox will be started in new user namespace")
			nss = append(nss, specs.LinuxNamespace{Type: specs.UserNamespace})
			cmd.Args = append(cmd.Args, "--setup-root")
			if err := donateProcUmounter(conf, &donations); err != nil {
				return err
			}

			const nobody = 65534
			if rootlessEUID || conf.Rootless {
				log.Infof("Rootless mode: sandbox will run as nobody inside user namespace, mapped to the current user, uid: %d, gid: %d", os.Getuid(), os.Getgid())
			} else {
				// Map nobody in the new namespace to nobody in the parent namespace.
				s.UID = nobody
				s.GID = nobody
			}

			// Set credentials to run as user and group nobody.
			cmd.SysProcAttr.Credential = &syscall.Credential{Uid: nobody, Gid: nobody}
			cmd.SysProcAttr.UidMappings = []syscall.SysProcIDMap{
				{
					ContainerID: nobody,
					HostID:      s.UID,
					Size:        1,
				},
			}
			cmd.SysProcAttr.GidMappings = []syscall.SysProcIDMap{
				{
					ContainerID: nobody,
					HostID:      s.GID,
					Size:        1,
				},
			}

			// A sandbox process will construct an empty root for itself, so it has
			// to have CAP_SYS_ADMIN and CAP_SYS_CHROOT capabilities.
			cmd.SysProcAttr.AmbientCaps = append(cmd.SysProcAttr.AmbientCaps,
				uintptr(capability.CAP_SYS_ADMIN),
				uintptr(capability.CAP_SYS_CHROOT),
				// CAP_SETPCAP is required to clear the bounding set.
				uintptr(capability.CAP_SETPCAP),
			)
			if gPlatform.Requirements.RequiresCapSysPtrace {
				cmd.SysProcAttr.AmbientCaps = append(cmd.SysProcAttr.AmbientCaps,
					uintptr(capability.CAP_SYS_PTRACE))
			}
		} else {
			return fmt.Errorf("can't run sandbox process as user nobody since we don't have CAP_SETUID or CAP_SETGID")
		}
	}

	// The current process' stdio must be passed to the application via the
	// --stdio-fds flag. The stdio of the sandbox process itself must not
	// be connected to the same FDs, otherwise we risk leaking sandbox
	// errors to the application, so we set the sandbox stdio to nil,
	// causing them to read/write from the null device.
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	var stdios [3]*os.File

	// If the console control socket file is provided, then create a new
	// pty master/replica pair and set the TTY on the sandbox process.
	if args.Spec.Process != nil && args.Spec.Process.Terminal && args.ConsoleSocket != "" {
		// console.NewWithSocket will send the master on the given
		// socket, and return the replica.
		tty, err := console.NewWithSocket(args.ConsoleSocket)
		if err != nil {
			return fmt.Errorf("setting up console with socket %q: %v", args.ConsoleSocket, err)
		}
		defer tty.Close()

		// Set the TTY as a controlling TTY on the sandbox process.
		cmd.SysProcAttr.Setctty = true

		// Inconveniently, the Ctty must be the FD in the *child* process's FD
		// table. So transfer all files we have so far and make sure the next file
		// added to donations is stdin.
		//
		// See https://github.com/golang/go/issues/29458.
		nextFD = donations.Transfer(cmd, nextFD)
		cmd.SysProcAttr.Ctty = nextFD

		// Pass the tty as all stdio fds to sandbox.
		stdios[0] = tty
		stdios[1] = tty
		stdios[2] = tty

		if conf.Debug {
			// If debugging, send the boot process stdio to the
			// TTY, so that it is easier to find.
			cmd.Stdin = tty
			cmd.Stdout = tty
			cmd.Stderr = tty
		}
	} else {
		// If not using a console, pass our current stdio as the
		// container stdio via flags.
		stdios[0] = os.Stdin
		stdios[1] = os.Stdout
		stdios[2] = os.Stderr

		if conf.Debug {
			// If debugging, send the boot process stdio to the
			// this process' stdio, so that is is easier to find.
			cmd.Stdin = os.Stdin
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
		}
	}
	if err := s.configureStdios(conf, stdios[:]); err != nil {
		return fmt.Errorf("configuring stdios: %w", err)
	}
	// Note: this must be done right after "cmd.SysProcAttr.Ctty" is set above
	// because it relies on stdin being the next FD donated.
	donations.Donate("stdio-fds", stdios[:]...)
	if conf.ProfilingMetricsLog == "-" {
		donations.Donate("profiling-metrics-fd", stdios[1])
		cmd.Args = append(cmd.Args, "--profiling-metrics-fd-lossy=true")
	} else if conf.ProfilingMetricsLog != "" {
		lfOpts.Command = "metrics"
		if err := donations.DonateDebugLogFile("profiling-metrics-fd", conf.ProfilingMetricsLog, lfOpts); err != nil {
			return err
		}
		lfOpts.Command = "boot"
		cmd.Args = append(cmd.Args, "--profiling-metrics-fd-lossy=false")
	}

	totalSysMem, err := hostos.TotalSystemMemory()
	if err != nil {
		return err
	}
	cmd.Args = append(cmd.Args, "--total-host-memory", strconv.FormatUint(totalSysMem, 10))

	mem := totalSysMem
	if s.CgroupJSON.Cgroup != nil {
		cgCPUNum := cgroupCPUNum(s.CgroupJSON.Cgroup)
		cpuNum, cpuQuota, cpuPeriod, err := calculateCPUNum(s.CgroupJSON.Cgroup, cgCPUNum, s.CPULimits, conf.CPUNumFromQuota)
		if err != nil {
			return err
		}
		cmd.Args = append(cmd.Args, "--cpu-num", strconv.Itoa(cpuNum))
		if s.CPULimits != nil {
			cmd.Args = append(cmd.Args, "--lifecycle-cpu-num", strconv.Itoa(s.CPULimits.lifecycleCPUNum(cgCPUNum)))
		}
		cmd.Env = append(cmd.Env, "GOMAXPROCS="+strconv.Itoa(cpuNum))
		if cpuQuota > 0 {
			cmd.Args = append(cmd.Args, "--cpu-quota", strconv.FormatInt(cpuQuota, 10))
		}
		if cpuPeriod > 0 {
			cmd.Args = append(cmd.Args, "--cpu-period", strconv.FormatInt(cpuPeriod, 10))
		}

		memLimit, err := s.CgroupJSON.Cgroup.MemoryLimit()
		if err != nil {
			return fmt.Errorf("getting memory limit from cgroups: %v", err)
		}
		if memLimit < mem {
			mem = memLimit
		}
	}
	cmd.Args = append(cmd.Args, "--total-memory", strconv.FormatUint(mem, 10))

	if args.Attached {
		// Kill sandbox if parent process exits in attached mode.
		cmd.SysProcAttr.Pdeathsig = unix.SIGKILL
		// Tells boot that any process it creates must have pdeathsig set.
		cmd.Args = append(cmd.Args, "--attached")
	}

	if args.ExecFile != nil {
		donations.Donate("exec-fd", args.ExecFile)
	}

	nextFD = donations.Transfer(cmd, nextFD)

	_ = donation.DonateAndTransferCustomFiles(cmd, nextFD, args.PassFiles)

	// Add container ID as the last argument.
	cmd.Args = append(cmd.Args, s.ID)

	donation.LogDonations(cmd)
	log.Debugf("Starting sandbox: %s %v", cmd.Path, cmd.Args)
	log.Debugf("SysProcAttr: %+v", cmd.SysProcAttr)
	// Pass the current time so the sandbox process knows when it was spawned.
	cmd.Env = starttime.SpawnEnviron(cmd.Env)
	if err := specutils.StartInNS(cmd, nss); err != nil {
		err := fmt.Errorf("starting sandbox: %v", err)
		// If the sandbox failed to start, it may be because the binary
		// permissions were incorrect. Check the bits and return a more helpful
		// error message.
		//
		// NOTE: The error message is checked because error types are lost over
		// rpc calls.
		if strings.Contains(err.Error(), unix.EACCES.Error()) {
			if permsErr := checkBinaryPermissions(conf); permsErr != nil {
				return fmt.Errorf("%v: %v", err, permsErr)
			}
		}
		return err
	}
	s.OriginalOOMScoreAdj, err = specutils.GetOOMScoreAdj(cmd.Process.Pid)
	if err != nil {
		return err
	}
	if setUserMappings {
		if err := SetUserMappings(args.Spec, cmd.Process.Pid); err != nil {
			return err
		}
	}

	s.child = true
	s.Pid.Store(cmd.Process.Pid)
	if s.PidIdentity, err = specutils.ProcessIdentity(cmd.Process.Pid); err != nil {
		return err
	}
	log.Infof("Sandbox started, PID: %d", cmd.Process.Pid)

	if pinRing != nil {
		if err := spawnFDParking(pinRing, cmd.Process.Pid); err != nil {
			log.Warningf("Cannot spawn sidecar %q: %v. This slows down gVisor sandbox teardown.", gvisorbinaries.FDParking.Name, err)
		}
	}
	return nil
}

// donateProcUmounter donates the /proc unmounter sidecar binary to a sandbox
// process that sets up its own root, which spawns it unless it runs rootless.
func donateProcUmounter(conf *config.Config, donations *donation.Agency) error {
	if conf.Rootless {
		return nil
	}
	b := &gvisorbinaries.ProcUmounter
	p, err := b.Path()
	if err != nil {
		return fmt.Errorf("sidecar %q not usable: %w", b.Name, err)
	}
	f, err := os.OpenFile(p, unix.O_PATH, 0)
	if err != nil {
		return fmt.Errorf("cannot open %q: %w", p, err)
	}
	donations.DonateAndClose("proc-umounter-fd", f)
	return nil
}

// spawnFDParking starts the runsc-fd-parking sidecar, which holds
// `ring` until the sandbox process `pid` has exited and thus keeps the
// sandbox from being the last ref holder of the FDs pinned into the ring.
// See `//pkg/pinring` and `//runsc/fdparking`.
func spawnFDParking(ring *os.File, pid int) error {
	pidfd, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		return fmt.Errorf("pidfd_open(%d): %w", pid, err)
	}
	defer unix.Close(pidfd)
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		return fmt.Errorf("cannot open %s: %w", os.DevNull, err)
	}
	defer devNull.Close()
	parkingPid, err := gvisorbinaries.FDParking.ForkExec(gvisorbinaries.Options{
		// FDs 3 (sandbox pidfd) and 4 (pin ring) are what the
		// `//runsc/fdparking` binary expects.
		Files: []uintptr{devNull.Fd(), devNull.Fd(), devNull.Fd(), uintptr(pidfd), ring.Fd()},
		// The sidecar must outlive this process and not die with its session.
		SysProcAttr: &unix.SysProcAttr{Setsid: true},
	})
	if err != nil {
		return fmt.Errorf("cannot fork/exec: %w", err)
	}
	log.Infof("FD parking sidecar started, PID: %d", parkingPid)
	go func() {
		// Reap in case it does for some reason.
		for {
			if _, err := unix.Wait4(parkingPid, nil, 0, nil); err != unix.EINTR {
				return
			}
		}
	}()
	return nil
}

// createSaveFiles creates the files used by checkpoint to save the state. They are returned in
// the following order: sentry state, page metadata, page file. This is the same order expected by
// RPCs and argument passing to the sandbox.
func createSaveFiles(path string, direct bool, compression statefile.CompressionLevel) ([]*os.File, error) {
	var files []*os.File
	clean := cleanup.Make(func() {
		for _, f := range files {
			_ = f.Close()
			_ = os.Remove(f.Name())
		}
	})
	defer clean.Clean()

	stateFilePath := filepath.Join(path, checkpointfiles.StateFileName)
	f, err := os.OpenFile(stateFilePath, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0644)
	if err != nil {
		return nil, fmt.Errorf("creating checkpoint state file %q: %w", stateFilePath, err)
	}
	files = append(files, f)

	// When there is no compression, MemoryFile contents are page-aligned.
	// It is beneficial to store them separately so certain optimizations can be
	// applied during restore. See Restore().
	if compression == statefile.CompressionLevelNone {
		pagesMetadataFilePath := filepath.Join(path, checkpointfiles.PagesMetadataFileName)
		f, err = os.OpenFile(pagesMetadataFilePath, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0644)
		if err != nil {
			return nil, fmt.Errorf("creating checkpoint pages metadata file %q: %w", pagesMetadataFilePath, err)
		}
		files = append(files, f)

		pagesFilePath := filepath.Join(path, checkpointfiles.PagesFileName)
		pagesWriteFlags := os.O_CREATE | os.O_EXCL | os.O_RDWR
		if direct {
			// The writes will be page-aligned, so it can be opened with O_DIRECT.
			pagesWriteFlags |= syscall.O_DIRECT
		}
		f, err := os.OpenFile(pagesFilePath, pagesWriteFlags, 0644)
		if err != nil {
			return nil, fmt.Errorf("creating checkpoint pages file %q: %w", pagesFilePath, err)
		}
		files = append(files, f)
	}

	clean.Release()
	return files, nil
}

// createFSCheckpointLocalFiles creates the files of the filesystem checkpoint
// with the given prefix in imagePath.
func createFSCheckpointLocalFiles(imagePath, prefix string, direct bool) ([]*os.File, error) {
	if strings.Contains(prefix, "/") {
		subDir := filepath.Dir(filepath.Join(imagePath, checkpointfiles.PrefixFileName(prefix, checkpointfiles.FSCheckpointManifestFileName)))
		if err := os.MkdirAll(subDir, 0755); err != nil {
			return nil, fmt.Errorf("creating fs checkpoint directory %q: %w", subDir, err)
		}
	}
	dirFD, err := unix.Open(imagePath, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("opening fs checkpoint directory %q failed: %w", imagePath, err)
	}
	defer unix.Close(dirFD)
	return openFSCheckpointLocalFiles(dirFD, imagePath, prefix, unix.O_CREAT|unix.O_EXCL|unix.O_RDWR, direct)
}

// openFSCheckpointLocalFiles opens the files of the filesystem checkpoint
// with the given prefix in imagePath, relative to dirFD, an FD for imagePath.
func openFSCheckpointLocalFiles(dirFD int, imagePath, prefix string, openFlags int, direct bool) ([]*os.File, error) {
	var files [4]*os.File
	closeCleanup := cleanup.Make(func() {
		for _, f := range files {
			if f != nil {
				_ = f.Close()
				if openFlags&unix.O_CREAT != 0 {
					_ = os.Remove(f.Name())
				}
			}
		}
	})
	defer closeCleanup.Clean()

	// Use of O_DIRECT ~requires that I/O operations are aligned to some
	// unspecified [1] granularity that (as of this writing) is, in practice,
	// less than or equal to page size. Performance and memory pressure
	// benefits of O_DIRECT where applicable (avoiding page cache when it won't
	// be used) scale linearly with file size.
	// - Manifest file: no alignment guarantee, very small size
	// - Multi-tar files: no alignment guarantee, relatively small size
	// - Pages metadata file: no alignment guarantee, relatively small size
	// - Pages file: page-aligned I/O, relatively large size
	// Thus currently O_DIRECT is only used for pages file.
	//
	// [1] Obtainable via STATX_DIOALIGN / STATX_DIO_READ_ALIGN; "support
	// varies by filesystem". But we don't need to deal with this unless we
	// start having checkpoints stored on devices where the direct I/O size is
	// greater than page size.
	//
	// TODO: NOLINT - If multi-tar or pages metadata files get big, we can
	// guarantee alignment and enable async I/O by using
	// stateio.BufReader/Writer(stateio.FDReader/Writer) rather than
	// bufio.Reader/Writer(os.File) in the sentry.
	maybeODirect := 0
	if direct {
		maybeODirect = unix.O_DIRECT
	}

	manifestFileName := checkpointfiles.PrefixFileName(prefix, checkpointfiles.FSCheckpointManifestFileName)
	manifestFile, err := openCheckpointFile(dirFD, imagePath, manifestFileName, openFlags)
	if err != nil {
		return nil, fmt.Errorf("opening manifest file %q: %w", filepath.Join(imagePath, manifestFileName), err)
	}
	files[0] = manifestFile

	multiTarFileName := checkpointfiles.PrefixFileName(prefix, checkpointfiles.FSCheckpointMultiTarFileName)
	multiTarFile, err := openCheckpointFile(dirFD, imagePath, multiTarFileName, openFlags)
	if err != nil {
		return nil, fmt.Errorf("opening multi-tar file %q: %w", filepath.Join(imagePath, multiTarFileName), err)
	}
	files[1] = multiTarFile

	pagesMetadataFileName := checkpointfiles.PrefixFileName(prefix, checkpointfiles.PagesMetadataFileName)
	pagesMetadataFile, err := openCheckpointFile(dirFD, imagePath, pagesMetadataFileName, openFlags)
	if err != nil {
		return nil, fmt.Errorf("opening pages metadata file %q: %w", filepath.Join(imagePath, pagesMetadataFileName), err)
	}
	files[2] = pagesMetadataFile

	pagesFileName := checkpointfiles.PrefixFileName(prefix, checkpointfiles.PagesFileName)
	pagesFile, err := openCheckpointFile(dirFD, imagePath, pagesFileName, openFlags|maybeODirect)
	if err != nil {
		return nil, fmt.Errorf("opening pages file %q: %w", filepath.Join(imagePath, pagesFileName), err)
	}
	files[3] = pagesFile

	closeCleanup.Release()
	return files[:], nil
}

// maybeStartCheckpointGoferAndGetSocket checks if use of a checkpoint gofer is
// specified by configuration files in imagePath. If so, it starts the
// checkpoint gofer and returns an os.File representing a socket connected to
// it. Otherwise, it returns nil.
func (s *Sandbox) maybeStartCheckpointGoferAndGetSocket(conf *config.Config, cg cgroup.Cgroup, imagePath string, extraFlags ...string) (*os.File, error) {
	gcsOptsPath := path.Join(imagePath, checkpointGCSOptsFileName)
	gcsOptsFile, err := os.Open(gcsOptsPath)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			log.Warningf("Failed to open %q: %v", gcsOptsPath, err)
		}
		return nil, nil
	}
	defer gcsOptsFile.Close()
	// Pass /dev/null as the checkpoint gofer's standard streams to avoid
	// polluting this process' logs. The checkpoint gofer accepts flags like
	// -debug-log, which are plumbed through to it by passing conf.ToFlags()
	// below.
	devNullFile, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("failed to open %s: %w", os.DevNull, err)
	}
	defer devNullFile.Close()
	socketFDs, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("failed to create checkpoint gofer socketpair: %w", err)
	}
	defer unix.Close(socketFDs[1])
	clientSockFile := os.NewFile(uintptr(socketFDs[0]), "checkpointgofer-socket")
	err = cgroup.RunInCgroup(cg, func(cloneIntoCgroupFD *os.File) error {
		argv := append([]string{"runsc-checkpointgofer"}, conf.ToFlags()...)
		extraFiles := []uintptr{devNullFile.Fd(), devNullFile.Fd(), devNullFile.Fd(), uintptr(socketFDs[1]), gcsOptsFile.Fd()}

		// Add log file FDs.
		if conf.LogFilename != "" {
			f, err := log.OpenFile(conf.LogFilename, os.O_WRONLY|os.O_CREATE|os.O_APPEND, &log.DefaultFileOpts{})
			if err != nil {
				return fmt.Errorf("opening log file: %w", err)
			}
			defer f.Close()
			extraFiles = append(extraFiles, f.Fd())
			argv = append(argv, fmt.Sprintf("--log-fd=%d", len(extraFiles)-1))
		}
		lfOpts := &specutils.LogFileOpts{
			SandboxID: s.ID,
			CID:       s.ID,
			Command:   "checkpointgofer",
			Timestamp: s.StartTime,
		}
		if conf.DebugLog != "" && specutils.IsDebugCommand(conf, "checkpointgofer") {
			f, err := specutils.OpenDebugLogFile(conf.DebugLog, lfOpts)
			if err != nil {
				return fmt.Errorf("opening debug log file: %w", err)
			}
			defer f.Close()
			extraFiles = append(extraFiles, f.Fd())
			argv = append(argv, fmt.Sprintf("--debug-log-fd=%d", len(extraFiles)-1))
		}

		argv = append(argv, "checkpointgofer", "-sock-fd=3", "-gcs-opts-fd=4")
		argv = append(argv, extraFlags...)
		// Don't forward GOMAXPROCS defaults that apply to this process (in
		// particular, containerd-shim-runsc-v1 passes GOMAXPROCS=2 in
		// v1.service.newCommand()).
		env := slices.DeleteFunc(os.Environ(), func(env string) bool { return strings.HasPrefix(env, "GOMAXPROCS=") })
		sysProcAttr := &unix.SysProcAttr{
			// Detach from this session, otherwise the subprocess will get
			// SIGHUP and SIGCONT when re-parented.
			Setsid: true,
		}
		if cloneIntoCgroupFD != nil {
			sysProcAttr.UseCgroupFD = true
			sysProcAttr.CgroupFD = int(cloneIntoCgroupFD.Fd())
		}
		_, err := gvisorbinaries.CheckpointGofer.ForkExec(gvisorbinaries.Options{
			Argv:        argv,
			Envv:        env,
			Files:       extraFiles,
			SysProcAttr: sysProcAttr,
		})
		return err
	})
	if err != nil {
		clientSockFile.Close()
		return nil, fmt.Errorf("failed to start checkpoint gofer: %w", err)
	}
	return clientSockFile, nil
}

// ConfigureCmdForRootless configures cmd to donate a socket FD that can be
// used to synchronize userns configuration.
func ConfigureCmdForRootless(cmd *exec.Cmd, donations *donation.Agency) (*os.File, error) {
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fds[1]), "userns sync other FD")
	donations.DonateAndClose("sync-userns-fd", f)
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &unix.SysProcAttr{}
	}
	cmd.SysProcAttr.AmbientCaps = []uintptr{
		// Same as `cap` in cmd/gofer.go.
		unix.CAP_CHOWN,
		unix.CAP_DAC_OVERRIDE,
		unix.CAP_DAC_READ_SEARCH,
		unix.CAP_FOWNER,
		unix.CAP_FSETID,
		unix.CAP_SYS_CHROOT,
		// Needed for setuid(2)/setgid(2).
		unix.CAP_SETUID,
		unix.CAP_SETGID,
		// Needed for chroot.
		unix.CAP_SYS_ADMIN,
		// Needed to be able to clear bounding set (PR_CAPBSET_DROP).
		unix.CAP_SETPCAP,
	}
	return os.NewFile(uintptr(fds[0]), "userns sync FD"), nil
}

// selfFDDir is the directory that lists the FDs of this process.
const selfFDDir = "/proc/self/fd"

// shortSocketPath returns a path to connect to the socket at path, and a
// function that releases it. UDS connect fails when the len(socket path) >=
// UNIX_PATH_MAX. For such a path, it opens the socket using open(2) and uses
// /proc to refer to the open FD.
func shortSocketPath(path string) (string, func(), error) {
	if len(path) < linux.UnixPathMax {
		return path, func() {}, nil
	}
	sockFD, err := unix.Open(path, unix.O_PATH, 0)
	if err != nil {
		return "", nil, fmt.Errorf("failed to open socket at %q", path)
	}
	return filepath.Join("/proc/self/fd", fmt.Sprintf("%d", sockFD)), func() { unix.Close(sockFD) }, nil
}
