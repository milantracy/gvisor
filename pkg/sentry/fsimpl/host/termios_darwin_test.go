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

package host

import (
	"testing"

	"golang.org/x/sys/unix"
	"gvisor.dev/gvisor/pkg/abi/linux"
)

func TestTermiosRoundTrip(t *testing.T) {
	want := linux.DefaultReplicaTermios
	// VSWTC has no darwin equivalent.
	want.ControlCharacters[linux.VSWTC] = 0
	want.ControlFlags = want.ControlFlags&^linux.CBAUD | linux.B9600
	want.InputSpeed = 9600
	want.OutputSpeed = 9600

	var d unix.Termios
	toDarwinTermios(&want, &d, false)
	if got := toLinuxTermios(&d); got != want {
		t.Errorf("round trip of %+v: got %+v", want, got)
	}
}

func TestTermiosValues(t *testing.T) {
	l := linux.KernelTermios{
		InputFlags:   linux.ICRNL | linux.IUTF8,
		OutputFlags:  linux.OPOST | linux.ONLCR | linux.TAB3,
		ControlFlags: linux.CS7 | linux.CREAD | linux.B38400,
		LocalFlags:   linux.ISIG | linux.ICANON | linux.ECHO | linux.IEXTEN,
	}
	l.ControlCharacters[linux.VINTR] = 3
	l.ControlCharacters[linux.VMIN] = 1
	l.ControlCharacters[linux.VEOF] = 4

	// Bits that no Linux flag maps to are kept.
	d := unix.Termios{Lflag: unix.NOKERNINFO}
	toDarwinTermios(&l, &d, false)
	want := unix.Termios{
		Iflag:  unix.ICRNL | unix.IUTF8,
		Oflag:  unix.OPOST | unix.ONLCR | unix.TAB3,
		Cflag:  unix.CS7 | unix.CREAD,
		Lflag:  unix.ISIG | unix.ICANON | unix.ECHO | unix.IEXTEN | unix.NOKERNINFO,
		Ispeed: 38400,
		Ospeed: 38400,
	}
	want.Cc[unix.VINTR] = 3
	want.Cc[unix.VMIN] = 1
	want.Cc[unix.VEOF] = 4
	if d != want {
		t.Errorf("toDarwinTermios(%+v): got %+v, want %+v", l, d, want)
	}
}

func TestTermios2Speeds(t *testing.T) {
	l := linux.KernelTermios{
		ControlFlags: linux.CS8 | linux.BOTHER,
		InputSpeed:   12345,
		OutputSpeed:  54321,
	}
	var d unix.Termios
	toDarwinTermios(&l, &d, true)
	if d.Ispeed != 12345 || d.Ospeed != 54321 {
		t.Errorf("speeds: got %d/%d, want 12345/54321", d.Ispeed, d.Ospeed)
	}
	if got := toLinuxTermios(&d); got.ControlFlags&linux.CBAUD != linux.BOTHER {
		t.Errorf("CBAUD of speed 54321: got %#o, want BOTHER", got.ControlFlags&linux.CBAUD)
	}
}

func TestSetTermiosRequest(t *testing.T) {
	for _, tc := range []struct {
		req  uint64
		want uint
	}{
		{linux.TCSETS, unix.TIOCSETA},
		{linux.TCSETSW, unix.TIOCSETAW},
		{linux.TCSETSF, unix.TIOCSETAF},
		{linux.TCSETS2, unix.TIOCSETA},
		{linux.TCSETSW2, unix.TIOCSETAW},
		{linux.TCSETSF2, unix.TIOCSETAF},
	} {
		if got := setTermiosRequest(tc.req); got != tc.want {
			t.Errorf("setTermiosRequest(%#x): got %#x, want %#x", tc.req, got, tc.want)
		}
	}
}
