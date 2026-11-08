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

// Package sentrycmd holds the subcommands run by the sentry process.
package sentrycmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/google/subcommands"
	"github.com/moby/sys/capability"
	specs "github.com/opencontainers/runtime-spec/specs-go"
	"golang.org/x/sys/unix"
	"gvisor.dev/gvisor/pkg/fd"
	"gvisor.dev/gvisor/pkg/log"
	"gvisor.dev/gvisor/pkg/metric"
	"gvisor.dev/gvisor/pkg/prometheus"
	"gvisor.dev/gvisor/pkg/rdma"
	"gvisor.dev/gvisor/pkg/sentry/devices/nvproxy"
	"gvisor.dev/gvisor/pkg/sentry/devices/nvproxy/nvconf"
	"gvisor.dev/gvisor/pkg/sentry/syscalls/linux"
	"gvisor.dev/gvisor/pkg/timing"
	"gvisor.dev/gvisor/runsc/boot"
	"gvisor.dev/gvisor/runsc/boot/sentryapi"
	"gvisor.dev/gvisor/runsc/cmd/sandboxsetup"
	"gvisor.dev/gvisor/runsc/cmd/util"
	"gvisor.dev/gvisor/runsc/config"
	"gvisor.dev/gvisor/runsc/flag"
	"gvisor.dev/gvisor/runsc/profile"
	"gvisor.dev/gvisor/runsc/specutils"
)

// Note that directfsSandboxCaps is the same as caps defined in gofer.go
// except CAP_SYS_CHROOT because we don't need to chroot in directfs mode.
var (
	directfsSandboxCaps = []string{
		"CAP_CHOWN",
		"CAP_DAC_OVERRIDE",
		"CAP_DAC_READ_SEARCH",
		"CAP_FOWNER",
		"CAP_FSETID",
	}

	// DirectfsSandboxLinuxCaps is the minimal set of capabilities needed by the
	// sandbox to operate on files in directfs mode.
	DirectfsSandboxLinuxCaps = &specs.LinuxCapabilities{
		Bounding:  directfsSandboxCaps,
		Effective: directfsSandboxCaps,
		Permitted: directfsSandboxCaps,
	}

	hostnetSandboxLinuxCaps = map[capability.Cap]string{
		capability.CAP_NET_ADMIN:        "CAP_NET_ADMIN",
		capability.CAP_NET_BIND_SERVICE: "CAP_NET_BIND_SERVICE",
		capability.CAP_NET_RAW:          "CAP_NET_RAW",
	}
)

