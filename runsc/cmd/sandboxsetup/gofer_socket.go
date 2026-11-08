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

package sandboxsetup

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"

	specs "github.com/opencontainers/runtime-spec/specs-go"
	"gvisor.dev/gvisor/pkg/unet"
	"gvisor.dev/gvisor/runsc/cmd/util"
)

// MountOpener is used in two cases:
//
// (1) when the gofer process cannot access a mount source directly (e.g.
// due to permission restrictions in a user namespace). In this case, the
// returned file is just the opened mount source.
//
// (2) when the mount is id-mapped, which the gofer process cannot configure
// on its own. In this case, the returned file is an open_tree() detached mount
// point which has had the requested id mapping configured using mount_setattr().
//
// The caller is responsible for closing the returned file.
//
// If nil, the current process opens mount sources itself.
type MountOpener func(m *specs.Mount, flags uint32) (*os.File, error)

// NewSocket creates a unet.Socket from a file descriptor.
// It fatally exits if the socket cannot be created.
func NewSocket(ioFD int) *unet.Socket {
	socket, err := unet.NewSocket(ioFD)
	if err != nil {
		util.Fatalf("creating server on FD %d: %v", ioFD, err)
	}
	return socket
}

// WriteMounts serializes the given mounts as JSON and writes them to the
// given file descriptor.
func WriteMounts(mountsFD int, mounts []specs.Mount) error {
	bytes, err := json.Marshal(mounts)
	if err != nil {
		return err
	}

	f := os.NewFile(uintptr(mountsFD), "mounts file")
	defer f.Close()

	for written := 0; written < len(bytes); {
		w, err := f.Write(bytes[written:])
		if err != nil {
			return err
		}
		written += w
	}
	return nil
}

// ShouldExposeNvidiaDevice returns true if path refers to an Nvidia device
// which should be exposed to the container.
//
// Precondition: nvproxy is enabled.
func ShouldExposeNvidiaDevice(path string) bool {
	if !strings.HasPrefix(path, "/dev/nvidia") {
		return false
	}
	if path == "/dev/nvidiactl" || path == "/dev/nvidia-uvm" || path == "/dev/nvidia-uvm-tools" {
		return true
	}
	nvidiaDevPathReg := regexp.MustCompile(`^/dev/nvidia(\d+)$`)
	return nvidiaDevPathReg.MatchString(path)
}

// ShouldExposeVFIODevice returns true if path refers to a VFIO device
// which should be exposed to the container.
func ShouldExposeVFIODevice(path string) bool {
	return strings.HasPrefix(path, vfioPathDir)
}

// ShouldExposeTpuDevice returns true if path refers to a TPU device which
// should be exposed to the container.
//
// Precondition: tpuproxy is enabled.
func ShouldExposeTpuDevice(path string) bool {
	valid, _ := util.IsTPUDeviceValid(path)
	return valid || ShouldExposeVFIODevice(path)
}

// ShouldExposeRDMADevice returns true if path refers to an RDMA uverbs device.
//
// Precondition: rdmaproxy is enabled.
func ShouldExposeRDMADevice(path string) bool {
	return strings.HasPrefix(path, "/dev/infiniband/uverbs")
}

// ProcFDBindMount is the path where /proc/self/fd is bind-mounted inside the
// gofer's mount tree. Redhat distros don't allow bind-mounts in /proc/self
// directories due to SELinux rules.
const ProcFDBindMount = "/proc/fs"

// vfioPathDir is the directory containing VFIO device nodes.
const vfioPathDir = "/dev/vfio"
