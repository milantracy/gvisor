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
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
	"gvisor.dev/gvisor/pkg/lisafs"
	"gvisor.dev/gvisor/pkg/log"
	"gvisor.dev/gvisor/pkg/unet"
	"gvisor.dev/gvisor/runsc/config"
	fsgofer "gvisor.dev/gvisor/runsc/gofer/fs"
)

// The gofer follows `runsc gofer`: one process serves all host mounts of the
// sandbox over lisafs, with one socket pair per mount. The gofer gets its
// socket ends as --io-fds, in mount order, and -mount gives the host path of
// each mount in the same order.

// goferMount is a host directory or file that the gofer serves.
type goferMount struct {
	// source is the real path of the host file: it has no symlinks, because
	// the darwin gofer server opens files again by path with O_NOFOLLOW_ANY.
	source   string
	readonly bool
}

// String returns the -mount flag value of m.
func (m goferMount) String() string {
	if m.readonly {
		return "ro:" + m.source
	}
	return "rw:" + m.source
}

// parseGoferMount parses a -mount flag value: "ro:" or "rw:", then an
// absolute path.
func parseGoferMount(v string) (goferMount, error) {
	mode, source, ok := strings.Cut(v, ":")
	if !ok || (mode != "ro" && mode != "rw") || !filepath.IsAbs(source) {
		return goferMount{}, fmt.Errorf("%q is not ro:<path> or rw:<path> with an absolute path", v)
	}
	return goferMount{source: source, readonly: mode == "ro"}, nil
}

// mountsFlag is a repeatable -mount flag.
type mountsFlag []goferMount

func (f *mountsFlag) String() string {
	var s []string
	for _, m := range *f {
		s = append(s, m.String())
	}
	return strings.Join(s, ",")
}

func (f *mountsFlag) Set(v string) error {
	m, err := parseGoferMount(v)
	if err != nil {
		return err
	}
	*f = append(*f, m)
	return nil
}

// fdsFlag is a comma-separated list of FDs, as --io-fds of `runsc gofer`.
type fdsFlag []int

func (f *fdsFlag) String() string {
	var s []string
	for _, fd := range *f {
		s = append(s, strconv.Itoa(fd))
	}
	return strings.Join(s, ",")
}

func (f *fdsFlag) Set(v string) error {
	for _, s := range strings.Split(v, ",") {
		fd, err := strconv.Atoi(s)
		if err != nil {
			return fmt.Errorf("%q is not an FD: %w", s, err)
		}
		*f = append(*f, fd)
	}
	return nil
}

// goferMain is `hvfboot gofer`. It serves the host mounts until the Sentry
// closes all connections.
func goferMain(args []string) int {
	fs := flag.NewFlagSet("gofer", flag.ExitOnError)
	var ioFDs fdsFlag
	var mounts mountsFlag
	fs.Var(&ioFDs, "io-fds", "FDs to serve lisafs connections on, in mount order")
	fs.Var(&mounts, "mount", "ro:<path> or rw:<path>, the host file to serve on the FD of the same index (repeatable)")
	fdLimit := fs.Int("fdlimit", -1, "limit on the number of host FDs, or -1 for no limit")
	debug := fs.Bool("debug", false, "log at debug level")
	fs.Parse(args)
	if *debug {
		log.SetLevel(log.Debug)
	}
	if len(ioFDs) != len(mounts) {
		fmt.Fprintf(os.Stderr, "hvfboot gofer: %d FDs for %d mounts\n", len(ioFDs), len(mounts))
		return 2
	}
	if err := setFDLimit(*fdLimit); err != nil {
		fmt.Fprintf(os.Stderr, "hvfboot gofer: %v\n", err)
		return 1
	}

	// The Sentry runs the sandbox as root; the gofer runs as the user.
	fsgofer.MapRootToEffectiveIDs()
	conf := &fsgofer.Config{
		HostUDS:      config.HostUDSNone,
		HostFifo:     config.HostFifoNone,
		InodeNumbers: config.InodeNumbersPassthrough,
		RUID:         unix.Getuid(),
		EUID:         unix.Geteuid(),
		RGID:         unix.Getgid(),
		EGID:         unix.Getegid(),
	}
	server := lisafs.NewServer()
	for i, m := range mounts {
		sock, err := unet.NewSocket(ioFDs[i])
		if err != nil {
			fmt.Fprintf(os.Stderr, "hvfboot gofer: FD %d: %v\n", ioFDs[i], err)
			return 1
		}
		conn, err := server.CreateConnection(sock, m.source, fsgofer.ConnectionOpts(m.readonly), fsgofer.NewConnectionImpl(conf))
		if err != nil {
			fmt.Fprintf(os.Stderr, "hvfboot gofer: serving %s on FD %d: %v\n", m.source, ioFDs[i], err)
			return 1
		}
		log.Infof("Serving %q on FD %d (ro: %t)", m.source, ioFDs[i], m.readonly)
		server.StartConnection(conn)
	}
	server.Wait()
	server.Destroy()
	return 0
}

// setFDLimit sets RLIMIT_NOFILE to limit, unless limit is negative.
func setFDLimit(limit int) error {
	if limit < 0 {
		return nil
	}
	rlimit := unix.Rlimit{Cur: uint64(limit), Max: uint64(limit)}
	if err := unix.Setrlimit(unix.RLIMIT_NOFILE, &rlimit); err != nil {
		return fmt.Errorf("setting RLIMIT_NOFILE to %d: %w", limit, err)
	}
	return nil
}

// startGofer starts `hvfboot gofer` for mounts, and returns the Sentry ends
// of the socket pairs, in mount order. The gofer exits when the Sentry closes
// them.
func startGofer(mounts []goferMount, fdLimit int, debug bool) (*exec.Cmd, []*os.File, error) {
	self, err := os.Executable()
	if err != nil {
		return nil, nil, err
	}
	cmd := exec.Command(self, "gofer")
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	var sentryEnds, goferEnds []*os.File
	defer func() {
		// The gofer has its own copies after Start.
		for _, f := range goferEnds {
			f.Close()
		}
	}()
	var ioFDs fdsFlag
	for _, m := range mounts {
		fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
		if err != nil {
			return nil, nil, err
		}
		unix.CloseOnExec(fds[0])
		unix.CloseOnExec(fds[1])
		sentryEnds = append(sentryEnds, os.NewFile(uintptr(fds[0]), "sandbox IO FD"))
		goferEnds = append(goferEnds, os.NewFile(uintptr(fds[1]), "gofer IO FD"))
		// ExtraFiles[i] is FD 3+i in the gofer.
		ioFDs = append(ioFDs, 3+len(cmd.ExtraFiles))
		cmd.ExtraFiles = append(cmd.ExtraFiles, goferEnds[len(goferEnds)-1])
		cmd.Args = append(cmd.Args, "-mount", m.String())
	}
	cmd.Args = append(cmd.Args, "-io-fds", ioFDs.String(), "-fdlimit", strconv.Itoa(fdLimit))
	if debug {
		cmd.Args = append(cmd.Args, "-debug")
	}
	if err := cmd.Start(); err != nil {
		for _, f := range sentryEnds {
			f.Close()
		}
		return nil, nil, fmt.Errorf("starting the gofer: %w", err)
	}
	return cmd, sentryEnds, nil
}