// Boot implements subcommands.Command for the "boot" command which starts a
// new sandbox. It should not be called directly.
type Boot struct {
	util.InternalSubCommand

	// bundleDir is the directory containing the OCI spec.
	bundleDir string

	// specFD is the file descriptor that the spec will be read from.
	specFD int

	// controllerFD is the file descriptor of a stream socket for the
	// control server that is donated to this process.
	controllerFD int

	// deviceFD is the file descriptor for the platform device file.
	deviceFD int

	// pinRingFD is the file descriptor of the donated pin ring.
	// See `//pkg/pinring`.
	pinRingFD int

	// ioFDs is the list of FDs used to connect to FS gofers.
	ioFDs sandboxsetup.IntFlags

	// devIoFD is the FD to connect to dev gofer.
	devIoFD int

	// goferFilestoreFDs are FDs to the regular files that will back the tmpfs or
	// overlayfs mount for certain gofer mounts.
	goferFilestoreFDs sandboxsetup.IntFlags

	// goferMountConfs contains information about how the gofer mounts have been
	// configured. The first entry is for rootfs and the following entries are
	// for bind mounts in Spec.Mounts (in the same order).
	goferMountConfs specutils.GoferMountConfFlags

	// stdioFDs are the fds for stdin, stdout, and stderr. They must be
	// provided in that order.
	stdioFDs sandboxsetup.IntFlags

	// passFDs are mappings of user-supplied host to guest file descriptors.
	passFDs sandboxsetup.FDMappings

	// execFD is the host file descriptor used for program execution.
	execFD int

	// applyCaps determines if capabilities defined in the spec should be applied
	// to the process.
	applyCaps bool

	// setUpChroot is set to true if the sandbox is started in an empty root.
	setUpRoot bool

	// cpuNum number of CPUs to create inside the sandbox.
	cpuNum int

	// lifecycleCPUNum is the number of CPUs that the sandbox can use during
	// checkpoint and restore.
	lifecycleCPUNum int

	// cpuQuota and cpuPeriod carry the raw host CFS CPU settings that should be
	// exposed through sandbox cgroupfs.
	cpuQuota  int64
	cpuPeriod int64

	// totalMem sets the initial amount of total memory to report back to the
	// container.
	totalMem uint64

	// totalHostMem is the total memory reported by host /proc/meminfo.
	totalHostMem uint64

	// userLogFD is the file descriptor to write user logs to.
	userLogFD int

	// mountsFD is the file descriptor to read list of mounts after they have
	// been resolved (direct paths, no symlinks). They are resolved outside the
	// sandbox (e.g. gofer) and sent through this FD. When mountsFD is not
	// provided, there is no cleaning required for mounts and the mounts in
	// the spec can be used as is.
	mountsFD int

	podInitConfigFD int

	sinkFDs sandboxsetup.IntFlags

	saveFDs             sandboxsetup.IntFlags
	saveCheckpointGofer bool

	fsSaveFDs             sandboxsetup.IntFlags
	fsSaveCheckpointGofer bool

	fsRestoreFDs             sandboxsetup.IntFlags
	fsRestoreCheckpointGofer bool

	// attached is set to true to kill the sandbox process when the parent process
	// terminates. This flag is set when the command execve's itself because
	// parent death signal doesn't propagate through execve when uid/gid changes.
	attached bool

	// productName is the value to show in
	// /sys/devices/virtual/dmi/id/product_name.
	productName string

	hostTHP boot.HostTHP

	// FDs for profile data.
	profileFDs profile.FDArgs

	// finalMetricsFD is a file descriptor to write metric data to upon sandbox
	// termination.
	finalMetricsFD int

	// bootTimesFD is a file descriptor to write the startup timeline of the
	// sandbox to, as JSON, when the sandbox starts (see timing.Timer.Export).
	bootTimesFD int

	// profilingMetricsFD is a file descriptor to write Sentry metrics data to.
	profilingMetricsFD int

	// profilingMetricsLossy sets whether profilingMetricsFD is a lossy channel.
	// If so, the format used to write to it will contain a checksum.
	profilingMetricsLossy bool

	// cpuDMALatencyFD is a file descriptor holding /dev/cpu_dma_latency
	// open to prevent host CPUs from entering deep idle states for the
	// lifetime of the sandbox. See MaybeCapCPUIdleStates.
	cpuDMALatencyFD int

	// procMountSyncFD is a file descriptor that has to be closed when the
	// procfs mount isn't needed anymore.
	procMountSyncFD int

	// procUmounterFD is the /proc unmounter sidecar binary, which is spawned
	// to unmount /proc on behalf of this process. Only set with setUpRoot
	// and without rootless.
	procUmounterFD int

	// procMountSyncFile holds the *os.File for procMountSyncFD when this
	// process continues without re-exec'ing itself (see
	// `applyCapsAllThreads`). It keeps the FD alive.
	procMountSyncFile *os.File

	// syncUsernsFD is the file descriptor that has to be closed when the
	// boot process should invoke setuid/setgid for root user. This is mainly
	// used to synchronize rootless user namespace initialization.
	syncUsernsFD int

	// nvidiaDriverVersion is the Nvidia driver ABI version to use, or "latest"
	// for the newest supported one.
	nvidiaDriverVersion string

	// These correspond to fields in nvconf.HostSettings, which is not used
	// here because nvconf.HostSettings.FabricIMEXManagementDevMinor in
	// particular is uint32 and there is no flag.Uint32Var().
	procDriverNvidiaParams             string
	nvidiaFabricIMEXManagementDevMinor int64
	nvidiaMNCCManagementDevMinor       int64

	// uid and gid are the user and group IDs to switch to after setting up the
	// user namespace.
	uid int
	gid int

	// rootfsUpperTarFD is the file descriptor to a tar file that has rootfs change at startup.
	rootfsUpperTarFD int

	// noRootContainer boots without a root container. See Create.noRootContainer.
	noRootContainer bool
}

// Name implements subcommands.Command.Name.
func (*Boot) Name() string {
	return "boot"
}

// Synopsis implements subcommands.Command.Synopsis.
func (*Boot) Synopsis() string {
	return "launch a sandbox process"
}

