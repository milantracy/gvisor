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
	"golang.org/x/sys/unix"
	"gvisor.dev/gvisor/pkg/abi/linux"
)

// darwin has another termios layout, other flag values, other control
// character indices and other ioctl numbers than Linux. The functions below
// translate between the Linux termios of the application and the darwin
// termios of the host TTY. A set reads the host termios first and changes
// only what Linux can express, so darwin-only flags and control characters
// (such as VSTATUS) keep their values.

// flagPair is a termios flag (or a multi-bit field, with mask) in Linux and
// in darwin.
type flagPair struct {
	linux, darwin uint64
	// linuxMask and darwinMask are the masks of a multi-bit field, or 0 for a
	// one-bit flag.
	linuxMask, darwinMask uint64
}

var inputFlags = []flagPair{
	{linux: linux.IGNBRK, darwin: unix.IGNBRK},
	{linux: linux.BRKINT, darwin: unix.BRKINT},
	{linux: linux.IGNPAR, darwin: unix.IGNPAR},
	{linux: linux.PARMRK, darwin: unix.PARMRK},
	{linux: linux.INPCK, darwin: unix.INPCK},
	{linux: linux.ISTRIP, darwin: unix.ISTRIP},
	{linux: linux.INLCR, darwin: unix.INLCR},
	{linux: linux.IGNCR, darwin: unix.IGNCR},
	{linux: linux.ICRNL, darwin: unix.ICRNL},
	{linux: linux.IXON, darwin: unix.IXON},
	{linux: linux.IXANY, darwin: unix.IXANY},
	{linux: linux.IXOFF, darwin: unix.IXOFF},
	{linux: linux.IMAXBEL, darwin: unix.IMAXBEL},
	{linux: linux.IUTF8, darwin: unix.IUTF8},
}

var outputFlags = []flagPair{
	{linux: linux.OPOST, darwin: unix.OPOST},
	{linux: linux.ONLCR, darwin: unix.ONLCR},
	{linux: linux.OCRNL, darwin: unix.OCRNL},
	{linux: linux.ONOCR, darwin: unix.ONOCR},
	{linux: linux.ONLRET, darwin: unix.ONLRET},
	{linux: linux.OFILL, darwin: unix.OFILL},
	{linux: linux.OFDEL, darwin: unix.OFDEL},
	{linux: linux.NL1, darwin: unix.NL1, linuxMask: linux.NLDLY, darwinMask: unix.NLDLY},
	{linux: linux.CR1, darwin: unix.CR1, linuxMask: linux.CRDLY, darwinMask: unix.CRDLY},
	{linux: linux.CR2, darwin: unix.CR2, linuxMask: linux.CRDLY, darwinMask: unix.CRDLY},
	{linux: linux.CR3, darwin: unix.CR3, linuxMask: linux.CRDLY, darwinMask: unix.CRDLY},
	{linux: linux.TAB1, darwin: unix.TAB1, linuxMask: linux.TABDLY, darwinMask: unix.TABDLY},
	{linux: linux.TAB2, darwin: unix.TAB2, linuxMask: linux.TABDLY, darwinMask: unix.TABDLY},
	{linux: linux.TAB3, darwin: unix.TAB3, linuxMask: linux.TABDLY, darwinMask: unix.TABDLY},
	{linux: linux.BS1, darwin: unix.BS1, linuxMask: linux.BSDLY, darwinMask: unix.BSDLY},
	{linux: linux.VT1, darwin: unix.VT1, linuxMask: linux.VTDLY, darwinMask: unix.VTDLY},
	{linux: linux.FF1, darwin: unix.FF1, linuxMask: linux.FFDLY, darwinMask: unix.FFDLY},
}

var controlFlags = []flagPair{
	{linux: linux.CS6, darwin: unix.CS6, linuxMask: linux.CSIZE, darwinMask: unix.CSIZE},
	{linux: linux.CS7, darwin: unix.CS7, linuxMask: linux.CSIZE, darwinMask: unix.CSIZE},
	{linux: linux.CS8, darwin: unix.CS8, linuxMask: linux.CSIZE, darwinMask: unix.CSIZE},
	{linux: linux.CSTOPB, darwin: unix.CSTOPB},
	{linux: linux.CREAD, darwin: unix.CREAD},
	{linux: linux.PARENB, darwin: unix.PARENB},
	{linux: linux.PARODD, darwin: unix.PARODD},
	{linux: linux.HUPCL, darwin: unix.HUPCL},
	{linux: linux.CLOCAL, darwin: unix.CLOCAL},
	{linux: linux.CRTSCTS, darwin: unix.CRTSCTS},
}

