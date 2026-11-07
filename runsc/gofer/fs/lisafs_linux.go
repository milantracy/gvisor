// Copyright 2021 The gVisor Authors.
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

//go:build linux

package fs

import (
	"fmt"
	"runtime"
	"slices"
	"strconv"

	"golang.org/x/sys/unix"
	"gvisor.dev/gvisor/pkg/abi/linux"
	rwfd "gvisor.dev/gvisor/pkg/fd"
	"gvisor.dev/gvisor/pkg/fsutil"
	"gvisor.dev/gvisor/pkg/lisafs"
	"gvisor.dev/gvisor/pkg/log"
)

// LINT.IfChange

const (
	// pathOpenFlags open a file without reading it, for symlinks and sockets.
	pathOpenFlags = unix.O_PATH | openFlags

	// reopenDeletedSupported is true because reopen works through
	// /proc/self/fd, also for deleted files.
	reopenDeletedSupported = true

	utimeOmit = unix.UTIME_OMIT
)

// tryOpenFlags are the open flags that tryOpen tries in order.
var tryOpenFlags = []int{
	unix.O_RDONLY | unix.O_NONBLOCK | openFlags,
	pathOpenFlags,
}

var procSelfFD *rwfd.FD

// OpenProcSelfFD opens the /proc/self/fd directory, which will be used to
// reopen file descriptors.
func OpenProcSelfFD(path string) error {
	d, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY, 0)
	if err != nil {
		return fmt.Errorf("error opening /proc/self/fd: %v", err)
	}
	procSelfFD = rwfd.New(d)
	return nil
}

// reopen opens the file of fd again with flags.
func (fd *controlFDLisa) reopen(flags int) (int, error) {
	return unix.Openat(int(procSelfFD.FD()), strconv.Itoa(fd.hostFD), flags&^unix.O_NOFOLLOW, 0)
}

// reopenChild opens childHostFD, the file name in parent, again with flags.
func reopenChild(childHostFD int, parent *controlFDLisa, name string, flags int) (int, error) {
	return unix.Openat(int(procSelfFD.FD()), strconv.Itoa(childHostFD), flags&^unix.O_NOFOLLOW, 0)
}

// openDirNoSymlinks opens the directory at dirPath without following
// symlinks in any component, as a defense-in-depth measure that prevents
// attackers from redirecting it to a different location.
func openDirNoSymlinks(dirPath string) (int, error) {
	return unix.Openat2(unix.AT_FDCWD, dirPath, &unix.OpenHow{
		Flags:   uint64(openFlags | unix.O_PATH),
		Resolve: unix.RESOLVE_NO_SYMLINKS,
	})
}

// openPath opens name in dirFD with pathOpenFlags.
func openPath(dirFD int, name string) (int, error) {
	return unix.Openat(dirFD, name, pathOpenFlags, 0)
}

// fchmodPathFD changes the mode of the file of hostFD, which was opened with
// O_PATH. fchmod(2) fails with EBADF for O_PATH FDs, so it uses fchmodat(2)
// on /proc/self/fd.
func fchmodPathFD(hostFD int, mode uint32) error {
	return unix.Fchmodat(int(procSelfFD.FD()), strconv.Itoa(hostFD), mode, 0 /* flags */)
}

// utimensat sets the access and modification times of name in dirFD, or of
// dirFD if name is empty.
func utimensat(dirFD int, name string, times [2]unix.Timespec, flags int) error {
	return fsutil.Utimensat(dirFD, name, times, flags)
}

// fchownFD changes the owners of the file of hostFD.
func fchownFD(hostFD, uid, gid int) error {
	return unix.Fchownat(hostFD, "", uid, gid, unix.AT_EMPTY_PATH|unix.AT_SYMLINK_NOFOLLOW)
}

// mknodat creates the file name in dirFD with mode.
func mknodat(dirFD int, name string, mode linux.FileMode) error {
	return unix.Mknodat(dirFD, name, uint32(mode), 0)
}

// readlinkFD reads the target of the symlink of hostFD into buf.
func readlinkFD(hostFD int, buf []byte) (int, error) {
	return unix.Readlinkat(hostFD, "", buf)
}

func renameAt(oldDirFD int, oldName string, newDirFD int, newName string) error {
	return fsutil.RenameAt(oldDirFD, oldName, newDirFD, newName)
}

func renameAt2(oldDirFD int, oldName string, newDirFD int, newName string, flags uint32) error {
	return fsutil.RenameAt2(oldDirFD, oldName, newDirFD, newName, flags)
}

// hostOpenFlags returns the host open flags for the Linux open flags that
// lisafs allows (the access mode and O_TRUNC).
func hostOpenFlags(flags uint32) int {
	return int(flags)
}

// hostUnlinkFlags returns the host unlinkat(2) flags for the Linux flags.
func hostUnlinkFlags(flags uint32) int {
	return int(flags)
}

