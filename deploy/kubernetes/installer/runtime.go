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
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	k3sContainerdDir = "/var/lib/rancher/k3s/agent/etc/containerd"
	k3sSocket        = "/run/k3s/containerd/containerd.sock"
	// k3sBaseTemplate makes a k3s containerd template render the default
	// config that k3s generates.
	k3sBaseTemplate = "{{ template \"base\" . }}\n"
)

// runtimeTarget describes how containerd is set up on a node.
type runtimeTarget struct {
	// name is the setup name, for logs.
	name string
	// configPath is the file that the installer edits.
	configPath string
	// baseContent is the content of configPath when the installer creates
	// it. If empty, configPath must already exist.
	baseContent string
	// versionPath is the containerd config that gives the config version.
	versionPath string
	// effectivePath, if set, is the config that containerd loads, generated
	// from configPath at restart.
	effectivePath string
	// socket is the containerd socket.
	socket string
	// restartCmd restarts containerd.
	restartCmd []string
}

// detectRuntime returns the runtime setup of the node. It fails on setups
// that the installer does not handle yet, instead of leaving them
// half-configured.
func detectRuntime(c *config) (*runtimeTarget, error) {
	for _, p := range []string{"/var/lib/rancher/rke2", "/etc/k0s", "/var/snap/microk8s"} {
		if _, err := os.Stat(c.host(p)); err == nil {
			return nil, fmt.Errorf("found %s: this distribution is not supported yet", p)
		}
	}
	if _, err := os.Stat(c.host(filepath.Join(k3sContainerdDir, "config.toml"))); err == nil {
		return detectK3s(c)
	}
	if _, err := os.Stat(c.host(defaultAddr)); err != nil {
		return nil, fmt.Errorf("containerd socket %s not found: only containerd and k3s are supported", defaultAddr)
	}
	return &runtimeTarget{
		name:        "containerd",
		configPath:  c.containerdConf,
		versionPath: c.containerdConf,
		socket:      defaultAddr,
		restartCmd:  restartCommand(c, "containerd"),
	}, nil
}

// detectK3s handles k3s, which embeds containerd and generates its config
// from a template at each start.
func detectK3s(c *config) (*runtimeTarget, error) {
	generated := filepath.Join(k3sContainerdDir, "config.toml")
	data, err := os.ReadFile(c.host(generated))
	if err != nil {
		return nil, err
	}
	version, err := configVersion(string(data))
	if err != nil {
		return nil, err
	}
	// k3s with containerd 2.x reads config-v3.toml.tmpl, older versions
	// read config.toml.tmpl. Edit an existing template; else create the one
	// that matches the generated config version.
	names := []string{"config.toml.tmpl", "config-v3.toml.tmpl"}
	if version >= 3 {
		names[0], names[1] = names[1], names[0]
	}
	tmpl := filepath.Join(k3sContainerdDir, names[0])
	for _, n := range names {
		if _, err := os.Stat(c.host(filepath.Join(k3sContainerdDir, n))); err == nil {
			tmpl = filepath.Join(k3sContainerdDir, n)
			break
		}
	}
	unit := "k3s"
	if out, err := hostCommand(c, "systemctl", "is-active", "k3s"); err != nil || strings.TrimSpace(out) != "active" {
		unit = "k3s-agent"
	}
	return &runtimeTarget{
		name:          "k3s (" + unit + ")",
		configPath:    tmpl,
		baseContent:   k3sBaseTemplate,
		versionPath:   generated,
		effectivePath: generated,
		socket:        k3sSocket,
		restartCmd:    restartCommand(c, unit),
	}, nil
}

func restartCommand(c *config, unit string) []string {
	if len(c.restartCmd) > 0 {
		return c.restartCmd
	}
	return []string{"systemctl", "restart", unit}
}
