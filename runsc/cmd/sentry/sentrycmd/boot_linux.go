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
//go:build linux

package sentrycmd

import (
	"context"
	"os"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"

	"github.com/google/subcommands"
	"github.com/moby/sys/capability"
	specs "github.com/opencontainers/runtime-spec/specs-go"
	"golang.org/x/sys/unix"
	"gvisor.dev/gvisor/pkg/coretag"
	"gvisor.dev/gvisor/pkg/cpuid"
	"gvisor.dev/gvisor/pkg/hostarch"
	"gvisor.dev/gvisor/pkg/log"
	"gvisor.dev/gvisor/pkg/ring0"
	"gvisor.dev/gvisor/pkg/sentry/devices/nvproxy/nvconf"
	"gvisor.dev/gvisor/pkg/sentry/hostmm"
	"gvisor.dev/gvisor/pkg/sentry/platform/platformdesc"
	"gvisor.dev/gvisor/pkg/tcpip/nftables"
	"gvisor.dev/gvisor/runsc/cmd/sandboxsetup"
	"gvisor.dev/gvisor/runsc/cmd/util"
	"gvisor.dev/gvisor/runsc/config"
	"gvisor.dev/gvisor/runsc/flag"
	"gvisor.dev/gvisor/runsc/specutils"
	"gvisor.dev/gvisor/runsc/starttime"
)

// willReexec returns true if this boot process will re-execute itself to
// apply credential/capability changes to all of its threads, rather than
// applying capabilities to all threads in-process.
func (b *Boot) willReexec() bool {
	if config.CgoEnabled {
		// When cgo is on, we can't control non-Go threads, so need reexec.
		return true
	}
	if !b.applyCaps {
		return true // No caps to change but will change UID/GID to `nobody`.
	}
	if b.syncUsernsFD >= 0 {
		return true // Rootless mode.
	}
	return false
}

