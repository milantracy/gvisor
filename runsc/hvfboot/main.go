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

// Binary hvfboot runs a program in a gVisor Sentry on the hvf platform, with
// an EROFS image as the root filesystem and the standard I/O of hvfboot.
//
//	hvfboot -image root.erofs /bin/busybox echo hello
//
// The image must have blocks of at least 16 KiB and no inline file data
// (mkfs.erofs -b 16384 -E noinline_data); make_image.sh makes one from a
// container image. The root filesystem is read-only; /proc, /dev and a tmpfs
// /tmp are mounted on it.
//
// The //runsc/hvfboot:hvfboot target is signed with the hypervisor
// entitlement at build time. A binary without the entitlement signs a copy of
// itself and runs that copy.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"runtime"
	"strings"

	"golang.org/x/sys/unix"
	"gvisor.dev/gvisor/pkg/abi/linux"
	"gvisor.dev/gvisor/pkg/context"
	"gvisor.dev/gvisor/pkg/cpuid"
	"gvisor.dev/gvisor/pkg/fd"
	"gvisor.dev/gvisor/pkg/fspath"
	hvfapi "gvisor.dev/gvisor/pkg/hvf"
	"gvisor.dev/gvisor/pkg/hvf/entitle"
	"gvisor.dev/gvisor/pkg/log"
	"gvisor.dev/gvisor/pkg/memutil"
	"gvisor.dev/gvisor/pkg/sentry/devices/memdev"
	"gvisor.dev/gvisor/pkg/sentry/fdimport"
	"gvisor.dev/gvisor/pkg/sentry/fsimpl/cgroup2fs"
	"gvisor.dev/gvisor/pkg/sentry/fsimpl/dev"
	"gvisor.dev/gvisor/pkg/sentry/fsimpl/erofs"
	"gvisor.dev/gvisor/pkg/sentry/fsimpl/host"
	"gvisor.dev/gvisor/pkg/sentry/fsimpl/proc"
	"gvisor.dev/gvisor/pkg/sentry/fsimpl/tmpfs"
	"gvisor.dev/gvisor/pkg/sentry/kernel"
	"gvisor.dev/gvisor/pkg/sentry/kernel/auth"
	"gvisor.dev/gvisor/pkg/sentry/limits"
	"gvisor.dev/gvisor/pkg/sentry/loader"
	"gvisor.dev/gvisor/pkg/sentry/pgalloc"
	"gvisor.dev/gvisor/pkg/sentry/platform/hvf"
	"gvisor.dev/gvisor/pkg/sentry/strace"
	_ "gvisor.dev/gvisor/pkg/sentry/syscalls/linux" // Registers the Linux system call table.
	"gvisor.dev/gvisor/pkg/sentry/time"
	"gvisor.dev/gvisor/pkg/sentry/usage"
	"gvisor.dev/gvisor/pkg/sentry/vfs"
)

// envFlag is a repeatable KEY=VALUE flag.
type envFlag []string

func (e *envFlag) String() string { return strings.Join(*e, " ") }

func (e *envFlag) Set(v string) error {
	if !strings.Contains(v, "=") {
		return fmt.Errorf("%q is not KEY=VALUE", v)
	}
	*e = append(*e, v)
	return nil
}

// defaultEnv is the environment of the program, before -e.
var defaultEnv = []string{
	"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
	"HOME=/root",
	"LANG=C.UTF-8",
}

var (
	envv       envFlag
	image      = flag.String("image", "", "EROFS image of the root filesystem")
	debug      = flag.Bool("debug", false, "log at debug level")
	straceFlag = flag.Bool("strace", false, "log system calls (implies -debug)")
)