// Usage implements subcommands.Command.Usage.
func (*Boot) Usage() string {
	return "boot [flags] <container id>\n"
}

// SetFlags implements subcommands.Command.SetFlags.
func (b *Boot) SetFlags(f *flag.FlagSet) {
	f.StringVar(&b.bundleDir, "bundle", "", "required path to the root of the bundle directory")
	f.BoolVar(&b.applyCaps, "apply-caps", false, "if true, apply capabilities defined in the spec to the process")
	f.BoolVar(&b.setUpRoot, "setup-root", false, "if true, set up an empty root for the process")
	f.IntVar(&b.cpuNum, "cpu-num", 0, "number of CPUs to create inside the sandbox")
	f.IntVar(&b.lifecycleCPUNum, "lifecycle-cpu-num", 0, "number of CPUs that the sandbox can use during checkpoint and restore (0 means the same as --cpu-num)")
	// cpu-quota defaults to -1 (unlimited), matching the Linux CFS default
	// for cpu.cfs_quota_us. A positive value is passed through as-is to sandbox
	// cgroupfs.
	f.Int64Var(&b.cpuQuota, "cpu-quota", -1, "raw CFS cpu quota in usecs to expose via sandbox cgroupfs (-1 means unlimited)")
	// cpu-period defaults to 0 (unset). When unset, mountCgroupMounts
	// uses the Linux default of 100000us (100ms). A positive value is
	// passed through as-is.
	f.Int64Var(&b.cpuPeriod, "cpu-period", 0, "raw CFS cpu period in usecs to expose via sandbox cgroupfs (0 means use default 100ms)")
	f.IntVar(&b.cpuDMALatencyFD, "cpu-dma-latency-fd", -1, "file descriptor holding /dev/cpu_dma_latency open")
	f.IntVar(&b.procMountSyncFD, "proc-mount-sync-fd", -1, "file descriptor that has to be written to when /proc isn't needed anymore and can be unmounted")
	f.IntVar(&b.procUmounterFD, "proc-umounter-fd", -1, "file descriptor of the /proc unmounter binary")
	f.IntVar(&b.syncUsernsFD, "sync-userns-fd", -1, "file descriptor used to synchronize rootless user namespace initialization.")
	f.Uint64Var(&b.totalMem, "total-memory", 0, "sets the initial amount of total memory to report back to the container")
	f.Uint64Var(&b.totalHostMem, "total-host-memory", 0, "total memory reported by host /proc/meminfo")
	f.BoolVar(&b.attached, "attached", false, "if attached is true, kills the sandbox process when the parent process terminates")
	f.StringVar(&b.productName, "product-name", "", "value to show in /sys/devices/virtual/dmi/id/product_name")
	f.StringVar(&b.hostTHP.ShmemEnabled, "host-thp-shmem-enabled", "", "value of /sys/kernel/mm/transparent_hugepage/shmem_enabled on the host")
	f.StringVar(&b.hostTHP.Defrag, "host-thp-defrag", "", "value of /sys/kernel/mm/transparent_hugepage/defrag on the host")
	f.IntVar(&b.uid, "uid", 0, "user ID")
	f.IntVar(&b.gid, "gid", 0, "user ID")
	f.BoolVar(&b.noRootContainer, "no-root-container", false, "if true, boot the sandbox without a root container; containers are added later as subcontainers")

	// Open FDs that are donated to the sandbox.
	f.IntVar(&b.specFD, "spec-fd", -1, "required fd with the container spec")
	f.IntVar(&b.controllerFD, "controller-fd", -1, "required FD of a stream socket for the control server that must be donated to this process")
	f.IntVar(&b.deviceFD, "device-fd", -1, "FD for the platform device file")
	f.IntVar(&b.pinRingFD, "pin-ring-fd", -1, "FD of a disabled io_uring in which the sentry pins its expensive-to-release files")
	f.Var(&b.ioFDs, "io-fds", "list of image FDs and/or socket FDs to connect gofer clients. They must follow this order: root first, then mounts as defined in the spec")
	f.IntVar(&b.devIoFD, "dev-io-fd", -1, "FD to connect dev gofer client")
	f.Var(&b.stdioFDs, "stdio-fds", "list of FDs containing sandbox stdin, stdout, and stderr in that order")
	f.Var(&b.passFDs, "pass-fd", "mapping of host to guest FDs. They must be in M:N format. M is the host and N the guest descriptor.")
	f.IntVar(&b.execFD, "exec-fd", -1, "host file descriptor used for program execution.")
	f.Var(&b.goferFilestoreFDs, "gofer-filestore-fds", "FDs to the regular files that will back the overlayfs or tmpfs mount if a gofer mount is to be overlaid.")
	f.Var(&b.goferMountConfs, "gofer-mount-confs", "information about how the gofer mounts have been configured.")
	f.IntVar(&b.userLogFD, "user-log-fd", 0, "file descriptor to write user logs to. 0 means no logging.")
	f.IntVar(&b.mountsFD, "mounts-fd", -1, "mountsFD is an optional file descriptor to read list of mounts after they have been resolved (direct paths, no symlinks).")
	f.IntVar(&b.podInitConfigFD, "pod-init-config-fd", -1, "file descriptor to the pod init configuration file.")
	f.Var(&b.sinkFDs, "sink-fds", "ordered list of file descriptors to be used by the sinks defined in --pod-init-config.")
	f.Var(&b.saveFDs, "save-fds", "ordered list of file descriptors to be used save checkpoints. Order: kernel state, page metadata, page file")
	f.BoolVar(&b.saveCheckpointGofer, "save-checkpoint-gofer", false, "if true, -save-fds is a socket connected to checkpoint gofer")
	f.Var(&b.fsSaveFDs, "fs-save-fds", "ordered list of file descriptors for filesystem checkpoint save")
	f.BoolVar(&b.fsSaveCheckpointGofer, "fs-save-checkpoint-gofer", false, "if true, -fs-save-fds is a socket connected to checkpoint gofer")
	f.Var(&b.fsRestoreFDs, "fs-restore-fds", "ordered list of file descriptors for filesystem checkpoint restore")
	f.BoolVar(&b.fsRestoreCheckpointGofer, "fs-restore-checkpoint-gofer", false, "if true, -fs-restore-fds is a socket connected to checkpoint gofer")
	f.IntVar(&b.rootfsUpperTarFD, "rootfs-upper-tar-fd", -1, "file descriptor to the tar file containing the rootfs upper layer changes.")

	// Profiling flags.
	b.profileFDs.SetFromFlags(f)
	f.IntVar(&b.finalMetricsFD, "final-metrics-log-fd", -1, "file descriptor to write metrics to upon sandbox termination.")
	f.IntVar(&b.bootTimesFD, "boot-times-log-fd", -1, "file descriptor to write the startup timeline of the sandbox to, as JSON, when the sandbox starts.")
	f.IntVar(&b.profilingMetricsFD, "profiling-metrics-fd", -1, "file descriptor to write sentry profiling metrics.")
	f.BoolVar(&b.profilingMetricsLossy, "profiling-metrics-fd-lossy", false, "if true, treat the sentry profiling metrics FD as lossy and write a checksum to it.")

	// Nvidia driver properties.
	f.StringVar(&b.nvidiaDriverVersion, "nvidia-driver-version", "", "Nvidia driver ABI version to use, or 'latest' for the newest supported one")
	f.StringVar(&b.procDriverNvidiaParams, "nvidia-host-params", "", "value of /proc/driver/nvidia/params on the host")
	f.Int64Var(&b.nvidiaFabricIMEXManagementDevMinor, "nvidia-fabric-imex-mgmt-minor", -1, "DeviceFileMinor in /proc/driver/nvidia/capabilities/fabric-imex-mgmt on the host")
	f.Int64Var(&b.nvidiaMNCCManagementDevMinor, "nvidia-mncc-mgmt-minor", -1, "DeviceFileMinor in /proc/driver/nvidia/capabilities/mncc-mgmt on the host")
}

