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

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	specs "github.com/opencontainers/runtime-spec/specs-go"
	"golang.org/x/sys/unix"
	"gvisor.dev/gvisor/pkg/control/client"
	"gvisor.dev/gvisor/pkg/fd"
	"gvisor.dev/gvisor/pkg/log"
	"gvisor.dev/gvisor/pkg/timing"
	"gvisor.dev/gvisor/runsc/boot"
	"gvisor.dev/gvisor/runsc/boot/sentryapi"
	"gvisor.dev/gvisor/runsc/config"
	"gvisor.dev/gvisor/runsc/flag"
	"gvisor.dev/gvisor/runsc/specutils"
)

// The Sentry runs as `runsc boot` does: hvfboot makes an OCI spec and a
// runsc configuration, gives boot.New the host FDs in the order that `runsc
// boot` gets them, and starts the root container over the control socket.

// containerID is the ID of the sandbox and of its root container.
const containerID = "hvfboot"

// defaultCaps are the capabilities of the program: the default set of Docker.
var defaultCaps = []string{
	"CAP_AUDIT_WRITE",
	"CAP_CHOWN",
	"CAP_DAC_OVERRIDE",
	"CAP_FOWNER",
	"CAP_FSETID",
	"CAP_KILL",
	"CAP_MKNOD",
	"CAP_NET_BIND_SERVICE",
	"CAP_NET_RAW",
	"CAP_SETFCAP",
	"CAP_SETGID",
	"CAP_SETPCAP",
	"CAP_SETUID",
	"CAP_SYS_CHROOT",
}

// newConfig returns the runsc configuration of the sandbox.
func newConfig(network string, debug, strace bool) (*config.Config, error) {
	fs := flag.NewFlagSet("runsc", flag.ContinueOnError)
	config.RegisterFlags(fs)
	for name, value := range map[string]string{
		"platform": "hvf",
		"network":  network,
		// darwin has no O_PATH, which directfs needs.
		"directfs": "false",
		// The root filesystem is an EROFS image with a tmpfs overlay in
		// application memory, as -overlay2=root:memory.
		"overlay2":       "root:memory",
		"ignore-cgroups": "true",
		"debug":          fmt.Sprint(debug),
		"strace":         fmt.Sprint(strace),
	} {
		if err := fs.Set(name, value); err != nil {
			return nil, fmt.Errorf("setting --%s=%s: %w", name, value, err)
		}
	}
	conf, err := config.NewFromFlags(fs)
	if err != nil {
		return nil, err
	}
	// darwin has no seccomp; confineSentry applies a Seatbelt profile.
	conf.DisableSeccomp = true
	return conf, nil
}

// newSpec returns the OCI spec of a container that runs argv with env, with
// the volumes as bind mounts.
func newSpec(argv, env []string, volumes []volume) *specs.Spec {
	caps := &specs.LinuxCapabilities{
		Bounding:  defaultCaps,
		Effective: defaultCaps,
		Permitted: defaultCaps,
	}
	spec := &specs.Spec{
		Version:  specs.Version,
		Hostname: "gvisor",
		Root:     &specs.Root{Path: "/"},
		Process: &specs.Process{
			Args:         argv,
			Env:          env,
			Cwd:          "/",
			Capabilities: caps,
		},
		Mounts: []specs.Mount{
			{Destination: "/proc", Type: "proc", Source: "proc"},
			{Destination: "/dev", Type: "tmpfs", Source: "tmpfs", Options: []string{"nosuid", "strictatime", "mode=755", "size=65536k"}},
			{Destination: "/dev/pts", Type: "devpts", Source: "devpts", Options: []string{"nosuid", "noexec", "newinstance", "ptmxmode=0666", "mode=0620"}},
			{Destination: "/dev/shm", Type: "tmpfs", Source: "shm", Options: []string{"nosuid", "noexec", "nodev", "mode=1777", "size=65536k"}},
			{Destination: "/sys", Type: "sysfs", Source: "sysfs", Options: []string{"nosuid", "noexec", "nodev", "ro"}},
		},
		Linux: &specs.Linux{},
	}
	for _, v := range volumes {
		opts := []string{"rbind"}
		if v.readonly {
			opts = append(opts, "ro")
		}
		spec.Mounts = append(spec.Mounts, specs.Mount{
			Destination: v.target,
			Type:        "bind",
			Source:      v.source,
			Options:     opts,
		})
	}
	return spec
}

