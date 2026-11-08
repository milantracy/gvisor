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

// Binary hvfboot runs a program in a gVisor sandbox on the hvf platform, with
// an EROFS image as the root filesystem and the standard I/O of hvfboot.
//
//	hvfboot -image root.erofs /bin/busybox echo hello
//
// The image must have blocks of at least 16 KiB and no inline file data
// (mkfs.erofs -b 16384 -E noinline_data); make_image.sh makes one from a
// container image.
//
// The Sentry runs as `runsc boot` does: hvfboot makes an OCI spec with the
// default capabilities and mounts of Docker, and gives it to boot.Loader. The
// root filesystem has an overlay in application memory, as with
// --overlay2=root:memory.
//
// -v host:guest[:ro] bind-mounts a host file or directory at guest. As in
// runsc, one gofer process (`hvfboot gofer`) serves all host mounts over
// lisafs, with one socket pair per mount:
//
//	hvfboot -image root.erofs -v $HOME/src:/mnt/src:ro /bin/ls /mnt/src
//
// -network=sandbox (the default) gives the sandbox netstack with outbound
// TCP and UDP through a network proxy process (`hvfboot net`), as runsc does
// with an external network proxy; -network=none gives loopback only.
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
	"strings"

	"gvisor.dev/gvisor/pkg/abi/linux"
	"gvisor.dev/gvisor/pkg/cpuid"
	hvfapi "gvisor.dev/gvisor/pkg/hvf"
	"gvisor.dev/gvisor/pkg/hvf/entitle"
	"gvisor.dev/gvisor/pkg/log"
	"gvisor.dev/gvisor/pkg/sentry/platform/hvf"
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
	network    = flag.String("network", "sandbox", "sandbox: netstack with a network proxy process (hvfboot net); none: loopback only")
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "gofer":
			os.Exit(goferMain(os.Args[2:]))
		case "net":
			os.Exit(netMain(os.Args[2:]))
		}
	}
	flag.Var(&envv, "e", "set KEY=VALUE in the environment of the program (repeatable)")
	flag.Var(&volumes, "v", "mount host:guest[:ro] (repeatable)")
	flag.Parse()
	if *image == "" || flag.NArg() == 0 {
		fmt.Fprintf(os.Stderr, "usage: hvfboot -image <root.erofs> [-v host:guest[:ro]]... <program> [args...]\n")
		fmt.Fprintf(os.Stderr, "       hvfboot gofer -io-fds <fd,...> -mount <ro|rw>:<path>...\n")
		fmt.Fprintf(os.Stderr, "       hvfboot net -link-fd <fd> -lifeline-fd <fd>\n")
		os.Exit(2)
	}
	cpuid.Initialize()
	// Make the platform here to find out if the binary has the hypervisor
	// entitlement. boot.New gets it again from hvf.New, which makes one
	// platform per process.
	_, err := hvf.New()
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
	code, err := runSandbox(*image, flag.Args())
	if err != nil {
		fmt.Fprintf(os.Stderr, "hvfboot: %v\n", err)
		os.Exit(1)
	}
	os.Exit(code)
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