func main() {
	flag.Var(&envv, "e", "set KEY=VALUE in the environment of the program (repeatable)")
	flag.Parse()
	if *image == "" || flag.NArg() == 0 {
		fmt.Fprintf(os.Stderr, "usage: hvfboot -image <root.erofs> <program> [args...]\n")
		os.Exit(2)
	}
	cpuid.Initialize()
	plat, err := hvf.New()
	if errors.Is(err, hvfapi.ErrDenied) && os.Getenv(entitle.SignedEnv) == "" {
		// The binary does not have the hypervisor entitlement.
		code, err := entitle.Reexec()
		if err != nil {
			fmt.Fprintf(os.Stderr, "hvfboot: %v\n", err)
			os.Exit(1)
		}
		os.Exit(code)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "hvfboot: creating the hvf platform: %v\n", err)
		os.Exit(1)
	}
	if *debug || *straceFlag {
		log.SetLevel(log.Debug)
	}
	if *straceFlag {
		strace.Initialize()
		strace.EnableAll(strace.SinkTypeLog)
	}
	code, err := run(plat, *image, flag.Args())
	if err != nil {
		fmt.Fprintf(os.Stderr, "hvfboot: %v\n", err)
		os.Exit(1)
	}
	os.Exit(code)
}

func run(plat *hvf.HVF, imagePath string, argv []string) (int, error) {
	if err := usage.Init(); err != nil {
		return 0, fmt.Errorf("setting up memory accounting: %w", err)
	}
	k := &kernel.Kernel{Platform: plat}

	memfd, err := memutil.CreateMemFD("hvfboot-memory", 0)
	if err != nil {
		return 0, fmt.Errorf("creating the memory file: %w", err)
	}
	mf, err := pgalloc.NewMemoryFile(os.NewFile(uintptr(memfd), "hvfboot-memory"), pgalloc.MemoryFileOpts{
		DisableIMAWorkAround: true,
	})
	if err != nil {
		return 0, fmt.Errorf("creating the memory file: %w", err)
	}
	k.SetMemoryFile(mf)

	vdso, err := loader.PrepareVDSO(mf)
	if err != nil {
		return 0, fmt.Errorf("creating the vDSO: %w", err)
	}
	tk := kernel.NewTimekeeper()
	params := kernel.NewVDSOParamPage(mf, vdso.ParamPage.FileRange())
	tk.SetClocks(time.NewCalibratedClocks(false), params)

	creds := auth.NewRootCredentials(auth.NewRootUserNamespace())
	if err := k.Init(kernel.InitKernelArgs{
		ApplicationCores:  uint(runtime.NumCPU()),
		FeatureSet:        cpuid.HostFeatureSet(),
		Timekeeper:        tk,
		RootUserNamespace: creds.UserNamespace,
		Vdso:              vdso,
		VdsoParams:        params,
		RootUTSNamespace:  kernel.NewUTSNamespace("gvisor", "gvisor", creds.UserNamespace),
		RootIPCNamespace:  kernel.NewIPCNamespace(creds.UserNamespace),
		RootPIDNamespace:  kernel.NewRootPIDNamespace(creds.UserNamespace),
		Cgroup2FSInit:     cgroup2fs.NewFilesystem,
	}); err != nil {
		return 0, fmt.Errorf("initializing the kernel: %w", err)
	}
	ctx := k.SupervisorContext()
	vfsObj := k.VFS()
	vfsObj.MustRegisterFilesystemType(erofs.Name, &erofs.FilesystemType{}, &vfs.RegisterFilesystemTypeOptions{})
	vfsObj.MustRegisterFilesystemType(tmpfs.Name, &tmpfs.FilesystemType{}, &vfs.RegisterFilesystemTypeOptions{AllowUserMount: true})
	vfsObj.MustRegisterFilesystemType(proc.Name, &proc.FilesystemType{}, &vfs.RegisterFilesystemTypeOptions{AllowUserMount: true})
	vfsObj.MustRegisterFilesystemType(dev.Name, &dev.FilesystemType{}, &vfs.RegisterFilesystemTypeOptions{})
	if err := memdev.Register(vfsObj); err != nil {
		return 0, fmt.Errorf("registering memory devices: %w", err)
	}

	hostFS, err := host.NewFilesystem(vfsObj)
	if err != nil {
		return 0, fmt.Errorf("creating the host filesystem: %w", err)
	}
	defer hostFS.DecRef(ctx)
	k.SetHostMount(vfsObj.NewDisconnectedMount(hostFS, nil, &vfs.MountOptions{}))

	imageFD, err := unix.Open(imagePath, unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return 0, fmt.Errorf("opening %s: %w", imagePath, err)
	}
	mntns, err := vfsObj.NewMountNamespace(ctx, creds, "", erofs.Name, &vfs.MountOptions{
		ReadOnly: true,
		GetFilesystemOptions: vfs.GetFilesystemOptions{
			InternalMount: true,
			Data:          fmt.Sprintf("ifd=%d", imageFD),
		},
	}, nil)
	if err != nil {
		return 0, fmt.Errorf("mounting %s: %w", imagePath, err)
	}
	for _, m := range []struct{ target, fsType string }{
		{"/proc", proc.Name},
		{"/dev", dev.Name},
		{"/tmp", tmpfs.Name},
	} {
		if err := mountAt(ctx, vfsObj, creds, mntns, m.target, m.fsType); err != nil {
			return 0, err
		}
	}

	fdTable := k.NewFDTable()
	stdio := make(map[int]*fd.FD)
	for i := 0; i < 3; i++ {
		dup, err := unix.Dup(i)
		if err != nil {
			return 0, fmt.Errorf("dup(%d): %w", i, err)
		}
		stdio[i] = fd.New(dup)
	}
	if _, err := fdimport.Import(ctx, fdTable, stdio, fdimport.ImportOptions{UID: auth.NoID, GID: auth.NoID}); err != nil {
		return 0, fmt.Errorf("importing standard I/O: %w", err)
	}

	ls, err := limits.NewLinuxLimitSet()
	if err != nil {
		return 0, err
	}
	tg, _, err := k.CreateProcess(kernel.CreateProcessArgs{
		Filename:             argv[0],
		Argv:                 argv,
		Envv:                 mergeEnv(defaultEnv, envv),
		WorkingDirectory:     "/",
		Credentials:          creds,
		FDTable:              fdTable,
		Umask:                0022,
		Limits:               ls,
		MaxSymlinkTraversals: linux.MaxSymlinkTraversals,
		UTSNamespace:         k.RootUTSNamespace(),
		IPCNamespace:         k.RootIPCNamespace(),
		PIDNamespace:         k.RootPIDNamespace(),
		MountNamespace:       mntns,
	})
	if err != nil {
		return 0, fmt.Errorf("creating the process: %w", err)
	}
	if err := k.Start(); err != nil {
		return 0, fmt.Errorf("starting the kernel: %w", err)
	}
	k.WaitExited()
	return int(tg.ExitStatus().ExitStatus()), nil
}

// mountAt mounts a new filesystem of type fsType at target in mntns. The target
// directory must exist.
func mountAt(ctx context.Context, vfsObj *vfs.VirtualFilesystem, creds *auth.Credentials, mntns *vfs.MountNamespace, target, fsType string) error {
	root := mntns.Root(ctx)
	defer root.DecRef(ctx)
	pop := &vfs.PathOperation{Root: root, Start: root, Path: fspath.Parse(target)}
	opts := &vfs.MountOptions{GetFilesystemOptions: vfs.GetFilesystemOptions{InternalMount: true}}
	if _, err := vfsObj.MountAt(ctx, creds, fsType, pop, fsType, opts); err != nil {
		return fmt.Errorf("mounting %s at %s: %w", fsType, target, err)
	}
	return nil
}

// mergeEnv returns base with the entries of override added; an entry of
// override replaces the entry of base with the same key.
func mergeEnv(base, override []string) []string {
	env := append([]string(nil), base...)
	for _, kv := range override {
		key := kv[:strings.Index(kv, "=")+1]
		replaced := false
		for i, b := range env {
			if strings.HasPrefix(b, key) {
				env[i] = kv
				replaced = true
				break
			}
		}
		if !replaced {
			env = append(env, kv)
		}
	}
	return env
}
