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
	"regexp"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

const (
	blockBegin = "# BEGIN gvisor-deploy: managed by the gVisor installer. Do not edit."
	blockEnd   = "# END gvisor-deploy"
	// blockBeginNoEOL starts the block in a file that had no final newline
	// before the block was added, so that removeBlock restores it exactly.
	blockBeginNoEOL = blockBegin + " (file had no final newline)"
)

var (
	versionRE = regexp.MustCompile(`^\s*version\s*=\s*(\d+)\s*(#.*)?$`)
	tableRE   = regexp.MustCompile(`^\s*\[`)
)

// configVersion returns the value of the top-level "version" key of a
// containerd config file, or 0 if the key is not present.
func configVersion(config string) (int, error) {
	for _, line := range strings.Split(config, "\n") {
		if tableRE.MatchString(line) {
			break
		}
		if m := versionRE.FindStringSubmatch(line); m != nil {
			return strconv.Atoi(m[1])
		}
	}
	return 0, nil
}

// criPlugin returns the name of the CRI runtime plugin for a containerd
// config version.
func criPlugin(version int) (string, error) {
	switch version {
	case 2:
		return "io.containerd.grpc.v1.cri", nil
	case 3, 4:
		return "io.containerd.cri.v1.runtime", nil
	default:
		return "", fmt.Errorf("unsupported containerd config version %d", version)
	}
}

// runtimeBlock returns the containerd config tables that register the runsc
// runtime handler, between marker lines.
//
// The tables go in the main config file, not in a file listed in "imports":
// containerd 1.x replaces whole plugin sections with the ones of imported
// files, which drops the rest of the CRI config.
func runtimeBlock(version int, handler, shimPath, configPath string, podAnnotations []string) (string, error) {
	plugin, err := criPlugin(version)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString(blockBegin + "\n")
	fmt.Fprintf(&b, "[plugins.%q.containerd.runtimes.%s]\n", plugin, handler)
	b.WriteString("  runtime_type = \"io.containerd.runsc.v1\"\n")
	fmt.Fprintf(&b, "  runtime_path = %q\n", shimPath)
	if len(podAnnotations) > 0 {
		quoted := make([]string, len(podAnnotations))
		for i, a := range podAnnotations {
			quoted[i] = strconv.Quote(a)
		}
		fmt.Fprintf(&b, "  pod_annotations = [%s]\n", strings.Join(quoted, ", "))
	}
	fmt.Fprintf(&b, "[plugins.%q.containerd.runtimes.%s.options]\n", plugin, handler)
	b.WriteString("  TypeUrl = \"io.containerd.runsc.v1.options\"\n")
	fmt.Fprintf(&b, "  ConfigPath = %q\n", configPath)
	b.WriteString(blockEnd + "\n")
	return b.String(), nil
}

// removeBlock returns config without the gvisor-deploy block, and without
// the separator that setBlock adds before it.
func removeBlock(config string) (string, error) {
	noEOL := true
	begin := strings.Index(config, blockBeginNoEOL+"\n")
	if begin < 0 {
		noEOL = false
		begin = strings.Index(config, blockBegin+"\n")
	}
	if begin < 0 {
		return config, nil
	}
	end := strings.Index(config[begin:], blockEnd+"\n")
	if end < 0 {
		return "", fmt.Errorf("found %q without %q", blockBegin, blockEnd)
	}
	end += begin + len(blockEnd) + 1
	switch {
	case noEOL && strings.HasSuffix(config[:begin], "\n\n"):
		begin -= 2
	case !noEOL && begin > 0 && strings.HasSuffix(config[:begin], "\n\n"):
		begin--
	}
	return config[:begin] + config[end:], nil
}

// setBlock returns config with block, replacing any previous gvisor-deploy
// block. It fails if handler is already configured outside the block, since
// TOML forbids defining a table twice.
func setBlock(config, block, handler string) (string, error) {
	base, err := removeBlock(config)
	if err != nil {
		return "", err
	}
	if handlerDefined(base, handler) {
		return "", fmt.Errorf("runtime handler %q is already configured outside of the gvisor-deploy block", handler)
	}
	if base == "" {
		return block, nil
	}
	if !strings.HasSuffix(base, "\n") {
		base += "\n"
		block = strings.Replace(block, blockBegin+"\n", blockBeginNoEOL+"\n", 1)
	}
	return base + "\n" + block, nil
}

// handlerDefined reports whether a containerd config defines a CRI runtime
// handler. It parses the config as TOML, without the lines that hold Go
// template actions (k3s templates). It also matches the handler's table
// names as text, in case the config does not parse.
func handlerDefined(config, handler string) bool {
	for _, name := range []string{handler, `"` + handler + `"`, "'" + handler + "'"} {
		for _, suffix := range []string{"]", ".", " ", "="} {
			if strings.Contains(config, "runtimes."+name+suffix) {
				return true
			}
		}
	}
	var lines []string
	for _, line := range strings.Split(config, "\n") {
		if !strings.Contains(line, "{{") {
			lines = append(lines, line)
		}
	}
	var parsed struct {
		Plugins map[string]struct {
			Containerd struct {
				Runtimes map[string]any `toml:"runtimes"`
			} `toml:"containerd"`
		} `toml:"plugins"`
	}
	if _, err := toml.Decode(strings.Join(lines, "\n"), &parsed); err != nil {
		return false
	}
	for _, p := range parsed.Plugins {
		if _, ok := p.Containerd.Runtimes[handler]; ok {
			return true
		}
	}
	return false
}

// runscConfig returns the runsc shim config file. flags holds "key=value"
// lines for the [runsc_config] section.
func runscConfig(binaryName string, flags []string) (string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "# Generated by gvisor-deploy. Do not edit.\n")
	fmt.Fprintf(&b, "binary_name = %q\n", binaryName)
	b.WriteString("\n[runsc_config]\n")
	for _, f := range flags {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		k, v, ok := strings.Cut(f, "=")
		k = strings.TrimLeft(strings.TrimSpace(k), "-")
		if !ok || k == "" || strings.ContainsAny(k, " \t\"'[]=") {
			return "", fmt.Errorf("invalid runsc flag %q, want key=value", f)
		}
		fmt.Fprintf(&b, "  %s = %q\n", k, strings.TrimSpace(v))
	}
	return b.String(), nil
}
