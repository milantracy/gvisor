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
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testOriginal = "version = 2\n\n[plugins]\n  x = 1\n"

// fakeRestart returns restart results in order, and records the wantBlock
// argument of each call.
type fakeRestart struct {
	results []error
	calls   []bool
}

func (f *fakeRestart) restart(_ *config, wantBlock bool) error {
	f.calls = append(f.calls, wantBlock)
	if len(f.results) == 0 {
		return nil
	}
	err := f.results[0]
	f.results = f.results[1:]
	return err
}

func testConfig(t *testing.T, f *fakeRestart) (*config, func(string) (string, error)) {
	t.Helper()
	c := &config{
		hostRoot:   t.TempDir(),
		installDir: "/opt/gvisor",
		handler:    "runsc",
		rt: &runtimeTarget{
			name:        "test",
			configPath:  "/etc/containerd/config.toml",
			versionPath: "/etc/containerd/config.toml",
		},
		restart: f.restart,
	}
	if err := os.MkdirAll(c.host("/etc/containerd"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(c.host(c.rt.configPath), []byte(testOriginal), 0644); err != nil {
		t.Fatal(err)
	}
	block := testBlock(t, 2, "/opt/gvisor/bin/containerd-shim-runsc-v1")
	return c, func(s string) (string, error) { return setBlock(s, block, "runsc") }
}

func readHost(t *testing.T, c *config, p string) string {
	t.Helper()
	data, err := os.ReadFile(c.host(p))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func exists(c *config, name string) bool {
	_, err := os.Stat(c.host(filepath.Join(c.installDir, name)))
	return err == nil
}

func TestApplyConfigSuccess(t *testing.T) {
	f := &fakeRestart{}
	c, set := testConfig(t, f)
	if err := applyContainerdConfig(c, set, true); err != nil {
		t.Fatalf("applyContainerdConfig: %v", err)
	}
	if got := readHost(t, c, c.rt.configPath); !strings.Contains(got, blockBegin) {
		t.Errorf("config has no block:\n%s", got)
	}
	if len(f.calls) != 1 || !f.calls[0] {
		t.Errorf("restart calls = %v, want [true]", f.calls)
	}
	if exists(c, restartPending) || exists(c, configBackup) {
		t.Errorf("restart marker or backup left after success")
	}
	// Applying the same change again does not restart.
	if err := applyContainerdConfig(c, set, true); err != nil || len(f.calls) != 1 {
		t.Errorf("second apply: err=%v, restart calls=%v", err, f.calls)
	}
}

func TestApplyConfigRollback(t *testing.T) {
	f := &fakeRestart{results: []error{errors.New("CRI plugin status: error"), nil}}
	c, set := testConfig(t, f)
	err := applyContainerdConfig(c, set, true)
	var r refusal
	if !errors.As(err, &r) {
		t.Fatalf("applyContainerdConfig = %v, want a refusal", err)
	}
	if got := readHost(t, c, c.rt.configPath); got != testOriginal {
		t.Errorf("config not restored:\n got %q\nwant %q", got, testOriginal)
	}
	if len(f.calls) != 2 || !f.calls[0] || f.calls[1] {
		t.Errorf("restart calls = %v, want [true false]", f.calls)
	}
	if exists(c, restartPending) || exists(c, configBackup) {
		t.Errorf("restart marker or backup left after rollback")
	}
}

func TestApplyConfigRollbackFails(t *testing.T) {
	f := &fakeRestart{results: []error{errors.New("down"), errors.New("still down")}}
	c, set := testConfig(t, f)
	err := applyContainerdConfig(c, set, true)
	var r refusal
	if err == nil || errors.As(err, &r) {
		t.Fatalf("applyContainerdConfig = %v, want a non-refusal error", err)
	}
	if got := readHost(t, c, c.rt.configPath); got != testOriginal {
		t.Errorf("config not restored:\n got %q\nwant %q", got, testOriginal)
	}
	// The next run must know that a restart is pending.
	if !exists(c, restartPending) || !exists(c, configBackup) {
		t.Errorf("restart marker or backup removed although containerd is unhealthy")
	}
}

func TestApplyConfigAfterInterruptedRun(t *testing.T) {
	f := &fakeRestart{results: []error{errors.New("CRI plugin status: error"), nil}}
	c, set := testConfig(t, f)
	// An earlier run changed the config and was killed before the restart.
	changed, err := set(testOriginal)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(c.host(c.rt.configPath), []byte(changed), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(c.host(c.installDir), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(c.host(filepath.Join(c.installDir, configBackup)), []byte("1"+testOriginal), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(c.host(filepath.Join(c.installDir, restartPending)), nil, 0644); err != nil {
		t.Fatal(err)
	}
	err = applyContainerdConfig(c, set, true)
	var r refusal
	if !errors.As(err, &r) {
		t.Fatalf("applyContainerdConfig = %v, want a refusal", err)
	}
	if len(f.calls) == 0 {
		t.Fatalf("pending restart not done")
	}
	if got := readHost(t, c, c.rt.configPath); got != testOriginal {
		t.Errorf("config not restored from the backup:\n got %q\nwant %q", got, testOriginal)
	}
}

func TestRemoveConfigRollback(t *testing.T) {
	f := &fakeRestart{}
	c, set := testConfig(t, f)
	if err := applyContainerdConfig(c, set, true); err != nil {
		t.Fatalf("install: %v", err)
	}
	installed := readHost(t, c, c.rt.configPath)
	f.results = []error{errors.New("CRI plugin status: error"), nil}
	err := applyContainerdConfig(c, removeBlock, false)
	var r refusal
	if err == nil || errors.As(err, &r) {
		t.Fatalf("uninstall = %v, want a non-refusal error", err)
	}
	if got := readHost(t, c, c.rt.configPath); got != installed {
		t.Errorf("config not restored:\n got %q\nwant %q", got, installed)
	}
	if calls := f.calls[1:]; len(calls) != 2 || calls[0] || !calls[1] {
		t.Errorf("uninstall restart calls = %v, want [false true]", calls)
	}
}

func TestHandlerName(t *testing.T) {
	for _, h := range []string{"runsc", "runsc-v2", "a"} {
		if !handlerRE.MatchString(h) {
			t.Errorf("handler %q rejected", h)
		}
	}
	for _, h := range []string{"", "bad name", "runsc]", "Runsc", "-runsc", "runsc.x", strings.Repeat("a", 64)} {
		if handlerRE.MatchString(h) {
			t.Errorf("handler %q accepted", h)
		}
	}
}
