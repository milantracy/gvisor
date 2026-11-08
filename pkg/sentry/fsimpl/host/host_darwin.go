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
	"gvisor.dev/gvisor/pkg/sentry/vfs"
)

// utimeOmit is UTIME_OMIT from <sys/stat.h> on darwin.
const utimeOmit = -2

// hostFIONREAD is FIONREAD from <sys/filio.h> on darwin, _IOR('f', 127, int).
const hostFIONREAD = 0x4004667f

// fdatasync calls fsync(2), which also writes the data and more.
func fdatasync(fd int) error {
	return unix.Fsync(fd)
}

// isPollable returns true if fd reports readiness through kqueue in the way
// that epoll does on Linux. kqueue accepts regular files and directories, but
// epoll does not, so they are not pollable.
func isPollable(fd int) bool {
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return false
	}
	switch uint32(st.Mode) & unix.S_IFMT {
	case unix.S_IFREG, unix.S_IFDIR:
		return false
	}
	kq, err := unix.Kqueue()
	if err != nil {
		return false
	}
	defer unix.Close(kq)
	changes := []unix.Kevent_t{{
		Ident:  uint64(fd),
		Filter: unix.EVFILT_READ,
		Flags:  unix.EV_ADD,
	}}
	_, err = unix.Kevent(kq, changes, nil, nil)
	return err == nil
}

// darwinOpenFlags maps darwin file status flags to Linux flags. The access
// mode has the same values.
var darwinOpenFlags = []struct {
	host  int
	linux uint32
}{
	{unix.O_NONBLOCK, linux.O_NONBLOCK},
	{unix.O_APPEND, linux.O_APPEND},
	{unix.O_SYNC, linux.O_SYNC},
	{unix.O_DSYNC, linux.O_DSYNC},
}

// hostOpenFlags returns the file status flags of fd as Linux flags.
func hostOpenFlags(fd int) (uint32, error) {
	flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
	if err != nil {
		return 0, err
	}
	l := uint32(flags & unix.O_ACCMODE)
	for _, f := range darwinOpenFlags {
		if flags&f.host != 0 {
			l |= f.linux
		}
	}
	return l, nil
}

// fstat fills s from fstat(2) of fd.
func fstat(fd int, s *hostStat) error {
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return err
	}
	*s = hostStat{
		Dev:     uint64(st.Dev),
		Mode:    uint32(st.Mode),
		Nlink:   uint32(st.Nlink),
		UID:     st.Uid,
		GID:     st.Gid,
		Size:    st.Size,
		Blocks:  st.Blocks,
		Blksize: uint32(st.Blksize),
		Atime:   timespecToStatxTimestamp(st.Atim),
		Mtime:   timespecToStatxTimestamp(st.Mtim),
		Ctime:   timespecToStatxTimestamp(st.Ctim),
	}
	return nil
}

// fallocate returns EOPNOTSUPP: darwin has no fallocate(2). Linux returns
// the same for a file system that does not support it.
func fallocate(fd int, mode uint32, offset, length int64) error {
	return unix.EOPNOTSUPP
}

// hostStatx returns the fstat(2) of the host file: darwin has no statx(2).
func (i *inode) hostStatx(opts vfs.StatOptions) (linux.Statx, error) {
	return i.statxFromStat(i.devMinor)
}

// isHostEventFdDevice returns false: darwin has no eventfd.
func isHostEventFdDevice(dev uint64) bool {
	return false
}
