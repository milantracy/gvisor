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

package fs

import (
	"golang.org/x/sys/unix"
	"gvisor.dev/gvisor/pkg/abi/linux"
	"gvisor.dev/gvisor/pkg/lisafs"
)

// LINT.IfChange

// darwin has no O_PATH. A control FD is a real FD:
//   - O_EVTONLY opens a file without asking for read or write access to it.
//     It still needs read permission.
//   - O_SYMLINK opens a symlink itself instead of its target. With
//     O_NOFOLLOW, open(2) fails on a symlink, so pathOpenFlags leaves out
//     O_NOFOLLOW. Names from lisafs are single path components, so no other
//     symlink is followed.
//   - Sockets cannot be opened, so the gofer cannot walk to them.
//
// darwin has no /proc/self/fd either. A file is opened again by its path,
// with no symlinks allowed in the path, and the new FD must refer to the
// same file as the control FD.
const (
	pathOpenFlags = unix.O_EVTONLY | unix.O_SYMLINK | unix.O_NONBLOCK | unix.O_CLOEXEC

	// reopenDeletedSupported is false because a deleted file has no path to
	// open it again with.
	reopenDeletedSupported = false

	// utimeOmit is UTIME_OMIT from <sys/stat.h>.
	utimeOmit = -2

	// xattrCreate and xattrReplace are from <sys/xattr.h>.
	xattrCreate  = 0x2
	xattrReplace = 0x4
)

// tryOpenFlags are the open flags that tryOpen tries in order.
var tryOpenFlags = []int{
	unix.O_RDONLY | unix.O_NONBLOCK | openFlags,
	pathOpenFlags,
}

// OpenProcSelfFD does nothing on darwin, which has no /proc. Files are
// opened again by path.
func OpenProcSelfFD(path string) error {
	return nil
}

// reopen opens the file of fd again with flags.
//
// Callers hold the server's rename mutex for reading, so the path of fd does
// not change.
func (fd *controlFDLisa) reopen(flags int) (int, error) {
	return reopenPath(fd.hostFD, fd.Node().FilePath(), flags) // +checklocksignore
}

// reopenChild opens childHostFD, the file name in parent, again with flags.
func reopenChild(childHostFD int, parent *controlFDLisa, name string, flags int) (int, error) {
	newFD, err := unix.Openat(parent.hostFD, name, flags|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, err
	}
	if err := checkSameFile(childHostFD, newFD); err != nil {
		unix.Close(newFD)
		return -1, err
	}
	return newFD, nil
}

// reopenPath opens path with flags, and checks that it is the file of hostFD.
func reopenPath(hostFD int, path string, flags int) (int, error) {
	newFD, err := unix.Open(path, (flags&^unix.O_NOFOLLOW)|unix.O_NOFOLLOW_ANY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, err
	}
	if err := checkSameFile(hostFD, newFD); err != nil {
		unix.Close(newFD)
		return -1, err
	}
	return newFD, nil
}

// checkSameFile returns ESTALE if a and b are not FDs of the same file.
func checkSameFile(a, b int) error {
	var sa, sb unix.Stat_t
	if err := unix.Fstat(a, &sa); err != nil {
		return err
	}
	if err := unix.Fstat(b, &sb); err != nil {
		return err
	}
	if sa.Dev != sb.Dev || sa.Ino != sb.Ino {
		return unix.ESTALE
	}
	return nil
}

// openDirNoSymlinks opens the directory at dirPath without following
// symlinks in any component, as a defense-in-depth measure that prevents
// attackers from redirecting it to a different location.
func openDirNoSymlinks(dirPath string) (int, error) {
	return unix.Open(dirPath, unix.O_EVTONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW_ANY|unix.O_CLOEXEC, 0)
}

// openPath opens name in dirFD with pathOpenFlags.
func openPath(dirFD int, name string) (int, error) {
	return unix.Openat(dirFD, name, pathOpenFlags, 0)
}

// fchmodPathFD fails: only sockets use it, and darwin control FDs are never
// sockets.
func fchmodPathFD(hostFD int, mode uint32) error {
	return unix.EOPNOTSUPP
}

// utimensat sets the access and modification times of name in dirFD, or of
// dirFD if name is empty.
func utimensat(dirFD int, name string, times [2]unix.Timespec, flags int) error {
	if name == "" {
		return futimens(dirFD, &times)
	}
	return unix.UtimesNanoAt(dirFD, name, times[:], flags)
}

// fchownFD changes the owners of the file of hostFD. A symlink FD (opened
// with O_SYMLINK) refers to the symlink itself.
func fchownFD(hostFD, uid, gid int) error {
	return unix.Fchown(hostFD, uid, gid)
}

// mknodat creates the file name in dirFD with mode. Only regular files are
// supported: darwin needs root for character devices, including the 0:0
// overlayfs whiteout.
func mknodat(dirFD int, name string, mode linux.FileMode) error {
	if mode.FileType() != linux.ModeRegular {
		return unix.EPERM
	}
	fd, err := unix.Openat(dirFD, name, unix.O_CREAT|unix.O_EXCL|unix.O_WRONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, uint32(mode.Permissions()))
	if err != nil {
		return err
	}
	return unix.Close(fd)
}

