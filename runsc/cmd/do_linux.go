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

//go:build linux

package cmd

import (
	"fmt"

	specs "github.com/opencontainers/runtime-spec/specs-go"
	"gvisor.dev/gvisor/pkg/log"
	"gvisor.dev/gvisor/runsc/config"
)

// setupNetwork prepares the network of the container cid, and returns a
// function that tears it down.
func (c *Do) setupNetwork(conf *config.Config, cid string, spec *specs.Spec) (func(), error) {
	if conf.Network == config.NetworkNone {
		addNamespace(spec, specs.LinuxNamespace{Type: specs.NetworkNamespace})
	} else if conf.Rootless {
		if conf.Network == config.NetworkSandbox {
			c.notifyUser("*** Warning: sandbox network isn't supported with --rootless, switching to host ***")
			conf.Network = config.NetworkHost
		}
	} else {
		switch clean, err := c.setupNet(cid, spec); err {
		case errNoDefaultInterface:
			log.Warningf("Network interface not found, using internal network")
			addNamespace(spec, specs.LinuxNamespace{Type: specs.NetworkNamespace})
			conf.Network = config.NetworkHost

		case nil:
			// Setup successful.
			return clean, nil

		default:
			return nil, fmt.Errorf("Error setting up network: %v", err)
		}
	}

	return func() {}, nil
}
