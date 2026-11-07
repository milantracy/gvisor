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

// Package host provides a filesystem implementation for host files imported as
// file descriptors.
//go:build linux

package host

import (
	"golang.org/x/sys/unix"
	"gvisor.dev/gvisor/pkg/abi/linux"
	"gvisor.dev/gvisor/pkg/errors/linuxerr"
	"gvisor.dev/gvisor/pkg/log"
	"gvisor.dev/gvisor/pkg/sentry/vfs"
	"gvisor.dev/gvisor/pkg/sync"
)

// utimeOmit is UTIME_OMIT.
const utimeOmit = unix.UTIME_OMIT

// hostFIONREAD is the host FIONREAD ioctl.
const hostFIONREAD = linux.FIONREAD

func fdatasync(fd int) error {
	return unix.Fdatasync(fd)
}

// isPollable returns true if fd supports epoll.
func isPollable(fd int) bool {
	epollfd, err := unix.EpollCreate1(0)
	if err != nil {
		// This shouldn't happen. If it does, just say file doesn't support epoll.
		return false
	}
	defer unix.Close(epollfd)

	event := unix.EpollEvent{
		Fd:     int32(fd),
		Events: unix.EPOLLIN,
	}
	err = unix.EpollCtl(epollfd, unix.EPOLL_CTL_ADD, fd, &event)
	return err == nil
}

// hostOpenFlags returns the file status flags of fd. Linux and host flags are
// the same.
func hostOpenFlags(fd int) (uint32, error) {
	flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
	return uint32(flags), err
}

// fstat fills s from fstat(2) of fd.
func fstat(fd int, s *hostStat) error {
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return err
	}
	*s = hostStat{
		Dev:     st.Dev,
		Mode:    st.Mode,
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

func fallocate(fd int, mode uint32, offset, length int64) error {
	return unix.Fallocate(fd, mode, offset, length)
}

func unixToLinuxStatxTimestamp(ts unix.StatxTimestamp) linux.StatxTimestamp {
	return linux.StatxTimestamp{Sec: ts.Sec, Nsec: ts.Nsec}
}

// hostStatx returns the statx(2) of the host file.
func (i *inode) hostStatx(opts vfs.StatOptions) (linux.Statx, error) {
	// Limit our host call only to known flags.
	mask := opts.Mask & linux.STATX_ALL
	var s unix.Statx_t
	err := unix.Statx(i.hostFD, "", int(unix.AT_EMPTY_PATH|opts.Sync), int(mask), &s)
	if linuxerr.Equals(linuxerr.ENOSYS, err) {
		// Fallback to fstat(2), if statx(2) is not supported on the host.
		//
		// TODO(b/151263641): Remove fallback.
		return i.statxFromStat(i.devMinor)
	}
	if err != nil {
		return linux.Statx{}, err
	}

	// Unconditionally fill blksize, attributes, and device numbers, as
	// indicated by /include/uapi/linux/stat.h. Inode number is always
	// available, since we use our own rather than the host's.
	ls := linux.Statx{
		Mask:           linux.STATX_INO,
		Blksize:        s.Blksize,
		Attributes:     s.Attributes,
		Ino:            i.ino,
		AttributesMask: s.Attributes_mask,
		DevMajor:       linux.UNNAMED_MAJOR,
		DevMinor:       i.devMinor,
	}
	// Clear STATX_ATTR_MOUNT_ROOT passed through from the host; the sentry VFS
	// sets it from its own mounts.
	ls.Attributes &^= linux.STATX_ATTR_MOUNT_ROOT
	ls.AttributesMask &^= linux.STATX_ATTR_MOUNT_ROOT

	// Copy other fields that were returned by the host. RdevMajor/RdevMinor
	// are never copied (and therefore left as zero), so as not to expose host
	// device numbers.
	ls.Mask |= s.Mask & linux.STATX_ALL
	if s.Mask&linux.STATX_TYPE != 0 {
		if i.virtualOwner.enabled {
			ls.Mode |= uint16(i.virtualOwner.atomicMode()) & linux.S_IFMT
		} else {
			ls.Mode |= s.Mode & linux.S_IFMT
		}
	}
	if s.Mask&linux.STATX_MODE != 0 {
		if i.virtualOwner.enabled {
			ls.Mode |= uint16(i.virtualOwner.atomicMode()) &^ linux.S_IFMT
		} else {
			ls.Mode |= s.Mode &^ linux.S_IFMT
		}
	}
	if s.Mask&linux.STATX_NLINK != 0 {
		ls.Nlink = s.Nlink
	}
	if s.Mask&linux.STATX_UID != 0 {
		if i.virtualOwner.enabled {
			ls.UID = i.virtualOwner.atomicUID()
		} else {
			ls.UID = s.Uid
		}
	}
	if s.Mask&linux.STATX_GID != 0 {
		if i.virtualOwner.enabled {
			ls.GID = i.virtualOwner.atomicGID()
		} else {
			ls.GID = s.Gid
		}
	}
	if s.Mask&linux.STATX_ATIME != 0 {
		ls.Atime = unixToLinuxStatxTimestamp(s.Atime)
	}
	if s.Mask&linux.STATX_BTIME != 0 {
		ls.Btime = unixToLinuxStatxTimestamp(s.Btime)
	}
	if s.Mask&linux.STATX_CTIME != 0 {
		ls.Ctime = unixToLinuxStatxTimestamp(s.Ctime)
	}
	if s.Mask&linux.STATX_MTIME != 0 {
		ls.Mtime = unixToLinuxStatxTimestamp(s.Mtime)
	}
	if s.Mask&linux.STATX_SIZE != 0 {
		ls.Size = s.Size
	}
	if s.Mask&linux.STATX_BLOCKS != 0 {
		ls.Blocks = s.Blocks
	}

	return ls, nil
}

// hostEventFdDevice is the host device that host event fds are associated
// with. It is calculated once lazily.
var hostEventFdDevice uint64
var hostEventFdDeviceOnce sync.Once

// isHostEventFdDevice initializes hostEventFdDevice and compares it to the
// given host device id.
func isHostEventFdDevice(dev uint64) bool {
	hostEventFdDeviceOnce.Do(func() {
		efd, _, err := unix.RawSyscall(unix.SYS_EVENTFD2, 0, 0, 0)
		if err != 0 {
			log.Warningf("failed to create dummy eventfd: %v. Importing eventfds will fail", error(err))
			return
		}
		defer unix.Close(int(efd))
		var stat unix.Stat_t
		if err := unix.Fstat(int(efd), &stat); err != nil {
			log.Warningf("failed to stat dummy eventfd: %v. Importing eventfds will fail", error(err))
			return
		}
		hostEventFdDevice = stat.Dev
	})
	return dev == hostEventFdDevice
}