// xattrFlags returns the host flags for the Linux setxattr(2) flags.
func xattrFlags(flags uint32) int {
	return int(flags)
}

func fallocate(hostFD int, mode, off, length uint64) error {
	return unix.Fallocate(hostFD, uint32(mode), int64(off), int64(length))
}

// getdents reads directory entries of hostFD into buf, in the format that
// fsutil.ParseDirents parses.
func getdents(hostFD int, buf []byte) (int, error) {
	return unix.Getdents(hostFD, buf)
}

// statDevAt returns the device number of name in dirFD.
func statDevAt(dirFD int, name string) (major, minor uint32, err error) {
	stat, err := fsutil.StatAt(dirFD, name)
	if err != nil {
		return 0, 0, err
	}
	return unix.Major(stat.Dev), unix.Minor(stat.Dev), nil
}

// acceptNonblock accepts a connection on fd, and returns a non-blocking,
// close-on-exec FD for it.
func acceptNonblock(fd int) (int, error) {
	nfd, _, err := unix.Accept4(fd, unix.O_NONBLOCK|unix.O_CLOEXEC)
	return nfd, err
}

// fstatfsTo returns the file system statistics of hostFD.
func fstatfsTo(hostFD int) (lisafs.StatFS, error) {
	var s unix.Statfs_t
	if err := unix.Fstatfs(hostFD, &s); err != nil {
		return lisafs.StatFS{}, err
	}

	return lisafs.StatFS{
		Type:            uint64(s.Type),
		BlockSize:       s.Bsize,
		Blocks:          s.Blocks,
		BlocksFree:      s.Bfree,
		BlocksAvailable: s.Bavail,
		Files:           s.Files,
		FilesFree:       s.Ffree,
		NameLength:      uint64(s.Namelen),
	}, nil
}

func fstatTo(hostFD int) (lisafs.Statx, error) {
	var stat unix.Statx_t
	if err := unix.Statx(hostFD, "", unix.AT_EMPTY_PATH, linux.STATX_BASIC_STATS|linux.STATX_BTIME, &stat); err != nil {
		return lisafs.Statx{}, err
	}
	return lisafs.Statx{
		Mask:      stat.Mask,
		Mode:      stat.Mode,
		DevMinor:  stat.Dev_minor,
		DevMajor:  stat.Dev_major,
		Ino:       stat.Ino,
		Nlink:     stat.Nlink,
		UID:       stat.Uid,
		GID:       stat.Gid,
		RdevMinor: stat.Rdev_minor,
		RdevMajor: stat.Rdev_major,
		Size:      stat.Size,
		Blksize:   stat.Blksize,
		Blocks:    stat.Blocks,
		Atime: lisafs.StatxTimestamp{
			Sec:  stat.Atime.Sec,
			Nsec: stat.Atime.Nsec,
		},
		Btime: lisafs.StatxTimestamp{
			Sec:  stat.Btime.Sec,
			Nsec: stat.Btime.Nsec,
		},
		Mtime: lisafs.StatxTimestamp{
			Sec:  stat.Mtime.Sec,
			Nsec: stat.Mtime.Nsec,
		},
		Ctime: lisafs.StatxTimestamp{
			Sec:  stat.Ctime.Sec,
			Nsec: stat.Ctime.Nsec,
		},
	}, nil
}

