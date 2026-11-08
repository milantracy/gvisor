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

package filter

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
	"gvisor.dev/gvisor/pkg/timing"
)

const sentryChildEnv = "FILTER_SENTRY_TEST_CHILD"

func TestMain(m *testing.M) {
	if path := os.Getenv(sentryChildEnv); path != "" {
		os.Exit(sentryChild(path))
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
	// The control server listens before confinement and accepts a
	// connection for each runsc command after it.
	sockPath := filepath.Join(filepath.Dir(path), "ctrl")
	l, err := net.Listen("unix", sockPath)
	if err != nil {
		fmt.Printf("listen: %v\n", err)
		return 1
	}
	// The client is another process, as runsc is. It connects after the
	// child is confined, which cannot exec.
	client := exec.Command("/bin/sh", "-c", "sleep 1; exec /usr/bin/nc -U "+sockPath+" </dev/null")
	if err := client.Start(); err != nil {
		fmt.Printf("client: %v\n", err)
		return 1
	}
	defer client.Wait()
	if err := Install(Options{}, timing.New("test", time.Now())); err != nil {
		fmt.Printf("install: %v\n", err)
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
	accepted := make(chan error, 1)
	go func() {
		c, err := l.Accept()
		if err == nil {
			c.Close()
		}
		accepted <- err
	}()
	select {
	case err = <-accepted:
	case <-time.After(5 * time.Second):
		err = fmt.Errorf("timeout")
	}
	printCheck("accept on listening socket", err)
	return 0
}

// TestSentryProfile runs a child with a write-only O_APPEND file as standard
// output, as `runsc ... >> out` does. The child writes its results there.
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
		// Seatbelt checks fstat of an FD that is not open for reading;
		// Install allows it for all files that are open.
		"fstat write-only FD: ok",
		"fstat write-only stdout: ok",
		"open same file: denied",
		"read /etc/hosts: denied",
		"list /: denied",
		"write file: denied",
		"exec: denied",
		"connect tcp: denied",
		"socketpair: ok",
		"accept on listening socket: ok",
	}, "\n") + "\n"
	if string(got) != want {
		t.Errorf("child output:\n%s\nwant:\n%s", got, want)
	}
}

func TestSentryProfileRules(t *testing.T) {
	got := SentryProfile([]string{"/out"}, []string{`/src "a"`})
	want := `(version 1)
(deny default)
(allow file-read-metadata (literal "/out"))
(allow file-read-metadata (subpath "/src \"a\""))
`
	if got != want {
		t.Errorf("SentryProfile:\n%s\nwant:\n%s", got, want)
	}
}

