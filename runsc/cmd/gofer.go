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

package cmd

import (
	"fmt"
	"path/filepath"

	"github.com/google/subcommands"
	specs "github.com/opencontainers/runtime-spec/specs-go"
	"gvisor.dev/gvisor/pkg/lisafs"
	"gvisor.dev/gvisor/pkg/log"
	"gvisor.dev/gvisor/pkg/unet"
	"gvisor.dev/gvisor/runsc/boot/sentryapi"
	"gvisor.dev/gvisor/runsc/cmd/sandboxsetup"
	"gvisor.dev/gvisor/runsc/cmd/util"
	"gvisor.dev/gvisor/runsc/config"
	"gvisor.dev/gvisor/runsc/flag"
	fsgofer "gvisor.dev/gvisor/runsc/gofer/fs"
	"gvisor.dev/gvisor/runsc/gofer/fs/extension"
	"gvisor.dev/gvisor/runsc/profile"
	"gvisor.dev/gvisor/runsc/specutils"
)

var caps = []string{
	"CAP_CHOWN",
	"CAP_DAC_OVERRIDE",
	"CAP_DAC_READ_SEARCH",
	"CAP_FOWNER",
	"CAP_FSETID",
	"CAP_SYS_CHROOT",
}

var udsOpenCaps = []string{
	"CAP_SETUID",
	"CAP_SETGID",
}

// goferCaps is the minimal set of capabilities needed by the Gofer to operate
// on files.
var goferCaps = &specs.LinuxCapabilities{
	Bounding:    caps,
	Effective:   caps,
	Permitted:   caps,
	Inheritable: caps,
	Ambient:     caps,
}

var goferUdsOpenCaps = &specs.LinuxCapabilities{
	Bounding:  udsOpenCaps,
	Effective: udsOpenCaps,
	Permitted: udsOpenCaps,
}

// goferSyncFDs contains file descriptors that are used for synchronization
// of the Gofer startup process against other processes.
type goferSyncFDs struct {
	// chrootFD is a file descriptor that is used to wait until container
	// filesystem related setup is done. This setup involves creating files and
	// mounts in the Gofer process's mount namespace and needs to be done before
	// the Gofer chroots.
	// If this is set, this FD is the first that the Gofer waits for.
	chrootFD int

	// usernsFD is a file descriptor that is used to wait until
	// user namespace ID mappings are established in the Gofer's userns.
	// If this is set, this FD is the second that the Gofer waits for.
	usernsFD int

	// procMountFD is a file descriptor that has to be closed when the
	// procfs mount isn't needed anymore. It is read by the procfs unmounter
	// process.
	// It is only set when re-execing is necessary.
	// If this is set, this FD is the last that the Gofer interacts with and
	// closes.
	procMountFD int
}

// Gofer implements subcommands.Command for the "gofer" command, which starts a
// filesystem gofer.  This command should not be called directly.
type Gofer struct {
	util.InternalSubCommand
	bundleDir  string
	ioFDs      sandboxsetup.IntFlags
	devIoFD    int
	applyCaps  bool
	setUpRoot  bool
	mountConfs specutils.GoferMountConfFlags

	// uid and gid are the user and group IDs to switch to after setting up the
	// user namespace.
	uid int
	gid int

	specFD           int
	mountsFD         int
	goferToHostRPCFD int
	profileFDs       profile.FDArgs
	syncFDs          goferSyncFDs
	stopProfiling    func()
}

// Name implements subcommands.Command.
func (*Gofer) Name() string {
	return "gofer"
}

// Synopsis implements subcommands.Command.
func (g *Gofer) Synopsis() string {
	return "launch a gofer process that proxies access to container files"
}

// Usage implements subcommands.Command.
func (*Gofer) Usage() string {
	return "gofer [flags] <container ID>\n"
}

