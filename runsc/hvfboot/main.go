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
// (mkfs.erofs -b 16384 -E noinline_data); make_busybox_image.sh makes one. The
// binary signs a copy of itself with the hypervisor entitlement and runs it.
package main

import (
	"flag"
	"fmt"
	"os"
	"runtime"

	"golang.org/x/sys/unix"
	"gvisor.dev/gvisor/pkg/abi/linux"
	"gvisor.dev/gvisor/pkg/cpuid"
	"gvisor.dev/gvisor/pkg/fd"
	"gvisor.dev/gvisor/pkg/hvf/entitle"
	"gvisor.dev/gvisor/pkg/log"
	"gvisor.dev/gvisor/pkg/memutil"
	"gvisor.dev/gvisor/pkg/sentry/fdimport"
	"gvisor.dev/gvisor/pkg/sentry/fsimpl/cgroup2fs"
	"gvisor.dev/gvisor/pkg/sentry/fsimpl/erofs"
	"gvisor.dev/gvisor/pkg/sentry/fsimpl/host"
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

var (
	image      = flag.String("image", "", "EROFS image of the root filesystem")
	debug      = flag.Bool("debug", false, "log at debug level")
	straceFlag = flag.Bool("strace", false, "log system calls (implies -debug)")
)

func main() {
	flag.Parse()
	if *image == "" || flag.NArg() == 0 {
		fmt.Fprintf(os.Stderr, "usage: hvfboot -image <root.erofs> <program> [args...]\n")
		os.Exit(2)
	}
	if os.Getenv(entitle.SignedEnv) == "" {
		code, err := entitle.Reexec()
		if err != nil {
			fmt.Fprintf(os.Stderr, "hvfboot: %v\n", err)
			os.Exit(1)
		}
		os.Exit(code)
	}
	if *debug || *straceFlag {
		log.SetLevel(log.Debug)
	}
	if *straceFlag {
		strace.Initialize()
		strace.EnableAll(strace.SinkTypeLog)
	}
	code, err := run(*image, flag.Args())
	if err != nil {
		fmt.Fprintf(os.Stderr, "hvfboot: %v\n", err)
		os.Exit(1)
	}
	os.Exit(code)
}

func run(imagePath string, argv []string) (int, error) {
	cpuid.Initialize()
	if err := usage.Init(); err != nil {
		return 0, fmt.Errorf("setting up memory accounting: %w", err)
	}
	plat, err := hvf.New()
	if err != nil {
		return 0, fmt.Errorf("creating the hvf platform: %w", err)
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
		Envv:                 []string{"PATH=/bin:/sbin:/usr/bin:/usr/sbin", "HOME=/"},
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
