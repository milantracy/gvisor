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
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
	"gvisor.dev/gvisor/pkg/abi/linux"
	"gvisor.dev/gvisor/pkg/memutil"
	"gvisor.dev/gvisor/pkg/seatbelt"
)

const profileChildEnv = "HVFBOOT_PROFILE_TEST_CHILD"

func TestMain(m *testing.M) {
	if base := os.Getenv(profileChildEnv); base != "" {
		os.Exit(profileChild(base))
	}
	os.Exit(m.Run())
}

// profileChild confines itself with the profile of a gofer that serves
// base/rw (read-write) and base/ro (read-only) with base/tmp as TMPDIR. It
// prints one "name: result" line per check.
func profileChild(base string) int {
	rw, ro, tmp := filepath.Join(base, "rw"), filepath.Join(base, "ro"), filepath.Join(base, "tmp")
	os.Setenv("TMPDIR", tmp)
	mounts := []goferMount{{source: rw}, {source: ro, readonly: true}}
	if err := seatbelt.Init(goferProfile(mounts, tmp)); err != nil {
		fmt.Printf("init: %v\n", err)
		return 1
	}
	check := func(name string, err error) {
		switch {
		case err == nil:
			fmt.Printf("%s: ok\n", name)
		case errors.Is(err, unix.EPERM), errors.Is(err, unix.EACCES):
			fmt.Printf("%s: denied\n", name)
		default:
			fmt.Printf("%s: %v\n", name, err)
		}
	}
	_, err := os.ReadFile(filepath.Join(rw, "f"))
	check("read rw", err)
	check("write rw", os.WriteFile(filepath.Join(rw, "new"), []byte("x"), 0644))
	check("rename rw", os.Rename(filepath.Join(rw, "new"), filepath.Join(rw, "new2")))
	_, err = os.ReadFile(filepath.Join(ro, "f"))
	check("read ro", err)
	check("write ro", os.WriteFile(filepath.Join(ro, "new"), []byte("x"), 0644))
	check("rename ro to rw", os.Rename(filepath.Join(ro, "f"), filepath.Join(rw, "f2")))
	_, err = os.ReadFile(filepath.Join(base, "secret"))
	check("read outside", err)
	_, err = os.ReadDir(base)
	check("list parent", err)
	fd, err := memutil.CreateMemFD("test", linux.MFD_CLOEXEC)
	if err == nil {
		unix.Close(fd)
	}
	check("memfd", err)
	check("exec", exec.Command("/bin/true").Run())
	c, err := net.Dial("tcp", "127.0.0.1:9")
	if err == nil {
		c.Close()
	}
	if errors.Is(err, unix.ECONNREFUSED) {
		err = nil // The connection was attempted, so it was allowed.
	}
	check("connect tcp", err)
	return 0
}

func TestGoferProfile(t *testing.T) {
	// Seatbelt matches real paths; TempDir may be under the /var symlink.
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"rw", "ro", "tmp"} {
		if err := os.Mkdir(filepath.Join(base, d), 0755); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{"rw/f", "ro/f", "secret"} {
		if err := os.WriteFile(filepath.Join(base, f), []byte("data"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(self)
	cmd.Env = append(os.Environ(), profileChildEnv+"="+base)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("child: %v\n%s", err, out)
	}
	want := strings.Join([]string{
		"read rw: ok",
		"write rw: ok",
		"rename rw: ok",
		"read ro: ok",
		"write ro: denied",
		"rename ro to rw: denied",
		"read outside: denied",
		"list parent: denied",
		"memfd: ok",
		"exec: denied",
		"connect tcp: denied",
	}, "\n") + "\n"
	if string(out) != want {
		t.Errorf("child output:\n%s\nwant:\n%s", out, want)
	}
}

func TestGoferProfileQuotes(t *testing.T) {
	got := goferProfile([]goferMount{{source: `/a "b"`, readonly: true}}, "/t")
	want := `(version 1)
(deny default)
(allow file-read* file-write* (subpath "/t"))
(allow file-read* (subpath "/a \"b\""))
`
	if got != want {
		t.Errorf("goferProfile:\n%s\nwant:\n%s", got, want)
	}
}
