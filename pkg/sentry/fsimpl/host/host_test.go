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

package host

import (
	"os"
	"runtime"
	"testing"

	"golang.org/x/sys/unix"
	"gvisor.dev/gvisor/pkg/abi/linux"
)

func TestHostOpenFlags(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "flags")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	fd, err := unix.Open(f.Name(), unix.O_WRONLY|unix.O_APPEND|unix.O_NONBLOCK, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	got, err := hostOpenFlags(fd)
	if err != nil {
		t.Fatal(err)
	}
	want := uint32(linux.O_WRONLY | linux.O_APPEND | linux.O_NONBLOCK)
	if mask := uint32(linux.O_ACCMODE | linux.O_APPEND | linux.O_NONBLOCK | linux.O_SYNC | linux.O_DSYNC); got&mask != want {
		t.Errorf("hostOpenFlags: got %#x, want %#x", got&mask, want)
	}
}

func TestIsPollable(t *testing.T) {
	var p [2]int
	if err := unix.Pipe(p[:]); err != nil {
		t.Fatal(err)
	}
	defer unix.Close(p[0])
	defer unix.Close(p[1])
	if !isPollable(p[0]) {
		t.Errorf("pipe is not pollable")
	}
	if runtime.GOOS != "darwin" {
		// On Linux, a regular file is pollable if its filesystem implements
		// poll.
		return
	}
	f, err := os.CreateTemp(t.TempDir(), "regular")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if isPollable(int(f.Fd())) {
		t.Errorf("regular file is pollable")
	}
}

func TestFstat(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "stat")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.Write(make([]byte, 100)); err != nil {
		t.Fatal(err)
	}
	var s hostStat
	if err := fstat(int(f.Fd()), &s); err != nil {
		t.Fatal(err)
	}
	if s.Size != 100 {
		t.Errorf("Size: got %d, want 100", s.Size)
	}
	if s.Mode&linux.S_IFMT != linux.S_IFREG {
		t.Errorf("Mode: got %#o, want a regular file", s.Mode)
	}
	if s.UID != uint32(os.Getuid()) {
		t.Errorf("UID: got %d, want %d", s.UID, os.Getuid())
	}
	if s.Mtime.Sec == 0 {
		t.Errorf("Mtime is zero")
	}
}
