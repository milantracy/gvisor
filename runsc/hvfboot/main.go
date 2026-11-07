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
// -v host:guest[:ro] mounts a host file or directory at guest. As in runsc,
// one gofer process (`hvfboot gofer`) serves all host mounts over lisafs,
// with one socket pair per mount:
//
//	hvfboot -image root.erofs -v $HOME/src:/mnt/src:ro /bin/ls /mnt/src
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
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
	"gvisor.dev/gvisor/pkg/abi/linux"
	"gvisor.dev/gvisor/pkg/context"
	"gvisor.dev/gvisor/pkg/cpuid"
	"gvisor.dev/gvisor/pkg/errors/linuxerr"
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
	"gvisor.dev/gvisor/pkg/sentry/fsimpl/gofer"
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

// volume is a -v mount.
type volume struct {
	goferMount
	target string
	isDir  bool
}

// volumesFlag is a repeatable host:guest[:ro] flag.
type volumesFlag []volume

func (f *volumesFlag) String() string {
	var s []string
	for _, v := range *f {
		s = append(s, v.source+":"+v.target)
	}
	return strings.Join(s, " ")
}

func (f *volumesFlag) Set(v string) error {
	parts := strings.Split(v, ":")
	readonly := false
	if len(parts) == 3 && parts[2] == "ro" {
		readonly = true
		parts = parts[:2]
	}
	if len(parts) != 2 || parts[0] == "" || !filepath.IsAbs(parts[1]) {
		return fmt.Errorf("%q is not host:guest[:ro] with an absolute guest path", v)
	}
	// The gofer opens files again by path and allows no symlinks in it, so
	// it serves the real path.
	source, err := filepath.Abs(parts[0])
	if err != nil {
		return err
	}
	source, err = filepath.EvalSymlinks(source)
	if err != nil {
		return err
	}
	fi, err := os.Stat(source)
	if err != nil {
		return err
	}
	*f = append(*f, volume{goferMount: goferMount{source: source, readonly: readonly}, target: filepath.Clean(parts[1]), isDir: fi.IsDir()})
	return nil
}

var (
	envv       envFlag
	volumes    volumesFlag
	image      = flag.String("image", "", "EROFS image of the root filesystem")
	debug      = flag.Bool("debug", false, "log at debug level")
	straceFlag = flag.Bool("strace", false, "log system calls (implies -debug)")
	fdLimit    = flag.Int("fdlimit", -1, "limit on the number of host FDs of the gofer, or -1 for no limit")
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "gofer" {
		os.Exit(goferMain(os.Args[2:]))
	}
	flag.Var(&envv, "e", "set KEY=VALUE in the environment of the program (repeatable)")
	flag.Var(&volumes, "v", "mount host:guest[:ro] (repeatable)")
	flag.Parse()
	if *image == "" || flag.NArg() == 0 {
		fmt.Fprintf(os.Stderr, "usage: hvfboot -image <root.erofs> [-v host:guest[:ro]]... <program> [args...]\n")
		fmt.Fprintf(os.Stderr, "       hvfboot gofer -io-fds <fd,...> -mount <ro|rw>:<path>...\n")
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
	vfsObj.MustRegisterFilesystemType(gofer.Name, &gofer.FilesystemType{}, &vfs.RegisterFilesystemTypeOptions{})
	if err := memdev.Register(vfsObj); err != nil {
		return 0, fmt.Errorf("registering memory devices: %w", err)
	}

	hostFS, err := host.NewFilesystem(vfsObj)
	if err != nil {
		return 0, fmt.Errorf("creating the host filesystem: %w", err)
	}
	defer hostFS.DecRef(ctx)
	k.SetHostMount(vfsObj.NewDisconnectedMount(hostFS, nil, &vfs.MountOptions{}))

	// Open all host resources, then confine the Sentry before it parses
	// untrusted data (the image and the program).
	imageFD, err := unix.Open(imagePath, unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return 0, fmt.Errorf("opening %s: %w", imagePath, err)
	}
	var socks []*os.File
	if len(volumes) > 0 {
		var mounts []goferMount
		for _, v := range volumes {
			mounts = append(mounts, v.goferMount)
		}
		// The gofer exits when its connections close, at the latest when
		// the Sentry exits. hvfboot does not wait for it.
		_, s, err := startGofer(mounts, *fdLimit, *debug)
		if err != nil {
			return 0, err
		}
		socks = s
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
	var sources []string
	for _, v := range volumes {
		sources = append(sources, v.source)
	}
	if err := confineSentry(sources); err != nil {
		return 0, err
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
	for i, v := range volumes {
		err := mountVolume(ctx, vfsObj, creds, mntns, v, socks[i])
		socks[i].Close()
		if err != nil {
			return 0, err
		}
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
	return exitCode(tg.ExitStatus()), nil
}

// exitCode returns the exit code of a process with status ws, as a shell
// reports it: the exit status, or 128 plus the number of the signal that
// killed the process.
func exitCode(ws linux.WaitStatus) int {
	if ws.Signaled() {
		return 128 + int(ws.TerminationSignal())
	}
	return int(ws.ExitStatus())
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

// mountVolume mounts v at its target in mntns, with sock as the connection to
// the gofer. It makes the target directory if it does not exist and its parent
// is writable.
func mountVolume(ctx context.Context, vfsObj *vfs.VirtualFilesystem, creds *auth.Credentials, mntns *vfs.MountNamespace, v volume, sock *os.File) error {
	root := mntns.Root(ctx)
	defer root.DecRef(ctx)
	pop := &vfs.PathOperation{Root: root, Start: root, Path: fspath.Parse(v.target)}
	if v.isDir {
		err := vfsObj.MkdirAt(ctx, creds, pop, &vfs.MkdirOptions{Mode: 0755})
		if err != nil && !linuxerr.Equals(linuxerr.EEXIST, err) {
			return fmt.Errorf("making mount point %s: %w", v.target, err)
		}
	}
	// The gofer client owns the FD in the mount options and closes it.
	fd, err := unix.Dup(int(sock.Fd()))
	if err != nil {
		return err
	}
	unix.CloseOnExec(fd)
	data := []string{
		"trans=fd",
		"rfdno=" + strconv.Itoa(fd),
		"wfdno=" + strconv.Itoa(fd),
		// The host can change the files at any time.
		"cache=remote_revalidating",
		"disable_fifo_open",
	}
	opts := &vfs.MountOptions{
		ReadOnly: v.readonly,
		GetFilesystemOptions: vfs.GetFilesystemOptions{
			InternalMount: true,
			Data:          strings.Join(data, ","),
		},
	}
	if _, err := vfsObj.MountAt(ctx, creds, gofer.Name, pop, gofer.Name, opts); err != nil {
		unix.Close(fd)
		return fmt.Errorf("mounting %s at %s: %w", v.source, v.target, err)
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
