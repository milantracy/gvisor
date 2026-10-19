// Copyright 2026 The gVisor Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package sandbox

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	specs "github.com/opencontainers/runtime-spec/specs-go"
)

// baseEnv is prepended to the process environment unless SkipBaseEnv is set.
var baseEnv = []string{"PATH=/bin:/usr/bin:/usr/local/bin"}

var hostBinaryDirs = []string{"/bin", "/usr", "/lib", "/lib64", "/etc/alternatives"}

const defaultRootfsDir = "rootfs"

// bundleConfig holds configuration for creating an OCI bundle. The zero value
// of every field reproduces the default sandbox layout.
type bundleConfig struct {
	ID         string
	RuntimeDir string
	Network    NetworkMode
	Mounts     []Mount
	Env        []string
	WorkingDir string
	Hostname   string

	// RootPath is the container's root filesystem.
	RootPath string

	// RootReadonly mounts the container's root filesystem read-only.
	RootReadonly bool

	// User is the user the init process runs as. Defaults to 0:0.
	User *specs.User

	// Capabilities is the capability set of the init process. When nil, the
	// runtime applies its own default.
	Capabilities *specs.LinuxCapabilities

	// Namespaces are appended to the namespace set.
	Namespaces []specs.LinuxNamespace

	// UIDMappings and GIDMappings replace the default ID mappings when either
	// is non-nil.
	UIDMappings []specs.LinuxIDMapping
	GIDMappings []specs.LinuxIDMapping

	// SkipDefaultNamespaces omits the automatic namespaces and the ID
	// mappings that come with them.
	SkipDefaultNamespaces bool

	// SkipLinuxSystemMounts omits the automatic /proc and /dev mounts.
	SkipLinuxSystemMounts bool

	// SkipHostBinaryMounts omits the read-only binds of the host's binary and
	// library directories.
	SkipHostBinaryMounts bool

	// SkipBaseEnv omits the default PATH entry otherwise prepended to Env.
	SkipBaseEnv bool
}

// ociType returns the OCI filesystem type name for t.
func (t MountType) ociType() (string, error) {
	switch t {
	case MountTypeBind:
		return "bind", nil
	case MountTypeTmpfs:
		return "tmpfs", nil
	case MountTypeProc:
		return "proc", nil
	case MountTypeSysfs:
		return "sysfs", nil
	case MountTypeDevtmpfs:
		return "devtmpfs", nil
	case MountTypeDevpts:
		return "devpts", nil
	case MountTypeCgroup:
		return "cgroupfs", nil
	}
	return "", fmt.Errorf("unknown mount type %d", t)
}

// ociMount converts m into an OCI mount entry.
func (m Mount) ociMount() (specs.Mount, error) {
	fsType, err := m.Type.ociType()
	if err != nil {
		return specs.Mount{}, err
	}
	source := m.Source
	if source != "" {
		source = filepath.Clean(source)
	}
	var opts []string
	for _, o := range []struct {
		set  bool
		name string
	}{
		{m.Recursive, "rbind"},
		{m.Private, "rprivate"},
		{m.NoSuid, "nosuid"},
		{m.NoDev, "nodev"},
		{m.ReadOnly, "ro"},
	} {
		if o.set {
			opts = append(opts, o.name)
		}
	}
	if m.Mode != nil {
		opts = append(opts, fmt.Sprintf("mode=%04o", *m.Mode))
	}

	return specs.Mount{
		Destination: filepath.Clean(m.Destination),
		Source:      source,
		Type:        fsType,
		Options:     opts,
	}, nil
}

// sharedTmpfsSource returns the mount source of the i-th entry of
// bundleConfig.Mounts when it is a tmpfs. The sandbox spec names this source
// in a mount hint, so that every container shares one tmpfs instance.
func sharedTmpfsSource(i int) string {
	return fmt.Sprintf("/sandbox-tmpfs-%d", i)
}