// newControlSocket returns a listening Unix socket for the control server in
// a new directory, and a function that removes the directory.
func newControlSocket() (int, string, func(), error) {
	dir, err := os.MkdirTemp("", "hvfboot-")
	if err != nil {
		return -1, "", nil, err
	}
	cleanup := func() { os.RemoveAll(dir) }
	path := filepath.Join(dir, "control")
	sock, err := unix.Socket(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		cleanup()
		return -1, "", nil, err
	}
	unix.CloseOnExec(sock)
	if err := unix.Bind(sock, &unix.SockaddrUnix{Name: path}); err != nil {
		unix.Close(sock)
		cleanup()
		return -1, "", nil, err
	}
	if err := unix.Listen(sock, 16); err != nil {
		unix.Close(sock)
		cleanup()
		return -1, "", nil, err
	}
	return sock, path, cleanup, nil
}

// runSandbox runs argv in a sandbox made by boot.Loader, and returns its exit
// code.
func runSandbox(imagePath string, argv []string) (int, error) {
	conf, err := newConfig(*network, *debug, *straceFlag)
	if err != nil {
		return 0, err
	}
	spec := newSpec(argv, mergeEnv(defaultEnv, envv), volumes)
	mountConfs := []specutils.GoferMountConf{{Lower: specutils.Erofs, Upper: specutils.MemoryOverlay}}
	for range volumes {
		mountConfs = append(mountConfs, specutils.GoferMountConf{Lower: specutils.Lisafs, Upper: specutils.NoOverlay})
	}

	// Open all host resources, then confine the Sentry before it parses
	// untrusted data (the image and the program).
	imageFD, err := unix.Open(imagePath, unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return 0, fmt.Errorf("opening %s: %w", imagePath, err)
	}
	goferFDs := []int{imageFD}
	if len(volumes) > 0 {
		var mounts []goferMount
		for _, v := range volumes {
			mounts = append(mounts, v.goferMount)
		}
		// The gofer exits when its connections close, at the latest when the
		// Sentry exits. hvfboot does not wait for it.
		_, socks, err := startGofer(mounts, *fdLimit, *debug)
		if err != nil {
			return 0, err
		}
		for _, s := range socks {
			// The Loader owns the FDs.
			sfd, err := unix.Dup(int(s.Fd()))
			s.Close()
			if err != nil {
				return 0, err
			}
			goferFDs = append(goferFDs, sfd)
		}
	}
	var stdioFDs []int
	for i := 0; i < 3; i++ {
		dup, err := unix.Dup(i)
		if err != nil {
			return 0, fmt.Errorf("dup(%d): %w", i, err)
		}
		stdioFDs = append(stdioFDs, dup)
	}
	ctrlFD, ctrlPath, removeCtrl, err := newControlSocket()
	if err != nil {
		return 0, fmt.Errorf("creating the control socket: %w", err)
	}
	defer removeCtrl()

	l, err := boot.New(boot.Args{
		ID:                containerID,
		Spec:              spec,
		Conf:              conf,
		ControllerFD:      ctrlFD,
		PinRingFD:         -1,
		CPUDMALatencyFD:   -1,
		Device:            fd.New(-1),
		GoferFDs:          goferFDs,
		DevGoferFD:        -1,
		StdioFDs:          stdioFDs,
		ExecFD:            -1,
		GoferMountConfs:   mountConfs,
		NumCPU:            runtime.NumCPU(),
		LifecycleNumCPU:   runtime.NumCPU(),
		UserLogFD:         -1,
		PodInitConfigFD:   -1,
		RootfsUpperTarFD:  -1,
		StartupTimer:      timing.New("hvfboot", time.Now()),
		GoferFilestoreFDs: nil,
	})
	if err != nil {
		return 0, fmt.Errorf("creating the loader: %w", err)
	}
	defer l.Destroy()

	// Connect to the control server before confinement denies it.
	ctrl, err := client.ConnectTo(ctrlPath)
	if err != nil {
		return 0, fmt.Errorf("connecting to the control server: %w", err)
	}
	defer ctrl.Close()

	var sources []string
	for _, v := range volumes {
		sources = append(sources, v.source)
	}
	if err := confineSentry(sources); err != nil {
		return 0, err
	}

	l.StartControlServer()
	startErr := make(chan error, 1)
	go func() {
		cid := containerID
		startErr <- ctrl.Call(sentryapi.ContMgrRootContainerStart, &cid, nil)
	}()
	l.WaitForStartSignal()
	if err := l.Run(); err != nil {
		return 0, fmt.Errorf("running the sandbox: %w", err)
	}
	if err := <-startErr; err != nil {
		return 0, fmt.Errorf("starting the root container: %w", err)
	}
	ws := l.WaitExit()
	log.Infof("Application exiting with %+v", ws)
	return exitCode(ws), nil
}
