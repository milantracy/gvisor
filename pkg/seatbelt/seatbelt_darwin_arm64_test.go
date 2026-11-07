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

//go:build darwin && arm64

package seatbelt

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
)

const childEnv = "SEATBELT_TEST_CHILD"

func TestMain(m *testing.M) {
	if dirs := os.Getenv(childEnv); dirs != "" {
		os.Exit(child(dirs))
	}
	os.Exit(m.Run())
}

// child confines itself to dirs ("rw:ro"), makes the checks, and prints one
// "name: result" line per check.
func child(dirs string) int {
	rw, ro, _ := strings.Cut(dirs, ":")
	outside := filepath.Dir(rw)
	// The test binary is opened before Init; it must not be readable after.
	profile := fmt.Sprintf(`(version 1)
(deny default)
(allow file-read* file-write* (subpath %s))
(allow file-read* (subpath %s))
`, Quote(rw), Quote(ro))
	if err := Init(profile); err != nil {
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
	check("mkdir rw", os.Mkdir(filepath.Join(rw, "d"), 0755))
	_, err = os.ReadFile(filepath.Join(ro, "f"))
	check("read ro", err)
	check("write ro", os.WriteFile(filepath.Join(ro, "new"), []byte("x"), 0644))
	_, err = os.ReadFile(filepath.Join(outside, "secret"))
	check("read outside", err)
	_, err = os.ReadFile("/etc/hosts")
	check("read /etc/hosts", err)
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

func TestQuote(t *testing.T) {
	for in, want := range map[string]string{
		`/a/b`:   `"/a/b"`,
		`/a"b`:   `"/a\"b"`,
		`/a\b`:   `"/a\\b"`,
		`/a b/c`: `"/a b/c"`,
	} {
		if got := Quote(in); got != want {
			t.Errorf("Quote(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestInit(t *testing.T) {
	// Seatbelt matches real paths; TempDir may be under the /var symlink.
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	rw, ro := filepath.Join(base, "rw"), filepath.Join(base, `ro "q"`)
	for _, d := range []string{rw, ro} {
		if err := os.Mkdir(d, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "f"), []byte("data"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(base, "secret"), []byte("s"), 0644); err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(self)
	cmd.Env = append(os.Environ(), childEnv+"="+rw+":"+ro)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("child: %v\n%s", err, out)
	}
	want := `read rw: ok
write rw: ok
mkdir rw: ok
read ro: ok
write ro: denied
read outside: denied
read /etc/hosts: denied
exec: denied
connect tcp: denied
`
	if string(out) != want {
		t.Errorf("child output:\n%s\nwant:\n%s", out, want)
	}
}
