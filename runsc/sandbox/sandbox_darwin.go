// Copyright 2026 The gVisor Authors.
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

//go:build darwin

package sandbox

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
	"gvisor.dev/gvisor/pkg/coverage"
	"gvisor.dev/gvisor/pkg/log"
	"gvisor.dev/gvisor/pkg/sentry/platform/platformdesc"
	"gvisor.dev/gvisor/pkg/state/statefile"
	"gvisor.dev/gvisor/runsc/cgroup"
	"gvisor.dev/gvisor/runsc/config"
	"gvisor.dev/gvisor/runsc/donation"
	"gvisor.dev/gvisor/runsc/profile"
	"gvisor.dev/gvisor/runsc/specutils"
	"gvisor.dev/gvisor/runsc/starttime"
)

// errCheckpointNotSupported is returned by checkpoint and restore on darwin.
var errCheckpointNotSupported = errors.New("checkpoint and restore are not supported on darwin")

// selfFDDir is the directory that lists the FDs of this process.
const selfFDDir = "/dev/fd"

// darwinUnixPathMax is the size of sun_path on darwin.
const darwinUnixPathMax = 104

// shortSocketPath returns path and a function that does nothing. darwin
// has no O_PATH and no /proc to refer to an open socket by a shorter path.
func shortSocketPath(path string) (string, func(), error) {
	if len(path) >= darwinUnixPathMax {
		return "", nil, fmt.Errorf("control socket path %q is too long for darwin (%d bytes or more); use a shorter --root", path, darwinUnixPathMax)
	}
	return path, func() {}, nil
}

func openRestoreDir(dir string) (int, error) {
	return -1, errCheckpointNotSupported
}

func openCheckpointFile(dirFD int, dir, name string, flags int) (*os.File, error) {
	return nil, errCheckpointNotSupported
}

func openPagesFile(dirFD int, imagePath string, directIO DirectIO) (*os.File, error) {
	return nil, errCheckpointNotSupported
}

func createSaveFiles(path string, direct bool, compression statefile.CompressionLevel) ([]*os.File, error) {
	return nil, errCheckpointNotSupported
}

func createFSCheckpointLocalFiles(imagePath, prefix string, direct bool) ([]*os.File, error) {
	return nil, errCheckpointNotSupported
}

func openFSCheckpointLocalFiles(dirFD int, imagePath, prefix string, openFlags int, direct bool) ([]*os.File, error) {
	return nil, errCheckpointNotSupported
}

func (s *Sandbox) maybeStartCheckpointGoferAndGetSocket(conf *config.Config, cg cgroup.Cgroup, imagePath string, extraFlags ...string) (*os.File, error) {
	return nil, errCheckpointNotSupported
}