var localFlags = []flagPair{
	{linux: linux.ISIG, darwin: unix.ISIG},
	{linux: linux.ICANON, darwin: unix.ICANON},
	{linux: linux.ECHO, darwin: unix.ECHO},
	{linux: linux.ECHOE, darwin: unix.ECHOE},
	{linux: linux.ECHOK, darwin: unix.ECHOK},
	{linux: linux.ECHONL, darwin: unix.ECHONL},
	{linux: linux.NOFLSH, darwin: unix.NOFLSH},
	{linux: linux.TOSTOP, darwin: unix.TOSTOP},
	{linux: linux.ECHOCTL, darwin: unix.ECHOCTL},
	{linux: linux.ECHOPRT, darwin: unix.ECHOPRT},
	{linux: linux.ECHOKE, darwin: unix.ECHOKE},
	{linux: linux.FLUSHO, darwin: unix.FLUSHO},
	{linux: linux.PENDIN, darwin: unix.PENDIN},
	{linux: linux.IEXTEN, darwin: unix.IEXTEN},
	{linux: linux.EXTPROC, darwin: unix.EXTPROC},
}

// controlChars maps Linux control character indices to darwin ones. VSWTC
// has no darwin equivalent.
var controlChars = []struct{ linux, darwin int }{
	{linux.VINTR, unix.VINTR},
	{linux.VQUIT, unix.VQUIT},
	{linux.VERASE, unix.VERASE},
	{linux.VKILL, unix.VKILL},
	{linux.VEOF, unix.VEOF},
	{linux.VTIME, unix.VTIME},
	{linux.VMIN, unix.VMIN},
	{linux.VSTART, unix.VSTART},
	{linux.VSTOP, unix.VSTOP},
	{linux.VSUSP, unix.VSUSP},
	{linux.VEOL, unix.VEOL},
	{linux.VREPRINT, unix.VREPRINT},
	{linux.VDISCARD, unix.VDISCARD},
	{linux.VWERASE, unix.VWERASE},
	{linux.VLNEXT, unix.VLNEXT},
	{linux.VEOL2, unix.VEOL2},
}

// baudRates maps the Linux CBAUD codes to speeds in bits per second. darwin
// stores the speed itself.
var baudRates = []struct {
	code  uint32
	speed uint64
}{
	{linux.B0, 0}, {linux.B50, 50}, {linux.B75, 75}, {linux.B110, 110},
	{linux.B134, 134}, {linux.B150, 150}, {linux.B200, 200}, {linux.B300, 300},
	{linux.B600, 600}, {linux.B1200, 1200}, {linux.B1800, 1800},
	{linux.B2400, 2400}, {linux.B4800, 4800}, {linux.B9600, 9600},
	{linux.B19200, 19200}, {linux.B38400, 38400}, {linux.B57600, 57600},
	{linux.B115200, 115200}, {linux.B230400, 230400},
}

// toLinuxFlags returns the Linux flags for the darwin flags d.
func toLinuxFlags(pairs []flagPair, d uint64) uint32 {
	var l uint64
	for _, p := range pairs {
		if p.darwinMask != 0 {
			if d&p.darwinMask == p.darwin {
				l |= p.linux
			}
		} else if d&p.darwin != 0 {
			l |= p.linux
		}
	}
	return uint32(l)
}

// toDarwinFlags returns the darwin flags d with the flags in pairs set as in
// the Linux flags l. Bits of d that pairs does not cover are kept.
func toDarwinFlags(pairs []flagPair, l uint32, d uint64) uint64 {
	for _, p := range pairs {
		if p.darwinMask != 0 {
			d &^= p.darwinMask
		} else {
			d &^= p.darwin
		}
	}
	for _, p := range pairs {
		if p.linuxMask != 0 {
			if uint64(l)&p.linuxMask == p.linux {
				d |= p.darwin
			}
		} else if uint64(l)&p.linux != 0 {
			d |= p.darwin
		}
	}
	return d
}

func baudCode(speed uint64) uint32 {
	for _, b := range baudRates {
		if b.speed == speed {
			return b.code
		}
	}
	return linux.BOTHER
}

func baudSpeed(code uint32) (uint64, bool) {
	for _, b := range baudRates {
		if b.code == code {
			return b.speed, true
		}
	}
	return 0, false
}