// SetFlags implements subcommands.Command.
func (g *Gofer) SetFlags(f *flag.FlagSet) {
	f.StringVar(&g.bundleDir, "bundle", "", "path to the root of the bundle directory, defaults to the current directory")
	f.BoolVar(&g.applyCaps, "apply-caps", true, "if true, apply capabilities to restrict what the Gofer process can do")
	f.BoolVar(&g.setUpRoot, "setup-root", true, "if true, set up an empty root for the process")

	// Open FDs that are donated to the gofer.
	f.Var(&g.ioFDs, "io-fds", "list of FDs to connect gofer servers. Follows the same order as --gofer-mount-confs. FDs are only donated if the mount is backed by lisafs.")
	f.Var(&g.mountConfs, "gofer-mount-confs", "information about how the gofer mounts have been configured. They must follow this order: root first, then mounts as defined in the spec.")
	f.IntVar(&g.devIoFD, "dev-io-fd", -1, "optional FD to connect /dev gofer server")
	f.IntVar(&g.specFD, "spec-fd", -1, "required fd with the container spec")
	f.IntVar(&g.mountsFD, "mounts-fd", -1, "mountsFD is the file descriptor to write list of mounts after they have been resolved (direct paths, no symlinks).")
	f.IntVar(&g.goferToHostRPCFD, "rpc-fd", -1, "gofer-to-host RPC file descriptor.")

	// IDs to run gofer as.
	f.IntVar(&g.uid, "uid", 0, "User ID")
	f.IntVar(&g.gid, "gid", 0, "Group ID")

	// Add synchronization FD flags.
	g.syncFDs.setFlags(f)

	// Profiling flags.
	g.profileFDs.SetFromFlags(f)

	extension.SetFlags(f)
}

// serve serves the root filesystem of spec at rootPath, if it is a lisafs
// mount, and each lisafs mount m of spec at mountPath(m). root is the host
// path of the root filesystem.
func (g *Gofer) serve(spec *specs.Spec, conf *config.Config, root, rootPath string, mountPath func(m *specs.Mount) string, ruid int, euid int, rgid int, egid int, groups []int, canSetGroups bool) subcommands.ExitStatus {
	type connectionConfig struct {
		sock      *unet.Socket
		mountPath string
		readonly  bool
		mount     *specs.Mount
		// devGofer is true only for the dev gofer connection, which serves
		// host device files to the sentry's device proxies (e.g. nvproxy). A
		// container mount whose destination happens to be "/dev" is not a dev
		// gofer connection.
		devGofer bool
	}
	cfgs := make([]connectionConfig, 0, len(spec.Mounts)+1)

	ioFDs := g.ioFDs
	rootfsConf := g.mountConfs[0]
	if rootfsConf.ShouldUseLisafs() {
		// Start with root mount, then add any other additional mount as needed.
		cfgs = append(cfgs, connectionConfig{
			sock:      sandboxsetup.NewSocket(ioFDs[0]),
			mountPath: rootPath,
			readonly:  spec.Root.Readonly || rootfsConf.ShouldUseOverlayfs(),
		})
		log.Infof("Serving %q mapped to %q on FD %d (ro: %t)", rootPath, root, ioFDs[0], cfgs[0].readonly)
		ioFDs = ioFDs[1:]
	}

	mountIdx := 1 // first one is the root
	for i := range spec.Mounts {
		m := &spec.Mounts[i]
		if !specutils.HasMountConfig(*m) {
			continue
		}
		mountConf := g.mountConfs[mountIdx]
		mountIdx++
		if !mountConf.ShouldUseLisafs() {
			continue
		}
		if !filepath.IsAbs(m.Destination) {
			util.Fatalf("mount destination must be absolute: %q", m.Destination)
		}

		if len(ioFDs) == 0 {
			util.Fatalf("no FD found for mount. Did you forget --io-fd? FDs: %d, Mount: %+v", len(g.ioFDs), m)
		}
		ioFD := ioFDs[0]
		ioFDs = ioFDs[1:]
		readonly := specutils.IsReadonlyMount(m.Options) || mountConf.ShouldUseOverlayfs()
		cfgs = append(cfgs, connectionConfig{
			sock:      sandboxsetup.NewSocket(ioFD),
			mountPath: mountPath(m),
			readonly:  readonly,
			mount:     m,
		})
		log.Infof("Serving %q mapped on FD %d (ro: %t)", mountPath(m), ioFD, readonly)
	}

	if len(ioFDs) > 0 {
		util.Fatalf("too many FDs passed for mounts. mounts: %d, FDs: %d", len(cfgs), len(g.ioFDs))
	}

	if g.devIoFD >= 0 {
		cfgs = append(cfgs, connectionConfig{
			sock:      sandboxsetup.NewSocket(g.devIoFD),
			mountPath: "/dev",
			devGofer:  true,
		})
		log.Infof("Serving /dev mapped on FD %d (ro: false)", g.devIoFD)
	}

	// These are global fsgofer configurations.
	fsgoferConf := &fsgofer.Config{
		HostUDS:            conf.GetHostUDS(),
		HostFifo:           conf.HostFifo,
		CharDevicePolicy:   conf.CharacterDevicePolicy,
		InodeNumbers:       conf.InodeNumbers,
		DonateMountPointFD: conf.DirectFS,
		RUID:               ruid,
		EUID:               euid,
		RGID:               rgid,
		EGID:               egid,
		Groups:             groups,
		CanSetGroups:       canSetGroups,
	}
	// The dev gofer connection exists to open host device files on behalf of
	// the sentry's device proxies (e.g. nvproxy), which mediate all
	// application I/O on the resulting FDs. --character-device-policy governs
	// devices that the sandboxed application could open directly through a
	// gofer mount, not this connection, so character device opens are always
	// allowed here.
	devGoferConf := *fsgoferConf
	devGoferConf.CharDevicePolicy = config.CharDevPassthrough

	// Create the server and start connections.
	server := lisafs.NewServer()
	for _, cfg := range cfgs {
		var connImpl lisafs.ConnectionImpl
		var connOpts lisafs.ConnectionOpts
		// /dev is always served by the stock fsgofer.
		if cfg.mountPath != "/dev" {
			for _, e := range extension.Registered() {
				impl, opts, err := e.TryHandleMount(spec, cfg.mount, cfg.mountPath, cfg.readonly)
				if err != nil {
					util.Fatalf("extension %s for %q: %v", e.Name(), cfg.mountPath, err)
				}
				if impl != nil {
					connImpl = impl
					connOpts = opts
					log.Infof("Serving %q via extension %s on FD %d", cfg.mountPath, e.Name(), cfg.sock.FD())
					break
				}
			}
		}
		if connImpl == nil {
			connConf := fsgoferConf
			if cfg.devGofer {
				connConf = &devGoferConf
			}
			connImpl = fsgofer.NewConnectionImpl(connConf)
			connOpts = fsgofer.ConnectionOpts(cfg.readonly)
		}
		conn, err := server.CreateConnection(cfg.sock, cfg.mountPath, connOpts, connImpl)
		if err != nil {
			util.Fatalf("starting connection on FD %d for gofer mount failed: %v", cfg.sock.FD(), err)
		}
		server.StartConnection(conn)
	}
	server.Wait()
	server.Destroy()
	log.Infof("All lisafs servers exited.")
	if g.stopProfiling != nil {
		g.stopProfiling()
	}
	return subcommands.ExitSuccess
}

