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

package cmd

import (
	"context"
	"os"
	"path/filepath"
	"runtime/debug"

	"github.com/google/subcommands"
	specs "github.com/opencontainers/runtime-spec/specs-go"
	"golang.org/x/sys/unix"
	"gvisor.dev/gvisor/pkg/log"
	"gvisor.dev/gvisor/runsc/cmd/sandboxsetup"
	"gvisor.dev/gvisor/runsc/cmd/util"
	"gvisor.dev/gvisor/runsc/config"
	"gvisor.dev/gvisor/runsc/flag"
	fsgofer "gvisor.dev/gvisor/runsc/gofer/fs"
	"gvisor.dev/gvisor/runsc/gofer/fs/filter"
	"gvisor.dev/gvisor/runsc/profile"
	"gvisor.dev/gvisor/runsc/specutils"
)

// Execute implements subcommands.Command.
//
// darwin has no mount namespaces, chroot(2) without root, capabilities or
// seccomp. So the gofer serves the host paths of the root filesystem and the
// mounts, and a Seatbelt profile (see runsc/gofer/fs/filter) limits it to
// those paths. --apply-caps and --setup-root are ignored.
func (g *Gofer) Execute(_ context.Context, f *flag.FlagSet, args ...any) subcommands.ExitStatus {
	if g.bundleDir == "" || len(g.ioFDs) < 1 || g.specFD < 0 {
		f.Usage()
		return subcommands.ExitUsageError
	}
	if f.NArg() != 1 {
		f.Usage()
		return subcommands.ExitUsageError
	}
	if g.devIoFD >= 0 || g.goferToHostRPCFD >= 0 || g.syncFDs.chrootFD >= 0 || g.syncFDs.usernsFD >= 0 || g.syncFDs.procMountFD >= 0 {
		util.Fatalf("--dev-io-fd, --rpc-fd and the sync FDs are not supported on darwin")
	}

	conf := args[0].(*config.Config)
	debug.SetTraceback(conf.Traceback)

	specFile := os.NewFile(uintptr(g.specFD), "spec file")
	spec, err := specutils.ReadSpecFromFile(g.bundleDir, specFile, specutils.SpecOpts{Conf: conf})
	if err != nil {
		util.Fatalf("reading spec: %v", err)
	}
	if err := specFile.Close(); err != nil {
		util.Fatalf("closing specFile: %v", err)
	}
	specutils.LogSpecDebug(spec, conf.OCISeccomp)

	// The darwin lisafs server opens files again by path, and allows no
	// symlinks in it. Seatbelt matches real paths too. So the gofer serves
	// real paths.
	var mounts []filter.Mount
	root := spec.Root.Path
	if g.mountConfs[0].ShouldUseLisafs() {
		if root, err = filepath.EvalSymlinks(root); err != nil {
			util.Fatalf("resolving the root filesystem: %v", err)
		}
		mounts = append(mounts, filter.Mount{Path: root, ReadOnly: spec.Root.Readonly || g.mountConfs[0].ShouldUseOverlayfs()})
	}
	sources := make(map[string]string)
	mountIdx := 1 // The first one is the root.
	for _, m := range spec.Mounts {
		if !specutils.HasMountConfig(m) {
			continue
		}
		mountConf := g.mountConfs[mountIdx]
		mountIdx++
		if !mountConf.ShouldUseLisafs() {
			continue
		}
		real, err := filepath.EvalSymlinks(m.Source)
		if err != nil {
			util.Fatalf("resolving mount source %q: %v", m.Source, err)
		}
		sources[m.Source] = real
		mounts = append(mounts, filter.Mount{Path: real, ReadOnly: specutils.IsReadonlyMount(m.Options) || mountConf.ShouldUseOverlayfs()})
	}
	if g.mountsFD >= 0 {
		// Mount destinations are not mounted on the host; the Sentry
		// resolves them in its own filesystem.
		go func() {
			if err := sandboxsetup.WriteMounts(g.mountsFD, spec.Mounts); err != nil {
				log.Warningf("Failed to write mounts: %v", err)
			}
		}()
	}

	profileOpts := profile.MakeOpts(&g.profileFDs, conf.ProfileGCInterval)
	g.stopProfiling = profile.Start(profileOpts)

	// fsgofer should run with a umask of 0, because we want to preserve file
	// modes exactly as sent by the sandbox, which will have applied its own umask.
	unix.Umask(0)

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
	}

	// The Sentry runs the sandbox as root; the gofer runs as the user, as
	// the user namespace of rootless runsc does.
	fsgofer.MapRootToEffectiveIDs()
	ruid := unix.Getuid()
	euid := unix.Geteuid()
	rgid := unix.Getgid()
	egid := unix.Getegid()
	groups, err := unix.Getgroups()
	if err != nil {
		util.Fatalf("reading supplementary groups: %v", err)
	}

	tmpDir, err := filter.Install(filter.Options{
		UDSOpenEnabled:   conf.GetHostUDS().AllowOpen(),
		UDSCreateEnabled: conf.GetHostUDS().AllowCreate(),
		ProfileEnabled:   profileOpts.Enabled(),
		DirectFS:         conf.DirectFS,
		Mounts:           mounts,
	})
	if err != nil {
		util.Fatalf("confining the gofer: %v", err)
	}
	defer os.RemoveAll(tmpDir)
	log.Infof("Gofer confined to %v", mounts)

	mountPath := func(m *specs.Mount) string { return sources[m.Source] }
	return g.serve(spec, conf, root, root, mountPath, ruid, euid, rgid, egid, groups, false)
}