// runLoader creates the Loader for spec, runs the sandbox until the root
// container exits, and stores its exit status in waitStatus. preSeccomp, if
// not nil, runs right before the Loader installs the system call filter.
func (b *Boot) runLoader(f *flag.FlagSet, conf *config.Config, spec *specs.Spec, timer *timing.Timer, waitStatus *unix.WaitStatus, preSeccomp func()) subcommands.ExitStatus {
	var nvidiaDriverVersion nvconf.DriverVersion
	if b.nvidiaDriverVersion == "latest" {
		nvproxy.Init()
		nvidiaDriverVersion = nvproxy.LatestDriver()
	} else if b.nvidiaDriverVersion != "" {
		var err error
		nvidiaDriverVersion, err = nvconf.DriverVersionFrom(b.nvidiaDriverVersion)
		if err != nil {
			util.Fatalf("Failed to parse nvidia driver version: %v", err)
		}
	}

	linux.SetAFSSyscallPanic(conf.TestOnlyAFSSyscallPanic)

	var rdmaSnap *rdma.Snapshot
	if specutils.RDMAEnabled(spec, conf) {
		// Load the RDMA sysfs snapshot serialized by the chroot stage.
		var err error
		if rdmaSnap, err = rdma.Load(rdma.Path); err != nil {
			util.Fatalf("loading RDMA sysfs snapshot: %v", err)
		}
		timer.Reached("RDMA snapshot loaded")
	}

	// Create the loader.
	bootArgs := boot.Args{
		ID:                  f.Arg(0),
		Spec:                spec,
		Conf:                conf,
		ControllerFD:        b.controllerFD,
		Device:              fd.New(b.deviceFD),
		PinRingFD:           b.pinRingFD,
		CPUDMALatencyFD:     b.cpuDMALatencyFD,
		GoferFDs:            b.ioFDs.GetArray(),
		DevGoferFD:          b.devIoFD,
		StdioFDs:            b.stdioFDs.GetArray(),
		PassFDs:             b.passFDs.GetArray(),
		ExecFD:              b.execFD,
		GoferFilestoreFDs:   b.goferFilestoreFDs.GetArray(),
		GoferMountConfs:     b.goferMountConfs.GetArray(),
		NumCPU:              b.cpuNum,
		LifecycleNumCPU:     b.lifecycleCPUNum,
		CPUQuota:            b.cpuQuota,
		CPUPeriod:           b.cpuPeriod,
		TotalMem:            b.totalMem,
		TotalHostMem:        b.totalHostMem,
		UserLogFD:           b.userLogFD,
		RDMASysfs:           rdmaSnap,
		ProductName:         b.productName,
		PodInitConfigFD:     b.podInitConfigFD,
		SinkFDs:             b.sinkFDs.GetArray(),
		ProfileOpts:         profile.MakeOpts(&b.profileFDs, conf.ProfileGCInterval),
		NvidiaDriverVersion: nvidiaDriverVersion,
		NvidiaHostSettings: &nvconf.HostSettings{
			ProcDriverNvidiaParams:       b.procDriverNvidiaParams,
			HaveFabricIMEXManagement:     b.nvidiaFabricIMEXManagementDevMinor >= 0,
			FabricIMEXManagementDevMinor: uint32(b.nvidiaFabricIMEXManagementDevMinor),
			HaveMNCCManagement:           b.nvidiaMNCCManagementDevMinor >= 0,
			MNCCManagementDevMinor:       uint32(b.nvidiaMNCCManagementDevMinor),
		},
		HostTHP:                  b.hostTHP,
		SaveFDs:                  b.saveFDs.GetFDs(),
		SaveCheckpointGofer:      b.saveCheckpointGofer,
		FSSaveFDs:                b.fsSaveFDs.GetFDs(),
		FSSaveCheckpointGofer:    b.fsSaveCheckpointGofer,
		FSRestoreFDs:             b.fsRestoreFDs.GetFDs(),
		FSRestoreCheckpointGofer: b.fsRestoreCheckpointGofer,
		RootfsUpperTarFD:         b.rootfsUpperTarFD,
		NoRootContainer:          b.noRootContainer,
		StartupTimer:             timer,
	}
	timer.Reached("creating loader")
	l, err := boot.New(bootArgs)
	if err != nil {
		util.Fatalf("creating loader: %v", err)
	}
	timer.Reached("loader created")

	// Fatalf exits the process and doesn't run defers.
	// 'l' must be destroyed explicitly after this point!

	l.PreSeccompCallback = preSeccomp

	// Prepare metrics.
	// This needs to happen after the kernel is initialized (such that all metrics are registered)
	// but before the control server starts, as runsc queries registered metrics prior to sending
	// the start signal.
	metric.Initialize()
	timer.Reached("metrics initialized")
	var finalMetricsFile *os.File
	if b.finalMetricsFD != -1 {
		finalMetricsFile = os.NewFile(uintptr(b.finalMetricsFD), "final metrics file")
	}
	if b.profilingMetricsFD != -1 {
		if err := metric.StartProfilingMetrics(metric.ProfilingMetricsOptions[*os.File]{
			Sink:    os.NewFile(uintptr(b.profilingMetricsFD), "metrics file"),
			Lossy:   b.profilingMetricsLossy,
			Metrics: conf.ProfilingMetrics,
			Rate:    time.Duration(conf.ProfilingMetricsRate) * time.Microsecond,
		}); err != nil {
			l.Destroy()
			util.Fatalf("unable to start profiling metrics: %v", err)
		}
		defer metric.StopProfilingMetrics()
	}

	l.StartControlServer()

	// Wait for the start signal from runsc.
	l.WaitForStartSignal()
	timer.Reached("start signal received")

	if b.bootTimesFD != -1 {
		// Write the startup timeline before Run tells `runsc start` that the
		// sandbox has started, so that the file is complete by the time
		// `runsc start` returns.
		l.PostStartCallback = func() {
			bootTimesFile := os.NewFile(uintptr(b.bootTimesFD), "boot times file")
			if err := json.NewEncoder(bootTimesFile).Encode(timer.Export()); err != nil {
				log.Warningf("Failed to write boot times: %v", err)
			}
			if err := bootTimesFile.Close(); err != nil {
				log.Warningf("Failed to close boot times file: %v", err)
			}
		}
	}

	// Run the application and wait for it to finish.
	if err := l.Run(); err != nil {
		l.Destroy()
		util.Fatalf("running sandbox: %v", err)
	}

	timer.Log()

	ws := l.WaitExit()
	log.Infof("application exiting with %+v", ws)
	*waitStatus = unix.WaitStatus(ws)

	if finalMetricsFile != nil {
		if err := exportFinalMetrics(finalMetricsFile); err != nil {
			l.Destroy()
			util.Fatalf("unable to export final metrics: %v", err)
		}
	}

	l.Destroy()
	return subcommands.ExitSuccess
}

