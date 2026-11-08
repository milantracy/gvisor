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
	"fmt"

	specs "github.com/opencontainers/runtime-spec/specs-go"
	"gvisor.dev/gvisor/runsc/config"
)

// setupNetwork prepares the network of the container cid, and returns a
// function that tears it down.
//
// darwin has no network namespaces. With --network=sandbox, `runsc start`
// starts the network proxy of the sandbox (see runsc/sandbox), so there is
// nothing to prepare here.
func (c *Do) setupNetwork(conf *config.Config, cid string, spec *specs.Spec) (func(), error) {
	switch conf.Network {
	case config.NetworkNone, config.NetworkSandbox:
		return func() {}, nil
	default:
		return nil, fmt.Errorf("network type %v is not supported on darwin", conf.Network)
	}
}