// Execute implements subcommands.Command.Execute.  It starts a sandbox in a
// waiting state.
func (b *Boot) Execute(_ context.Context, f *flag.FlagSet, args ...any) subcommands.ExitStatus {
	if b.specFD == -1 || b.controllerFD == -1 || f.NArg() != 1 {
		f.Usage()
		return subcommands.ExitUsageError
	}

	conf := args[0].(*config.Config)

	timer := starttime.Timer("runsc boot")
	timer.ReachedAt("Go code running", starttime.GoStartTime())
	timer.Reached("boot subcommand dispatched")

	if hostPageSize := unix.Getpagesize(); hostPageSize != hostarch.PageSize {
		util.Fatalf("host page size (%d) does not match compiled page size (%d)", hostPageSize, hostarch.PageSize)
	}

	// Set traceback level
	debug.SetTraceback(conf.Traceback)

	// Initialize CPUID information.
	cpuid.Initialize()
	timer.Reached("CPUID initialized")

	// Initialize ring0 library.
	ring0.InitDefault()
	timer.Reached("ring0 initialized")

	argOverride := make(map[string]string)

	// Do these before chroot takes effect, otherwise we can't read /proc and /sys.
	if len(b.productName) == 0 {
		if product, err := os.ReadFile("/sys/devices/virtual/dmi/id/product_name"); err != nil {
			if os.IsNotExist(err) {
				// Some hosts, including Firecracker VMs, do not expose DMI data.
				log.Infof("Not setting product_name: %v", err)
			} else {
				log.Warningf("Not setting product_name: %v", err)
			}
		} else {
			b.productName = strings.TrimSpace(string(product))
			log.Infof("Setting product_name: %q", b.productName)
			argOverride["product-name"] = b.productName
		}
	}
	if conf.AppHugePages {
		if len(b.hostTHP.ShmemEnabled) == 0 {
			val, err := hostmm.ReadTransparentHugepageEnum("shmem_enabled")
			if err != nil {
				log.Warningf("Failed to infer --host-thp-shmem-enabled: %v", err)
			} else {
				b.hostTHP.ShmemEnabled = val
				log.Infof("Setting host-thp-shmem-enabled: %q", b.hostTHP.ShmemEnabled)
				argOverride["host-thp-shmem-enabled"] = b.hostTHP.ShmemEnabled
			}
		}
		if len(b.hostTHP.Defrag) == 0 {
			val, err := hostmm.ReadTransparentHugepageEnum("defrag")
			if err != nil {
				log.Warningf("Failed to infer --host-thp-defrag: %v", err)
			} else {
				b.hostTHP.Defrag = val
				log.Infof("Setting host-thp-defrag: %q", b.hostTHP.Defrag)
				argOverride["host-thp-defrag"] = b.hostTHP.Defrag
			}
		}
	}
	timer.Reached("host info read")

	if b.attached {
		// Ensure this process is killed after parent process terminates when
		// attached mode is enabled. In the unfortunate event that the parent
		// terminates before this point, this process leaks.
		if err := unix.Prctl(unix.PR_SET_PDEATHSIG, uintptr(unix.SIGKILL), 0, 0, 0); err != nil {
			util.Fatalf("error setting parent death signal: %v", err)
		}
	}

	if b.syncUsernsFD >= 0 {
		sandboxsetup.SyncUsernsForRootless(b.syncUsernsFD, uint32(b.uid), uint32(b.gid))
		argOverride["sync-userns-fd"] = "-1"
	}

	// Get the spec from the specFD. We *must* keep this os.File alive past
	// the call setCapsAndCallSelf, otherwise the FD will be closed and the
	// child process cannot read it
	specFile := os.NewFile(uintptr(b.specFD), "spec file")
	spec, err := specutils.ReadSpecFromFile(b.bundleDir, specFile, specutils.SpecOpts{Conf: conf, NoRootContainer: b.noRootContainer})
	if err != nil {
		util.Fatalf("reading spec: %v", err)
	}
	timer.Reached("spec read")

	if specutils.NVProxyEnabled(spec, conf) {
		if b.cpuDMALatencyFD < 0 {
			// Best effort; requires root.
			if gPlatform, err := platformdesc.Lookup(conf.Platform); err == nil && gPlatform.Requirements.FrequentHostThreadWakeups {
				if fd := sandboxsetup.MaybeCapCPUIdleStates(); fd >= 0 {
					b.cpuDMALatencyFD = fd
					argOverride["cpu-dma-latency-fd"] = strconv.Itoa(fd)
				}
			}
		}

		if b.procDriverNvidiaParams == "" {
			driverCaps, driverCapsErr := specutils.NVProxyDriverCapsAllowed(conf)
			nvhs, err := nvconf.GetHostSettings(nvconf.HostSettingsOptions{
				WantFabricIMEXManagement: driverCapsErr == nil && driverCaps&nvconf.CapFabricIMEXManagement != 0,
			})
			if err != nil {
				log.Warningf("Failed to get nvconf.HostSettings: %v", err)
			} else {
				b.procDriverNvidiaParams = nvhs.ProcDriverNvidiaParams
				argOverride["nvidia-host-params"] = nvhs.ProcDriverNvidiaParams
				if nvhs.HaveFabricIMEXManagement {
					b.nvidiaFabricIMEXManagementDevMinor = int64(nvhs.FabricIMEXManagementDevMinor)
					argOverride["nvidia-fabric-imex-mgmt-minor"] = strconv.FormatUint(uint64(nvhs.FabricIMEXManagementDevMinor), 10)
				}
				if nvhs.HaveMNCCManagement {
					b.nvidiaMNCCManagementDevMinor = int64(nvhs.MNCCManagementDevMinor)
					argOverride["nvidia-mncc-mgmt-minor"] = strconv.FormatUint(uint64(nvhs.MNCCManagementDevMinor), 10)
				}
			}
		}
		timer.Reached("set nvproxy flags")
	}

	if b.setUpRoot {
		if err := setUpChroot(spec, conf, timer); err != nil {
			util.Fatalf("error setting up chroot: %v", err)
		}
		timer.Reached("chroot set up")
		argOverride["setup-root"] = "false"

		if !conf.Rootless {
			// /proc is umounted from a forked process, because the
			// current one is going to drop capabilities and won't be
			// able to umount it.
			if b.procUmounterFD < 0 {
				util.Fatalf("--proc-umounter-fd is required to set up the root")
			}
			umounter := os.NewFile(uintptr(b.procUmounterFD), "proc umounter")
			cmd, w := sandboxsetup.ExecProcUmounter(umounter)
			umounter.Close()
			argOverride["proc-umounter-fd"] = "-1"
			if b.willReexec() {
				defer cmd.Wait()
				defer w.Close()
			} else {
				// Keep `w` alive until UmountProcFile uses it.
				b.procMountSyncFile = w
			}
			if b.procMountSyncFD != -1 {
				panic("procMountSyncFD is set")
			}
			b.procMountSyncFD = int(w.Fd())
			argOverride["proc-mount-sync-fd"] = strconv.Itoa(b.procMountSyncFD)

			// Clear FD_CLOEXEC in case this process re-executes itself.
			// procMountSyncFD should remain open.
			if _, _, errno := unix.RawSyscall(unix.SYS_FCNTL, w.Fd(), unix.F_SETFD, 0); errno != 0 {
				util.Fatalf("error clearing CLOEXEC: %v", errno)
			}

			if !b.applyCaps {
				// Remove the args that have already been done before calling self.
				args := sandboxsetup.PrepareArgs(b.Name(), f, argOverride)

				util.Fatalf("callSelfAsNobody(%v): %v", args, sandboxsetup.CallSelfAsNobody(args))

				// This prevents the specFile finalizer from running and closed
				// the specFD, which we have passed to ourselves when
				// re-execing.
				runtime.KeepAlive(specFile)
				panic("unreachable")
			}
		}
	}

	specutils.LogSpecDebug(spec, conf.OCISeccomp)

	if b.applyCaps {
		caps := &specs.LinuxCapabilities{}

		gPlatform, err := platformdesc.Lookup(conf.Platform)
		if err != nil {
			util.Fatalf("loading platform: %v", err)
		}
		if gPlatform.Requirements.RequiresCapSysPtrace {
			// Ptrace platform requires extra capabilities.
			const c = "CAP_SYS_PTRACE"
			caps.Bounding = append(caps.Bounding, c)
			caps.Effective = append(caps.Effective, c)
			caps.Permitted = append(caps.Permitted, c)
		}

		if conf.DirectFS {
			caps = specutils.MergeCapabilities(caps, DirectfsSandboxLinuxCaps)
		}
		if conf.Network == config.NetworkHost {
			curCaps, err := capability.NewPid2(0)
			if err != nil {
				util.Fatalf("capability.NewPid2(0) failed: %v", err)
			}
			if err := curCaps.Load(); err != nil {
				util.Fatalf("unable to load capabilities: %v", err)
			}
			addCaps := []string{}
			for c, strCap := range hostnetSandboxLinuxCaps {
				if c == capability.CAP_NET_RAW && !conf.EnableRaw {
					continue
				}
				if curCaps.Get(capability.PERMITTED, c) {
					addCaps = append(addCaps, strCap)
				}
			}
			if len(addCaps) != 0 {
				caps = specutils.MergeCapabilities(caps, &specs.LinuxCapabilities{
					Bounding:  addCaps,
					Effective: addCaps,
					Permitted: addCaps,
				})
			}
		}
		if b.willReexec() {
			if config.CgoEnabled {
				log.Warningf("Need to re-exec in order to drop capabilities, due to cgo build. This slows down gVisor startup. Use a pure-Go gVisor build for faster startup.")
			} else {
				log.Infof("Re-execing. FYI, this step isn't necessary when running with root. gVisor will start up faster (and is just as secure) when started as root, as it has to do fewer hoops to get to its as-sandboxed-as-possible state.")
			}
			argOverride["apply-caps"] = "false"

			// Remove the args that have already been done before calling self.
			args := sandboxsetup.PrepareArgs(b.Name(), f, argOverride)

			util.Fatalf("setCapsAndCallSelf(%v, %v): %v", args, caps, sandboxsetup.SetCapsAndCallSelf(args, caps))

			// This prevents the specFile finalizer from running and closed
			// the specFD, which we have passed to ourselves when
			// re-execing.
			runtime.KeepAlive(specFile)
			panic("unreachable")
		}

		timer.Reached("sandbox capabilities computed")
		if err := sandboxsetup.ApplyCapsAllThreads(caps, timer); err != nil {
			util.Fatalf("applying caps to all threads: %v", err)
		}
		timer.Reached("capabilities applied")
		if b.attached {
			// Re-arm the parent death signal in case the credential change
			// cleared it. (A pure capability drop should not clear it, but
			// re-arming is free and preserves the re-exec path's behavior,
			// which re-arms after the exec.)
			if err := unix.Prctl(unix.PR_SET_PDEATHSIG, uintptr(unix.SIGKILL), 0, 0, 0); err != nil {
				util.Fatalf("error setting parent death signal: %v", err)
			}
		}
	}

	if b.syncUsernsFD >= 0 {
		// syncUsernsFD is set, but runsc hasn't been re-executed with a new UID and GID.
		// We expect that setCapsAndCallSelf has to be called in this case.
		panic("unreachable")
	}

	// Close specFile to avoid exposing it to the sandbox.
	if err := specFile.Close(); err != nil {
		util.Fatalf("closing specFile: %v", err)
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
		timer.Reached("FD limit applied")
	}

	// When mountsFD is not provided, there is no cleaning required.
	if b.mountsFD >= 0 {
		// Read resolved mount list and replace the original one from the spec.
		mountsFile := os.NewFile(uintptr(b.mountsFD), "mounts file")
		cleanMounts, err := specutils.ReadMounts(mountsFile)
		if err != nil {
			mountsFile.Close()
			util.Fatalf("Error reading mounts file: %v", err)
		}
		mountsFile.Close()
		spec.Mounts = cleanMounts
		timer.Reached("resolved mounts read")
	}

	if conf.DirectFS {
		// sandbox should run with a umask of 0, because we want to preserve file
		// modes exactly as sent by the sentry, which would have already applied
		// the application umask.
		unix.Umask(0)
	}

	if conf.EnableCoreTags {
		tag, err := coretag.Get()
		if err != nil {
			util.Fatalf("Cannot get Sentry core tag: %v", err)
		}
		if tag == 0 {
			util.Fatalf("Core tagging is enabled, but the Sentry has no core tag. Aborting.")
		}
		log.Infof("Core tag enabled (core tag=%d)", tag)
	}

	// NFtables is only supported for netstack.
	if (conf.Network == config.NetworkNone || conf.Network == config.NetworkSandbox) && conf.Nftables {
		nftables.EnableNFTables()
	}

	var preSeccomp func()
	if b.procMountSyncFD != -1 {
		passFDs := b.passFDs.GetArray()
		preSeccomp = func() {
			// Call validateOpenFDs() before umounting /proc.
			validateOpenFDs(passFDs)
			// This callback runs on the same goroutine as the rest of sandbox
			// startup (for now at least...), so safe to record here.
			timer.Reached("open FDs validated")
			// Umount /proc right before installing seccomp filters.
			if b.procMountSyncFile != nil {
				sandboxsetup.UmountProcFile(b.procMountSyncFile)
			} else {
				sandboxsetup.UmountProc(b.procMountSyncFD)
			}
		}
	}
	return b.runLoader(f, conf, spec, timer, args[1].(*unix.WaitStatus), preSeccomp)
}