// validateOpenFDs checks that the sandbox process does not have any open
// directory FDs.
func validateOpenFDs(passFDs []sentryapi.FDMapping) {
	passHostFDs := make(map[int]struct{})
	for _, passFD := range passFDs {
		passHostFDs[passFD.Host] = struct{}{}
	}
	const selfFDDir = "/proc/self/fd"
	if err := filepath.WalkDir(selfFDDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type() != os.ModeSymlink {
			// All entries are symlinks. Ignore the callback for fd directory itself.
			return nil
		}
		if fdInfo, err := os.Stat(path); err != nil {
			if os.IsNotExist(err) {
				// Ignore FDs that are now closed. For example, the FD to selfFDDir that
				// was opened by filepath.WalkDir() to read dirents.
				return nil
			}
			return fmt.Errorf("os.Stat(%s) failed: %v", path, err)
		} else if !fdInfo.IsDir() {
			return nil
		}
		// Uh-oh. This is a directory FD.
		fdNo, err := strconv.Atoi(d.Name())
		if err != nil {
			return fmt.Errorf("strconv.Atoi(%s) failed: %v", d.Name(), err)
		}
		dirLink, err := os.Readlink(path)
		if err != nil {
			return fmt.Errorf("os.Readlink(%s) failed: %v", path, err)
		}
		if _, ok := passHostFDs[fdNo]; ok {
			// Passed FDs are allowed to be directories. The user must be knowing
			// what they are doing. Log a warning regardless.
			log.Warningf("Sandbox has access to FD %d, which is a directory for %s", fdNo, dirLink)
			return nil
		}
		return fmt.Errorf("FD %d is a directory for %s", fdNo, dirLink)
	}); err != nil {
		util.Fatalf("WalkDir(%s) failed: %v", selfFDDir, err)
	}
}

// exportFinalMetrics exports all metrics to the given file.
// The file is closed as part of this function.
func exportFinalMetrics(f *os.File) error {
	snapshot, err := metric.GetSnapshot(metric.SnapshotOptions{})
	if err != nil {
		return fmt.Errorf("getting metrics snapshot: %w", err)
	}
	_, err = prometheus.Write(f, prometheus.ExportOptions{}, map[*prometheus.Snapshot]prometheus.SnapshotExportOptions{
		snapshot: {
			ExporterPrefix: "runsc_",
		},
	})
	if err != nil {
		return fmt.Errorf("writing metrics snapshot: %w", err)
	}
	if err = f.Close(); err != nil {
		return fmt.Errorf("closing metrics snapshot file: %w", err)
	}
	return nil
}