// toLinuxTermios returns the Linux termios of the darwin termios d.
func toLinuxTermios(d *unix.Termios) linux.KernelTermios {
	t := linux.KernelTermios{
		InputFlags:   toLinuxFlags(inputFlags, d.Iflag),
		OutputFlags:  toLinuxFlags(outputFlags, d.Oflag),
		ControlFlags: toLinuxFlags(controlFlags, d.Cflag) | baudCode(d.Ospeed),
		LocalFlags:   toLinuxFlags(localFlags, d.Lflag),
		InputSpeed:   uint32(d.Ispeed),
		OutputSpeed:  uint32(d.Ospeed),
	}
	for _, c := range controlChars {
		t.ControlCharacters[c.linux] = d.Cc[c.darwin]
	}
	return t
}

// toDarwinTermios changes the darwin termios d to match the Linux termios t.
// If useSpeeds is true, the speeds are those of t (struct termios2), else
// they are those of the CBAUD code of t.
func toDarwinTermios(t *linux.KernelTermios, d *unix.Termios, useSpeeds bool) {
	d.Iflag = toDarwinFlags(inputFlags, t.InputFlags, d.Iflag)
	d.Oflag = toDarwinFlags(outputFlags, t.OutputFlags, d.Oflag)
	d.Cflag = toDarwinFlags(controlFlags, t.ControlFlags, d.Cflag)
	d.Lflag = toDarwinFlags(localFlags, t.LocalFlags, d.Lflag)
	for _, c := range controlChars {
		d.Cc[c.darwin] = t.ControlCharacters[c.linux]
	}
	if useSpeeds {
		d.Ispeed = uint64(t.InputSpeed)
		d.Ospeed = uint64(t.OutputSpeed)
	} else if speed, ok := baudSpeed(t.ControlFlags & linux.CBAUD); ok {
		d.Ispeed = speed
		d.Ospeed = speed
	}
}

// setTermiosRequest returns the darwin ioctl for the Linux TCSETS* or
// TCSETS*2 request req.
func setTermiosRequest(req uint64) uint {
	switch req {
	case linux.TCSETSW, linux.TCSETSW2:
		return unix.TIOCSETAW
	case linux.TCSETSF, linux.TCSETSF2:
		return unix.TIOCSETAF
	default:
		return unix.TIOCSETA
	}
}

func ioctlGetTermios(fd int) (*linux.Termios, error) {
	d, err := unix.IoctlGetTermios(fd, unix.TIOCGETA)
	if err != nil {
		return nil, err
	}
	kt := toLinuxTermios(d)
	t := kt.ToTermios()
	return &t, nil
}

// IsTTY returns whether the given file descriptor is a terminal.
func IsTTY(fd int) bool {
	_, err := unix.IoctlGetTermios(fd, unix.TIOCGETA)
	return err == nil
}

func ioctlSetTermios(fd int, req uint64, t *linux.Termios) error {
	d, err := unix.IoctlGetTermios(fd, unix.TIOCGETA)
	if err != nil {
		return err
	}
	var kt linux.KernelTermios
	kt.FromTermios(*t)
	toDarwinTermios(&kt, d, false)
	return unix.IoctlSetTermios(fd, setTermiosRequest(req), d)
}

func ioctlGetTermios2(fd int) (*linux.KernelTermios, error) {
	d, err := unix.IoctlGetTermios(fd, unix.TIOCGETA)
	if err != nil {
		return nil, err
	}
	t := toLinuxTermios(d)
	return &t, nil
}

func ioctlSetTermios2(fd int, req uint64, t *linux.KernelTermios) error {
	d, err := unix.IoctlGetTermios(fd, unix.TIOCGETA)
	if err != nil {
		return err
	}
	toDarwinTermios(t, d, t.ControlFlags&linux.CBAUD == linux.BOTHER)
	return unix.IoctlSetTermios(fd, setTermiosRequest(req), d)
}

func ioctlGetWinsize(fd int) (*linux.Winsize, error) {
	w, err := unix.IoctlGetWinsize(fd, unix.TIOCGWINSZ)
	if err != nil {
		return nil, err
	}
	return &linux.Winsize{Row: w.Row, Col: w.Col, Xpixel: w.Xpixel, Ypixel: w.Ypixel}, nil
}

func ioctlSetWinsize(fd int, w *linux.Winsize) error {
	return unix.IoctlSetWinsize(fd, unix.TIOCSWINSZ, &unix.Winsize{Row: w.Row, Col: w.Col, Xpixel: w.Xpixel, Ypixel: w.Ypixel})
}