// readlinkFD reads the target of the symlink of hostFD into buf.
func readlinkFD(hostFD int, buf []byte) (int, error) {
	return freadlink(hostFD, buf)
}

// hostOpenFlags returns the host open flags for the Linux open flags that
// lisafs allows (the access mode and O_TRUNC).
func hostOpenFlags(flags uint32) int {
	hostFlags := int(flags & linux.O_ACCMODE)
	if flags&linux.O_TRUNC != 0 {
		hostFlags |= unix.O_TRUNC
	}
	return hostFlags
}

// hostUnlinkFlags returns the host unlinkat(2) flags for the Linux flags.
func hostUnlinkFlags(flags uint32) int {
	if flags&linux.AT_REMOVEDIR != 0 {
		return unix.AT_REMOVEDIR
	}
	return 0
}

// xattrFlags returns the host flags for the Linux setxattr(2) flags.
func xattrFlags(flags uint32) int {
	var hostFlags int
	if flags&linux.XATTR_CREATE != 0 {
		hostFlags |= xattrCreate
	}
	if flags&linux.XATTR_REPLACE != 0 {
		hostFlags |= xattrReplace
	}
	return hostFlags
}

// getdents reads directory entries of hostFD into buf, in the format that
// fsutil.ParseDirents parses.
func getdents(hostFD int, buf []byte) (int, error) {
	var base uintptr
	return unix.Getdirentries(hostFD, buf, &base)
}

// statDevAt returns the device number of name in dirFD.
func statDevAt(dirFD int, name string) (major, minor uint32, err error) {
	var stat unix.Stat_t
	if err := unix.Fstatat(dirFD, name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return 0, 0, err
	}
	dev := uint64(uint32(stat.Dev))
	return unix.Major(dev), unix.Minor(dev), nil
}

// acceptNonblock accepts a connection on fd, and returns a non-blocking,
// close-on-exec FD for it.
func acceptNonblock(fd int) (int, error) {
	nfd, _, err := unix.Accept(fd)
	if err != nil {
		return -1, err
	}
	unix.CloseOnExec(nfd)
	if err := unix.SetNonblock(nfd, true); err != nil {
		unix.Close(nfd)
		return -1, err
	}
	return nfd, nil
}

// fstatfsTo returns the file system statistics of hostFD.
func fstatfsTo(hostFD int) (lisafs.StatFS, error) {
	var s unix.Statfs_t
	if err := unix.Fstatfs(hostFD, &s); err != nil {
		return lisafs.StatFS{}, err
	}
	return lisafs.StatFS{
		// darwin file system types have no Linux magic number.
		Type:            0,
		BlockSize:       int64(s.Bsize),
		Blocks:          s.Blocks,
		BlocksFree:      s.Bfree,
		BlocksAvailable: s.Bavail,
		Files:           s.Files,
		FilesFree:       s.Ffree,
		NameLength:      linux.NAME_MAX,
	}, nil
}

func fstatTo(hostFD int) (lisafs.Statx, error) {
	var stat unix.Stat_t
	if err := unix.Fstat(hostFD, &stat); err != nil {
		return lisafs.Statx{}, err
	}
	dev, rdev := uint64(uint32(stat.Dev)), uint64(uint32(stat.Rdev))
	return lisafs.Statx{
		Mask:      linux.STATX_BASIC_STATS | linux.STATX_BTIME,
		Mode:      stat.Mode,
		DevMinor:  unix.Minor(dev),
		DevMajor:  unix.Major(dev),
		Ino:       stat.Ino,
		Nlink:     uint32(stat.Nlink),
		UID:       stat.Uid,
		GID:       stat.Gid,
		RdevMinor: unix.Minor(rdev),
		RdevMajor: unix.Major(rdev),
		Size:      uint64(stat.Size),
		Blksize:   uint32(stat.Blksize),
		Blocks:    uint64(stat.Blocks),
		Atime:     lisafs.StatxTimestamp{Sec: stat.Atim.Sec, Nsec: uint32(stat.Atim.Nsec)},
		Btime:     lisafs.StatxTimestamp{Sec: stat.Btim.Sec, Nsec: uint32(stat.Btim.Nsec)},
		Mtime:     lisafs.StatxTimestamp{Sec: stat.Mtim.Sec, Nsec: uint32(stat.Mtim.Nsec)},
		Ctime:     lisafs.StatxTimestamp{Sec: stat.Ctim.Sec, Nsec: uint32(stat.Ctim.Nsec)},
	}, nil
}

// ConnectWithCreds implements lisafs.ControlFDImpl.ConnectWithCreds. darwin
// has no per-thread credentials, so the gofer cannot connect as another user.
func (fd *controlFDLisa) ConnectWithCreds(sockType uint32, uid lisafs.UID, gid lisafs.GID) (int, error) {
	return -1, unix.EOPNOTSUPP
}

// ConnectWithGroups implements lisafs.ControlFDImpl.ConnectWithGroups.
func (fd *controlFDLisa) ConnectWithGroups(sockType uint32, uid lisafs.UID, gid lisafs.GID, groups []lisafs.GID) (int, error) {
	return -1, unix.EOPNOTSUPP
}

// LINT.ThenChange(lisafs_linux.go)
