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

package specutils

import (
	"fmt"
	"os"

	"path/filepath"

	"strings"

	"github.com/moby/sys/capability"
	specs "github.com/opencontainers/runtime-spec/specs-go"

	"gvisor.dev/gvisor/pkg/log"
)

// nsPath returns the path of the namespace for the current process and the
// given namespace.
func nsPath(nst specs.LinuxNamespaceType) string {
	// LINT.IfChange
	base := "/proc/self/ns"
	switch nst {
	case specs.CgroupNamespace:
		return filepath.Join(base, "cgroup")
	case specs.IPCNamespace:
		return filepath.Join(base, "ipc")
	case specs.MountNamespace:
		return filepath.Join(base, "mnt")
	case specs.NetworkNamespace:
		return filepath.Join(base, "net")
	case specs.PIDNamespace:
		return filepath.Join(base, "pid")
	case specs.UserNamespace:
		return filepath.Join(base, "user")
	case specs.UTSNamespace:
		return filepath.Join(base, "uts")
	default:
		panic(fmt.Sprintf("unknown namespace %v", nst))
	}
	// LINT.ThenChange(:KnownNamespaces)
}

// GetNS returns true and the namespace with the given type from the slice of
// namespaces in the spec.  It returns false if the slice does not contain a
// namespace with the type.
func GetNS(nst specs.LinuxNamespaceType, s *specs.Spec) (specs.LinuxNamespace, bool) {
	if s.Linux == nil {
		return specs.LinuxNamespace{}, false
	}
	for _, ns := range s.Linux.Namespaces {
		if ns.Type == nst {
			return ns, true
		}
	}
	return specs.LinuxNamespace{}, false
}

// HasCapabilities returns true if the user has all capabilities in 'cs'.
func HasCapabilities(cs ...capability.Cap) bool {
	caps, err := capability.NewPid2(0)
	if err != nil {
		return false
	}
	if err := caps.Load(); err != nil {
		return false
	}
	for _, c := range cs {
		if !caps.Get(capability.EFFECTIVE, c) {
			return false
		}
	}
	return true
}

// SetgroupsAllowed returns true if the user is allowed to set supplementary
// groups.
func SetgroupsAllowed() bool {
	policy, err := os.ReadFile("/proc/self/setgroups")
	if err != nil {
		log.Warningf("Failed to read the user namespace setgroups policy; err: %v", err)
		return false
	}
	return strings.TrimSpace(string(policy)) == "allow"
}

// NullNetNSFilename is the name of the file (relative to `--shared-root`,
// which defaults to `--root`) that the shared empty ("null") gofer network
// namespace is bind-mounted to.
const NullNetNSFilename = "null-netns"

// KnownNamespaces returns a list of all supported namespace types.
// Used by `runsc features`.
func KnownNamespaces() []string {
	// LINT.IfChange
	return []string{
		string(specs.CgroupNamespace),
		string(specs.IPCNamespace),
		string(specs.MountNamespace),
		string(specs.NetworkNamespace),
		string(specs.PIDNamespace),
		string(specs.UserNamespace),
		string(specs.UTSNamespace),
	}
	// LINT.ThenChange(:nsPath)
}