// lisafsNeededForDirectFSSuppression returns true if this gofer serves a mount
// that suppresses directfs and therefore still needs LisaFS syscalls.
func lisafsNeededForDirectFSSuppression(spec *specs.Spec, mountHints *sentryapi.PodMountHints, rootfsHint *sentryapi.RootfsHint, mountConfs []specutils.GoferMountConf) bool {
	if len(mountConfs) > 0 && mountConfs[0].ShouldUseLisafs() &&
		rootfsHint != nil && rootfsHint.SuppressDirectFS {
		return true
	}
	if mountHints == nil {
		return false
	}
	mountIdx := 1 // First mount config is the root.
	for _, m := range spec.Mounts {
		if !specutils.HasMountConfig(m) {
			continue
		}
		mountConf := mountConfs[mountIdx]
		mountIdx++
		if !mountConf.ShouldUseLisafs() {
			continue
		}
		if hint := mountHints.FindMount(m.Source); hint != nil && hint.SuppressDirectFS {
			return true
		}
	}
	return false
}

// setFlags sets sync FD flags on the given FlagSet.
func (g *goferSyncFDs) setFlags(f *flag.FlagSet) {
	f.IntVar(&g.chrootFD, "sync-chroot-fd", -1, "file descriptor that the gofer waits on until container filesystem setup is done")
	f.IntVar(&g.usernsFD, "sync-userns-fd", -1, "file descriptor the gofer waits on until userns mappings are set up")
	f.IntVar(&g.procMountFD, "proc-mount-sync-fd", -1, "file descriptor that the gofer writes to when /proc isn't needed anymore and can be unmounted")
}

// flags returns the flags necessary to pass along the current sync FD values
// to a re-executed version of this process.
func (g *goferSyncFDs) flags() map[string]string {
	return map[string]string{
		"sync-chroot-fd":     fmt.Sprintf("%d", g.chrootFD),
		"sync-userns-fd":     fmt.Sprintf("%d", g.usernsFD),
		"proc-mount-sync-fd": fmt.Sprintf("%d", g.procMountFD),
	}
}
