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
	"time"

	"golang.org/x/sys/unix"
	"gvisor.dev/gvisor/pkg/abi/linux"
	"gvisor.dev/gvisor/pkg/memutil"
	"gvisor.dev/gvisor/pkg/seatbelt"
)

const (
	profileChildEnv = "HVFBOOT_PROFILE_TEST_CHILD"
	sentryChildEnv  = "HVFBOOT_SENTRY_TEST_CHILD"
	netChildEnv     = "HVFBOOT_NET_TEST_CHILD"
)

func TestMain(m *testing.M) {
	if base := os.Getenv(profileChildEnv); base != "" {
		os.Exit(profileChild(base))
	}
	if path := os.Getenv(sentryChildEnv); path != "" {
		os.Exit(sentryChild(path))
	}
	if dir := os.Getenv(netChildEnv); dir != "" {
		os.Exit(netChild(dir))
	}
	os.Exit(m.Run())
}

// printCheck prints "name: ok", "name: denied" or "name: <error>".
func printCheck(name string, err error) {
	switch {
	case err == nil:
		fmt.Printf("%s: ok\n", name)
	case errors.Is(err, unix.EPERM), errors.Is(err, unix.EACCES):
		fmt.Printf("%s: denied\n", name)
	default:
		fmt.Printf("%s: %v\n", name, err)
	}
}

// runChild runs the test binary with env set, and checks that it prints
// want.
func runChild(t *testing.T, env string, want []string) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(self)
	cmd.Env = append(os.Environ(), env)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("child: %v\n%s", err, out)
	}
	if w := strings.Join(want, "\n") + "\n"; string(out) != w {
		t.Errorf("child output:\n%s\nwant:\n%s", out, w)
	}
}

// sentryChild opens path, confines itself as the Sentry, and prints one
// "name: result" line per check.
func sentryChild(path string) int {
	f, err := os.Open(path)
	if err != nil {
		fmt.Printf("open: %v\n", err)
		return 1
	}
	w, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		fmt.Printf("open: %v\n", err)
		return 1
	}
	if err := confineSentry(nil); err != nil {
		fmt.Printf("confine: %v\n", err)
		return 1
	}
	var st unix.Stat_t
	buf := make([]byte, 4)
	_, err = f.ReadAt(buf, 0)
	printCheck("read FD opened before", err)
	printCheck("fstat readable FD", unix.Fstat(int(f.Fd()), &st))
	_, err = w.WriteAt([]byte("x"), 1)
	printCheck("write FD opened before", err)
	printCheck("fstat write-only FD", unix.Fstat(int(w.Fd()), &st))
	printCheck("fstat write-only stdout", unix.Fstat(1, &st))
	_, err = os.ReadFile(path)
	printCheck("open same file", err)
	_, err = os.ReadFile("/etc/hosts")
	printCheck("read /etc/hosts", err)
	_, err = os.ReadDir("/")
	printCheck("list /", err)
	printCheck("write file", os.WriteFile(path+".new", []byte("x"), 0644))
	printCheck("exec", exec.Command("/bin/true").Run())
	c, err := net.Dial("tcp", "127.0.0.1:9")
	if err == nil {
		c.Close()
	}
	if errors.Is(err, unix.ECONNREFUSED) {
		err = nil // The connection was attempted, so it was allowed.
	}
	printCheck("connect tcp", err)
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err == nil {
		unix.Close(fds[0])
		unix.Close(fds[1])
	}
	printCheck("socketpair", err)
	return 0
}

// TestSentryProfile runs a child with a write-only O_APPEND file as standard
// output, as `hvfboot ... >> out` does. The child writes its results there.
func TestSentryProfile(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(base, "image")
	if err := os.WriteFile(path, []byte("data"), 0644); err != nil {
		t.Fatal(err)
	}
	outPath := filepath.Join(base, "out")
	if err := os.WriteFile(outPath, []byte("pre\n"), 0644); err != nil {
		t.Fatal(err)
	}
	out, err := os.OpenFile(outPath, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(self)
	cmd.Env = append(os.Environ(), sentryChildEnv+"="+path)
	cmd.Stdout = out
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("child: %v\n%s", err, stderr.String())
	}
	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		"pre",
		"read FD opened before: ok",
		"fstat readable FD: ok",
		"write FD opened before: ok",
		// Seatbelt checks fstat of an FD that is not open for reading.
		"fstat write-only FD: denied",
		"fstat write-only stdout: ok",
		"open same file: denied",
		"read /etc/hosts: denied",
		"list /: denied",
		"write file: denied",
		"exec: denied",
		"connect tcp: denied",
		"socketpair: ok",
	}, "\n") + "\n"
	if string(got) != want {
		t.Errorf("child output:\n%s\nwant:\n%s", got, want)
	}
}

// netChild listens on host loopback and a Unix socket in dir, confines
// itself as the network proxy, and prints one "name: result" line per check.
func netChild(dir string) int {
	tcpL, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Printf("listen: %v\n", err)
		return 1
	}
	defer tcpL.Close()
	unixL, err := net.Listen("unix", filepath.Join(dir, "sock"))
	if err != nil {
		fmt.Printf("listen: %v\n", err)
		return 1
	}
	defer unixL.Close()
	if err := seatbelt.Init(netProfile); err != nil {
		fmt.Printf("init: %v\n", err)
		return 1
	}
	dial := func(network, addr string) error {
		c, err := net.DialTimeout(network, addr, 5*time.Second)
		if err == nil {
			c.Close()
		}
		return err
	}
	printCheck("connect tcp host loopback", dial("tcp", tcpL.Addr().String()))
	printCheck("connect unix socket", dial("unix", filepath.Join(dir, "sock")))
	// 192.0.2.0/24 is for documentation (RFC 5737). A UDP send needs no
	// reply; ENETUNREACH means that the profile allowed it.
	u, err := net.Dial("udp", "192.0.2.1:9")
	if err == nil {
		_, err = u.Write([]byte("x"))
		u.Close()
	}
	if errors.Is(err, unix.ENETUNREACH) || errors.Is(err, unix.EHOSTUNREACH) {
		err = nil
	}
	printCheck("send udp", err)
	_, err = os.ReadFile("/etc/hosts")
	printCheck("read /etc/hosts", err)
	printCheck("write file", os.WriteFile(filepath.Join(dir, "f"), []byte("x"), 0644))
	printCheck("exec", exec.Command("/bin/true").Run())
	return 0
}

func TestNetProfile(t *testing.T) {
	// Unix socket paths are short; t.TempDir may be too long.
	dir, err := os.MkdirTemp("/tmp", "nettest")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	runChild(t, netChildEnv+"="+dir, []string{
		"connect tcp host loopback: denied",
		"connect unix socket: denied",
		"send udp: ok",
		"read /etc/hosts: denied",
		"write file: denied",
		"exec: denied",
	})
}

func TestSentryProfileRules(t *testing.T) {
	got := sentryProfile([]string{"/out"}, []string{`/src "a"`})
	want := `(version 1)
(deny default)
(allow file-read-metadata (literal "/out"))
(allow file-read-metadata (subpath "/src \"a\""))
`
	if got != want {
		t.Errorf("sentryProfile:\n%s\nwant:\n%s", got, want)
	}
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
	check := printCheck
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
	runChild(t, profileChildEnv+"="+base, []string{
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
	})
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