// ConnectWithCreds implements lisafs.ControlFDImpl.ConnectWithCreds.
func (fd *controlFDLisa) ConnectWithCreds(sockType uint32, uid lisafs.UID, gid lisafs.GID) (int, error) {
	impl := fd.Conn().Impl().(*connectionImpl)
	if !impl.config.HostUDS.AllowOpen() {
		logRejectedUdsConnectOnce.Do(func() {
			log.Warningf("Rejecting attempt to connect to unix domain socket from host filesystem: %q. If you want to allow this, set flag --host-uds=open", fd.ControlFD.Node().FilePath())
		})
		return -1, unix.EPERM
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	// As per capabilities(7), "If the effective user ID is changed from 0 to
	// nonzero, then all capabilities are cleared from the effective set. If the
	// effective user ID is changed from nonzero to 0, then the permitted set is
	// copied to the effective set." Below, we temporarily change the effective
	// UID and GID. So the effective capability set is cleared and restored; the
	// permitted set stays the same. We change GID first, and then UID. Because
	// once the UID is changed, capabilities needed to change GID are dropped.
	uidChanged, gidChanged := false, false
	if int(gid) != impl.config.EGID {
		_, _, err := unix.Syscall(unix.SYS_SETREGID, uintptr(impl.config.RGID), uintptr(gid), 0)
		if err != 0 {
			log.Warningf("Failed to set egid; err: %v", err)
		} else {
			log.Debugf("Successfully set egid to %d", gid)
			gidChanged = true
		}
	}

	if int(uid) != impl.config.EUID {
		_, _, err := unix.Syscall(unix.SYS_SETREUID, uintptr(impl.config.RUID), uintptr(uid), 0)
		if err != 0 {
			log.Warningf("Failed to set euid; err: %v", err)
		} else {
			log.Debugf("Successfully set euid to %d", uid)
			uidChanged = true
		}
	}

	defer func() {
		if uidChanged {
			_, _, err := unix.Syscall(unix.SYS_SETREUID, uintptr(impl.config.RUID), uintptr(impl.config.EUID), 0)
			if err != 0 {
				panic(fmt.Sprintf("Failed to restore euid; err: %v", err))
			}
			log.Debugf("Successfully restored euid to %d", impl.config.EUID)
		}

		if gidChanged {
			_, _, err := unix.Syscall(unix.SYS_SETREGID, uintptr(impl.config.RGID), uintptr(impl.config.EGID), 0)
			if err != 0 {
				panic(fmt.Sprintf("Failed to restore egid; err: %v", err))
			}
			log.Debugf("Successfully restored egid to %d", impl.config.EGID)
		}
	}()

	return fd.Connect(sockType)
}

// ConnectWithGroups implements lisafs.ControlFDImpl.ConnectWithGroups.
func (fd *controlFDLisa) ConnectWithGroups(sockType uint32, uid lisafs.UID, gid lisafs.GID, groups []lisafs.GID) (int, error) {
	impl := fd.Conn().Impl().(*connectionImpl)
	if !impl.config.HostUDS.AllowOpen() {
		logRejectedUdsConnectOnce.Do(func() {
			log.Warningf("Rejecting attempt to connect to unix domain socket from host filesystem: %q. If you want to allow this, set flag --host-uds=open", fd.ControlFD.Node().FilePath())
		})
		return -1, unix.EPERM
	}
	gids := make([]int, len(groups))
	for i, group := range groups {
		gids[i] = int(group)
	}
	if groupsAlreadySet(gids, impl.config.Groups) {
		return fd.ConnectWithCreds(sockType, uid, gid)
	}
	// setgroups(2) needs CAP_SETGID in the effective set, which ConnectWithCreds
	// clears while the euid is changed. So the supplementary groups are changed
	// before it.
	return connectWithGroups(gids, func() (int, error) {
		return fd.ConnectWithCreds(sockType, uid, gid)
	})
}

// connectWithGroups calls connect on a dedicated OS thread whose supplementary
// groups are set to gids. unix.Setgroups changes only the calling thread, which
// is what we want here (syscall.Setgroups would change every thread). The
// thread is never reused: connectOnItsOwnThread returns while still locked to
// it, so the runtime terminates it along with its groups. Threads the runtime
// starts from a locked thread are created by its template thread instead (see
// runtime.newm), so they don't inherit the groups either.
func connectWithGroups(gids []int, connect func() (int, error)) (int, error) {
	done := make(chan connectResult, 1)
	go connectOnItsOwnThread(gids, connect, done)
	res := <-done
	if res.panicValue != nil {
		panic(res.panicValue)
	}
	return res.sock, res.err
}

type connectResult struct {
	sock       int
	err        error
	panicValue any
}

func connectOnItsOwnThread(gids []int, connect func() (int, error), done chan connectResult) {
	runtime.LockOSThread()
	if isThreadGroupLeader() {
		// The runtime never terminates the thread group leader; it parks it for
		// the life of the process instead, so its groups must not change. Stay
		// locked to it so that the new goroutine runs on another thread.
		defer runtime.UnlockOSThread()
		mainless := make(chan connectResult, 1)
		go connectOnItsOwnThread(gids, connect, mainless)
		done <- <-mainless
		return
	}
	defer func() {
		if p := recover(); p != nil {
			done <- connectResult{sock: -1, panicValue: p}
		}
	}()
	if err := unix.Setgroups(gids); err != nil {
		log.Warningf("Failed to set %d supplementary groups; err: %v", len(gids), err)
		done <- connectResult{sock: -1, err: err}
		return
	}
	log.Debugf("Successfully set supplementary groups to %v", gids)
	sock, err := connect()
	done <- connectResult{sock: sock, err: err}
}

// isThreadGroupLeader returns true if the calling thread is the thread group
// leader, whose thread ID is the process ID.
func isThreadGroupLeader() bool {
	return unix.Gettid() == unix.Getpid()
}

func groupsAlreadySet(want, have []int) bool {
	if slices.Contains(have, overflowGID) {
		return false
	}
	x := slices.Clone(want)
	y := slices.Clone(have)
	slices.Sort(x)
	slices.Sort(y)
	return slices.Equal(slices.Compact(x), slices.Compact(y))
}

// LINT.ThenChange(lisafs_darwin.go)