// createSandboxProcess starts the sandbox as a subprocess by running the "boot"
// command of the Sentry binary, passing in the bundle dir.
//
// darwin has no namespaces, user namespaces, capabilities or pivot_root(2)
// without root. So the sandbox process runs as the calling user, in the
// namespaces of the host, and confines itself with a Seatbelt profile (see
// runsc/boot/filter). The Sentry binary needs the hypervisor entitlement.
func (s *Sandbox) createSandboxProcess(conf *config.Config, args *Args) error {
	if args.Spec.Process != nil && args.Spec.Process.Terminal {
		return fmt.Errorf("a terminal (process.terminal) is not supported on darwin")
	}
	if args.DevIOFile != nil || len(args.FSRestoreImagePaths) > 0 || len(conf.TestOnlyAutosaveImagePath) != 0 {
		return fmt.Errorf("the dev gofer, filesystem restore and autosave are not supported on darwin")
	}
	if specutils.NVProxyEnabled(args.Spec, conf) || specutils.TPUProxyEnabled(args.Spec, conf) {
		return fmt.Errorf("device proxies are not supported on darwin")
	}

	// Ensure we don't leak FDs to the sandbox process.
	if err := SetCloExeOnAllFDs(); err != nil {
		return fmt.Errorf("setting CLOEXEC on all FDs: %w", err)
	}

	donations := donation.Agency{}
	defer donations.Close()

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

	// darwin has no prewarmer: runsc executes the Sentry binary directly.
	sentryBin := sentryBinary(conf)
	bootBinPath, err := sentryBin.Path()
	if err != nil {
		return fmt.Errorf("sidecar %q not usable: %w", sentryBin.Name, err)
	}
	log.Infof("Sidecar %q found: booting sandbox with %s", sentryBin.Name, bootBinPath)

	// Relay all the config flags to the sandbox process.
	cmd := exec.Command(bootBinPath, conf.ToFlags()...)
	cmd.SysProcAttr = &unix.SysProcAttr{
		// Detach from this session, otherwise cmd will get SIGHUP and SIGCONT
		// when re-parented.
		Setsid: true,
	}

	// Set Args[0] to make easier to spot the sandbox process.
	cmd.Args[0] = "runsc-sandbox"

	// Transfer FDs that need to be present before the "boot" command.
	nextFD := donations.Transfer(cmd, 3)

	// Add the "boot" command to the args.
	//
	// All flags after this must be for the boot command
	cmd.Args = append(cmd.Args, "boot", "--bundle="+args.BundleDir)
	if args.NoRootContainer {
		cmd.Args = append(cmd.Args, "--no-root-container")
	}

	cmd.Env = sandboxProcessEnv(conf, sandboxProcessEnvOptions{
		sentryUsesCgo: config.CgoEnabled,
	})

	// If there is a gofer, sends all socket ends to the sandbox.
	donations.DonateAndClose("io-fds", args.IOFiles...)
	donations.DonateAndClose("gofer-filestore-fds", args.GoferFilestoreFiles...)
	donations.DonateAndClose("mounts-fd", args.MountsFile)
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

	// The sandbox process runs as the calling user.
	s.UID = os.Getuid()
	s.GID = os.Getgid()

	// The current process' stdio must be passed to the application via the
	// --stdio-fds flag. The stdio of the sandbox process itself must not
	// be connected to the same FDs, otherwise we risk leaking sandbox
	// errors to the application, so we set the sandbox stdio to nil,
	// causing them to read/write from the null device.
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	stdios := []*os.File{os.Stdin, os.Stdout, os.Stderr}
	if conf.Debug {
		// If debugging, send the boot process stdio to the
		// this process' stdio, so that is is easier to find.
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
	}
	if err := s.configureStdios(conf, stdios); err != nil {
		return fmt.Errorf("configuring stdios: %w", err)
	}
	donations.Donate("stdio-fds", stdios...)
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

	totalSysMem, err := unix.SysctlUint64("hw.memsize")
	if err != nil {
		return fmt.Errorf("reading hw.memsize: %w", err)
	}
	cmd.Args = append(cmd.Args, "--total-host-memory", strconv.FormatUint(totalSysMem, 10))
	cmd.Args = append(cmd.Args, "--total-memory", strconv.FormatUint(totalSysMem, 10))

	if args.Attached {
		// Kill sandbox if parent process exits in attached mode. darwin has no
		// parent death signal; the sandbox watches its parent instead.
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
	// Pass the current time so the sandbox process knows when it was spawned.
	cmd.Env = starttime.SpawnEnviron(cmd.Env)
	if err := cmd.Start(); err != nil {
		err := fmt.Errorf("starting sandbox: %v", err)
		if strings.Contains(err.Error(), unix.EACCES.Error()) {
			if permsErr := checkBinaryPermissions(conf); permsErr != nil {
				return fmt.Errorf("%v: %v", err, permsErr)
			}
		}
		return err
	}

	s.child = true
	s.Pid.Store(cmd.Process.Pid)
	if s.PidIdentity, err = specutils.ProcessIdentity(cmd.Process.Pid); err != nil {
		return err
	}
	log.Infof("Sandbox started, PID: %d", cmd.Process.Pid)
	return nil
}
