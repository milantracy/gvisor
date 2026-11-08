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

package container

import (
	"fmt"
	"os"
	"os/exec"

	"golang.org/x/sys/unix"
	"gvisor.dev/gvisor/pkg/log"
	"gvisor.dev/gvisor/runsc/boot/sentryapi"
	"gvisor.dev/gvisor/runsc/cgroup"
	"gvisor.dev/gvisor/runsc/config"
	"gvisor.dev/gvisor/runsc/donation"
	"gvisor.dev/gvisor/runsc/profile"
	"gvisor.dev/gvisor/runsc/sandbox"
	"gvisor.dev/gvisor/runsc/specutils"
	"gvisor.dev/gvisor/runsc/starttime"
)

// startGoferProcess starts the gofer process of c, and returns the same
// values as createGoferProcess.
//
// darwin has no namespaces, cgroups or PR_SET_PDEATHSIG. The gofer runs as
// the user in the host filesystem, and a Seatbelt profile limits it to the
// paths that it serves (see runsc/gofer/fs/filter). The gofer exits when the
// sandbox closes its connections.
func (c *Container) startGoferProcess(conf *config.Config, specFile *os.File, mountHints *sentryapi.PodMountHints, attached bool, cloneIntoCgroupFD *os.File, cg cgroup.Cgroup, cpuLimits *sandbox.CPULimits, rootfsHint *sentryapi.RootfsHint) ([]*os.File, []*os.File, *os.File, *os.File, error) {
	if shouldCreateDeviceGofer(c.Spec, conf) {
		return nil, nil, nil, nil, fmt.Errorf("device gofers are not supported on darwin")
	}
	for _, m := range c.Spec.Mounts {
		if specutils.IsIDMappedMount(m) {
			return nil, nil, nil, nil, fmt.Errorf("ID-mapped mount %q is not supported on darwin", m.Destination)
		}
	}

	// Ensure we don't leak FDs to the gofer process.
	if err := sandbox.SetCloExeOnAllFDs(); err != nil {
		return nil, nil, nil, nil, fmt.Errorf("setting CLOEXEC on all FDs: %w", err)
	}

	donations := donation.Agency{}
	defer donations.Close()

	if err := donations.DonateLogFile("log-fd", conf.LogFilename, os.O_CREATE|os.O_WRONLY|os.O_APPEND, &log.DefaultFileOpts{}); err != nil {
		return nil, nil, nil, nil, err
	}
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

	cmd := exec.Command(specutils.ExePath, conf.ToFlags()...)
	cmd.Env = goferEnv(conf, os.Environ(), goferGOMAXPROCS(cg, cpuLimits))
	// Detach from session. Otherwise, signals sent to the foreground process
	// will also be forwarded by this process, resulting in duplicate signals.
	cmd.SysProcAttr = &unix.SysProcAttr{Setsid: true}
	cmd.Args[0] = "runsc-gofer"

	nextFD := donations.Transfer(cmd, 3)

	cmd.Args = append(cmd.Args, "gofer", "--bundle", c.BundleDir, "--apply-caps=false", "--setup-root=false")
	cmd.Args = append(cmd.Args, "--gofer-mount-confs="+c.GoferMountConfs.String())

	donations.Donate("spec-fd", specFile)
	if err := profile.DonateProfileFDs(conf, &donations, true /* isGofer */, lfOpts); err != nil {
		return nil, nil, nil, nil, fmt.Errorf("donating gofer profile fds: %w", err)
	}

	mountsSand, mountsGofer, err := os.Pipe()
	if err != nil {
		return nil, nil, nil, nil, err
	}
	donations.DonateAndClose("mounts-fd", mountsGofer)

	var sandEnds []*os.File
	addIOFile := func(goferConf specutils.GoferMountConf, source string) error {
		switch {
		case goferConf.ShouldUseLisafs():
			return createLisafsSocketPair(&sandEnds, &donations)
		case goferConf.ShouldUseErofs():
			f, err := os.Open(source)
			if err != nil {
				return fmt.Errorf("opening EROFS image %q: %v", source, err)
			}
			sandEnds = append(sandEnds, f)
		}
		return nil
	}
	rootSource := ""
	if rootfsHint != nil {
		rootSource = rootfsHint.Mount.Source
	}
	if err := addIOFile(c.GoferMountConfs[0], rootSource); err != nil {
		return nil, nil, nil, nil, err
	}
	cfgIdx := 1
	for _, m := range c.Spec.Mounts {
		if !specutils.HasMountConfig(m) {
			continue
		}
		if err := addIOFile(c.GoferMountConfs[cfgIdx], m.Source); err != nil {
			return nil, nil, nil, nil, err
		}
		cfgIdx++
	}

	donations.Transfer(cmd, nextFD)
	cmd.Args = append(cmd.Args, c.ID)
	donation.LogDonations(cmd)

	log.Debugf("Starting gofer: %s %v", cmd.Path, cmd.Args)
	if err := cmd.Start(); err != nil {
		return nil, nil, nil, nil, fmt.Errorf("gofer: %v", err)
	}
	log.Infof("Gofer started, PID: %d", cmd.Process.Pid)
	c.GoferPid.Store(cmd.Process.Pid)
	c.goferIsChild = true
	if c.GoferPidIdentity, err = specutils.ProcessIdentity(cmd.Process.Pid); err != nil {
		return nil, nil, nil, nil, err
	}

	goferFilestores, err := c.createGoferFilestores(conf.GetOverlay2(), mountHints)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("creating gofer filestore files: %w", err)
	}
	return sandEnds, goferFilestores, nil, mountsSand, nil
}

// createLisafsSocketPair creates a socket pair for one lisafs connection. It
// appends the sandbox end to sandEnds, and donates the gofer end as io-fds.
func createLisafsSocketPair(sandEnds *[]*os.File, donations *donation.Agency) error {
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		return err
	}
	// darwin has no SOCK_CLOEXEC. The FDs are donated before any other
	// process starts.
	unix.CloseOnExec(fds[0])
	unix.CloseOnExec(fds[1])
	*sandEnds = append(*sandEnds, os.NewFile(uintptr(fds[0]), "sandbox IO FD"))
	donations.DonateAndClose("io-fds", os.NewFile(uintptr(fds[1]), "gofer IO FD"))
	return nil
}

// goferRootfsPath returns the host path of the root filesystem of the gofer
// process pid. On darwin, the gofer sees the host filesystem.
func goferRootfsPath(pid int) string {
	return "/"
}

// createFileBeneath creates the file name in the directory dirFD, with mode
// 0666 and the open(2) flags. name must be one path component; it is not
// followed if it is a symlink.
func createFileBeneath(dirFD int, name string, flags int) (int, error) {
	return unix.Openat(dirFD, name, flags|unix.O_NOFOLLOW, 0666)
}
