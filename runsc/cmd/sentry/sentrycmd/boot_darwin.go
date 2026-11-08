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

package sentrycmd

import (
	"context"
	"os"
	"runtime/debug"

	"github.com/google/subcommands"
	"golang.org/x/sys/unix"
	"gvisor.dev/gvisor/pkg/cpuid"
	"gvisor.dev/gvisor/pkg/hostarch"
	"gvisor.dev/gvisor/pkg/log"
	"gvisor.dev/gvisor/pkg/tcpip/nftables"
	"gvisor.dev/gvisor/runsc/cmd/util"
	"gvisor.dev/gvisor/runsc/config"
	"gvisor.dev/gvisor/runsc/flag"
	"gvisor.dev/gvisor/runsc/specutils"
	"gvisor.dev/gvisor/runsc/starttime"
)

// Execute implements subcommands.Command.Execute. It starts a sandbox in a
// waiting state.
//
// darwin has no mount namespaces, pivot_root(2) without root, capabilities
// or user namespaces. So the Sentry does not set up a root or change its
// credentials. It is confined by the Seatbelt profile that the Loader applies
// in place of seccomp (see runsc/boot/filter), before it loads the
// application.
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
	if b.setUpRoot || b.applyCaps || b.syncUsernsFD >= 0 || b.procUmounterFD >= 0 {
		util.Fatalf("--setup-root, --apply-caps, --sync-userns-fd and --proc-umounter-fd are not supported on darwin")
	}

	debug.SetTraceback(conf.Traceback)
	cpuid.Initialize()
	timer.Reached("CPUID initialized")

	if b.attached {
		// darwin has no PR_SET_PDEATHSIG.
		if err := killOnParentExit(); err != nil {
			util.Fatalf("watching the parent process: %v", err)
		}
	}

	specFile := os.NewFile(uintptr(b.specFD), "spec file")
	spec, err := specutils.ReadSpecFromFile(b.bundleDir, specFile, specutils.SpecOpts{Conf: conf, NoRootContainer: b.noRootContainer})
	if err != nil {
		util.Fatalf("reading spec: %v", err)
	}
	timer.Reached("spec read")
	specutils.LogSpecDebug(spec, conf.OCISeccomp)
	if err := specFile.Close(); err != nil {
		util.Fatalf("closing specFile: %v", err)
	}

	if conf.FDLimit > -1 {
		rlimit := unix.Rlimit{
			Cur: uint64(conf.FDLimit),
			Max: uint64(conf.FDLimit),
		}
		switch err := unix.Setrlimit(unix.RLIMIT_NOFILE, &rlimit); err {
		case nil:
		case unix.EPERM, unix.EINVAL:
			log.Warningf("FD limit %d is higher than the current hard limit or system-wide maximum", conf.FDLimit)
		default:
			util.Fatalf("Failed to set RLIMIT_NOFILE: %v", err)
		}
		timer.Reached("FD limit applied")
	}

	if b.mountsFD >= 0 {
		mountsFile := os.NewFile(uintptr(b.mountsFD), "mounts file")
		cleanMounts, err := specutils.ReadMounts(mountsFile)
		mountsFile.Close()
		if err != nil {
			util.Fatalf("Error reading mounts file: %v", err)
		}
		spec.Mounts = cleanMounts
		timer.Reached("resolved mounts read")
	}

	if (conf.Network == config.NetworkNone || conf.Network == config.NetworkSandbox) && conf.Nftables {
		nftables.EnableNFTables()
	}

	return b.runLoader(f, conf, spec, timer, args[1].(*unix.WaitStatus), nil)
}

// killOnParentExit starts a goroutine that kills this process when its
// parent exits.
func killOnParentExit() error {
	kq, err := unix.Kqueue()
	if err != nil {
		return err
	}
	ppid := os.Getppid()
	changes := []unix.Kevent_t{{
		Ident:  uint64(ppid),
		Filter: unix.EVFILT_PROC,
		Flags:  unix.EV_ADD | unix.EV_ONESHOT,
		Fflags: unix.NOTE_EXIT,
	}}
	if _, err := unix.Kevent(kq, changes, nil, nil); err != nil {
		unix.Close(kq)
		return err
	}
	// The parent may have exited before the kevent was added; then the
	// process has been reparented to launchd.
	if os.Getppid() != ppid {
		unix.Kill(os.Getpid(), unix.SIGKILL)
	}
	go func() {
		events := make([]unix.Kevent_t, 1)
		for {
			n, err := unix.Kevent(kq, nil, events, nil)
			if err == unix.EINTR {
				continue
			}
			if err != nil {
				log.Warningf("Waiting for the parent process to exit: %v", err)
				return
			}
			if n == 1 {
				log.Warningf("Parent process %d exited; killing the sandbox", ppid)
				unix.Kill(os.Getpid(), unix.SIGKILL)
			}
		}
	}()
	return nil
}