// newSpec builds the OCI runtime specification of the containers that Exec
// starts, without the process arguments. newBundle adjusts it before use.
func newSpec(cfg bundleConfig) (*specs.Spec, error) {
	// Without an explicit root, the container gets an empty root filesystem.
	rootPath := cfg.RootPath
	if rootPath == "" {
		rootPath = defaultRootfsDir
	}

	var namespaces []specs.LinuxNamespace
	if !cfg.SkipDefaultNamespaces {
		namespaces = []specs.LinuxNamespace{
			{Type: specs.PIDNamespace},
			{Type: specs.MountNamespace},
			{Type: specs.UTSNamespace},
			{Type: specs.IPCNamespace},
		}
		if os.Geteuid() != 0 {
			namespaces = append(namespaces, specs.LinuxNamespace{Type: specs.UserNamespace})
		}
		if cfg.Network == NetworkModeSandbox {
			namespaces = append(namespaces, specs.LinuxNamespace{Type: specs.NetworkNamespace})
		}
	}
	namespaces = append(namespaces, cfg.Namespaces...)

	user := specs.User{UID: 0, GID: 0}
	if cfg.User != nil {
		user = *cfg.User
	}

	spec := &specs.Spec{
		Version: "1.0.0",
		Root: &specs.Root{
			Path:     rootPath,
			Readonly: cfg.RootReadonly,
		},
		Process: &specs.Process{
			User:         user,
			Cwd:          cfg.WorkingDir,
			Capabilities: cfg.Capabilities,
		},
		// enable basic namespaces for gVisor.
		Linux: &specs.Linux{
			Namespaces: namespaces,
		},
		Hostname: cfg.Hostname,
	}

	if cfg.SkipBaseEnv {
		spec.Process.Env = cfg.Env
	} else {
		spec.Process.Env = slices.Concat(baseEnv, cfg.Env)
	}

	switch {
	case cfg.UIDMappings != nil || cfg.GIDMappings != nil:
		spec.Linux.UIDMappings = cfg.UIDMappings
		spec.Linux.GIDMappings = cfg.GIDMappings
	case os.Geteuid() != 0 && !cfg.SkipDefaultNamespaces:
		spec.Linux.UIDMappings = []specs.LinuxIDMapping{
			{ContainerID: 0, HostID: uint32(os.Geteuid()), Size: 1},
		}
		spec.Linux.GIDMappings = []specs.LinuxIDMapping{
			{ContainerID: 0, HostID: uint32(os.Getegid()), Size: 1},
		}
	}

	if !cfg.SkipLinuxSystemMounts {
		spec.Mounts = append(spec.Mounts,
			specs.Mount{Destination: "/proc", Type: "proc", Source: "proc"},
			specs.Mount{Destination: "/dev", Type: "tmpfs", Source: "tmpfs"},
		)
	}

	// Map host binaries & libraries as readonly. The binaries will be
	// executed in gVisor sandbox, not on the host.
	if !cfg.SkipHostBinaryMounts {
		for _, p := range hostBinaryDirs {
			if _, err := os.Stat(p); err == nil {
				opts := []string{"rbind", "ro", "nosuid", "nodev"}
				if p == "/etc/alternatives" {
					opts = []string{"rbind", "ro"}
				}
				spec.Mounts = append(spec.Mounts, specs.Mount{
					Destination: p,
					Type:        "bind",
					Source:      p,
					Options:     opts,
				})
			}
		}
	}

	// Add custom mounts. Custom mounts overriding default host mounts create duplicate OCI
	// entries. The later entry overrides the earlier one, as expected by OCI specs.
	for _, m := range cfg.Mounts {
		om, err := m.ociMount()
		if err != nil {
			return nil, err
		}
		spec.Mounts = append(spec.Mounts, om)
	}

	return spec, nil
}

// newSandboxSpec builds the OCI runtime specification of the sandbox itself.
// The sandbox has no root container, so the spec has no process and no root.
// Its mount hints make each tmpfs in cfg.Mounts one instance that all the
// containers share.
func newSandboxSpec(cfg bundleConfig, containerSpec *specs.Spec) (*specs.Spec, error) {
	annotations := make(map[string]string)
	for i, m := range cfg.Mounts {
		if m.Type != MountTypeTmpfs {
			continue
		}
		om, err := m.ociMount()
		if err != nil {
			return nil, err
		}
		prefix := fmt.Sprintf("dev.gvisor.spec.mount.tmpfs%d.", i)
		annotations[prefix+"source"] = sharedTmpfsSource(i)
		annotations[prefix+"type"] = "tmpfs"
		annotations[prefix+"share"] = "pod"
		if len(om.Options) > 0 {
			annotations[prefix+"options"] = strings.Join(om.Options, ",")
		}
	}
	return &specs.Spec{
		Version:     "1.0.0",
		Annotations: annotations,
		Hostname:    cfg.Hostname,
		Linux:       containerSpec.Linux,
	}, nil
}

// newBundle creates the sandbox's OCI bundle on the fly with the given
// configuration. It returns the bundle directory and the spec of the
// containers that Exec starts, whose root path is absolute.
func newBundle(cfg bundleConfig) (string, *specs.Spec, error) {
	spec, err := newSpec(cfg)
	if err != nil {
		return "", nil, err
	}
	sandboxSpec, err := newSandboxSpec(cfg, spec)
	if err != nil {
		return "", nil, err
	}

	bundleDir := filepath.Join(cfg.RuntimeDir, cfg.ID)
	createDir := bundleDir
	if cfg.RootPath == "" {
		createDir = filepath.Join(bundleDir, defaultRootfsDir)
	}
	if err := os.MkdirAll(createDir, 0755); err != nil {
		return "", nil, fmt.Errorf("failed to create bundle directories: %w", err)
	}
	// Each container has its own bundle directory, so relative host paths
	// must be resolved against the sandbox's.
	if !filepath.IsAbs(spec.Root.Path) {
		spec.Root.Path = filepath.Join(bundleDir, spec.Root.Path)
	}
	for i := range spec.Mounts {
		if m := &spec.Mounts[i]; m.Type == "bind" && !filepath.IsAbs(m.Source) {
			m.Source = filepath.Join(bundleDir, m.Source)
		}
	}
	// cfg.Mounts come last in spec.Mounts. Point each tmpfs among them at the
	// source the sandbox spec shares.
	customMounts := spec.Mounts[len(spec.Mounts)-len(cfg.Mounts):]
	for i, m := range cfg.Mounts {
		if m.Type == MountTypeTmpfs {
			customMounts[i].Source = sharedTmpfsSource(i)
		}
	}

	if err := writeSpec(bundleDir, sandboxSpec); err != nil {
		return "", nil, err
	}
	return bundleDir, spec, nil
}

// writeSpec writes spec to config.json in dir.
func writeSpec(dir string, spec *specs.Spec) error {
	configPath := filepath.Join(dir, "config.json")
	configFile, err := os.Create(configPath)
	if err != nil {
		return fmt.Errorf("failed to create config.json: %w", err)
	}
	defer configFile.Close()

	encoder := json.NewEncoder(configFile)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(spec); err != nil {
		return fmt.Errorf("failed to encode config.json: %w", err)
	}
	return nil
}
