// Copyright 2020 The gVisor Authors.
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

package forwardcmd

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

func sameStat(a, b *unix.Stat_t) bool {
	return a.Dev == b.Dev && a.Ino == b.Ino
}

// splitParentAndBase splits p on its last '/' without lexical '..' cleaning so
// that parent directory lookup traverses symlinks and '..' in kernel order.
func splitParentAndBase(p string) (parent, base string, hasParent, trailingSlash bool) {
	if strings.HasSuffix(p, "/") {
		return "", "", false, true
	}
	idx := strings.LastIndexByte(p, '/')
	if idx < 0 {
		return "", p, false, false
	}
	if idx == 0 {
		return "/", p[1:], true, false
	}
	return p[:idx], p[idx+1:], true, false
}

// checkStickyDirOwnership enforces Linux fs.protected_symlinks and
// fs.protected_regular ownership rules on an entry in dirStat: inside a
// world-writable sticky directory, the entry must be owned by either the
// effective UID of the caller or the directory owner.
func checkStickyDirOwnership(dirStat *unix.Stat_t, entryUID uint32, euid int) error {
	if euid < 0 {
		return unix.EACCES
	}
	if dirStat.Mode&(unix.S_ISVTX|0002) == (unix.S_ISVTX|0002) &&
		entryUID != uint32(euid) &&
		entryUID != dirStat.Uid {
		return unix.EACCES
	}
	return nil
}

func openatEINTR(dirfd int, path string, flags int, mode uint32) (int, error) {
	for {
		fd, err := unix.Openat(dirfd, path, flags, mode)
		if !errors.Is(err, unix.EINTR) {
			return fd, err
		}
	}
}

func fstatatEINTR(dirfd int, path string, st *unix.Stat_t, flags int) error {
	for {
		err := unix.Fstatat(dirfd, path, st, flags)
		if !errors.Is(err, unix.EINTR) {
			return err
		}
	}
}

func fstatfsEINTR(fd int, sfs *unix.Statfs_t) error {
	for {
		err := unix.Fstatfs(fd, sfs)
		if !errors.Is(err, unix.EINTR) {
			return err
		}
	}
}

func readlinkFD(pathFD int) (string, error) {
	for size := 128; ; size *= 2 {
		buf := make([]byte, size)
		var (
			n   int
			err error
		)
		for {
			n, err = unix.Readlinkat(pathFD, "", buf)
			if !errors.Is(err, unix.EINTR) {
				break
			}
		}
		if err != nil {
			return "", err
		}
		if n < size {
			return string(buf[:n]), nil
		}
	}
}

// writeOutputFile resolves outPath using pinned directory file descriptors and
// writes output via writeFn without truncating existing files on error or
// replacing symlinks.
func writeOutputFile(outPath string, inStat *unix.Stat_t, writeFn func(*os.File) error) error {
	dirFD := unix.AT_FDCWD
	defer func() {
		if dirFD != unix.AT_FDCWD {
			unix.Close(dirFD)
		}
	}()

	cur := outPath
	for range maxSymlinkHops {
		parent, base, hasParent, trailingSlash := splitParentAndBase(cur)
		if trailingSlash || base == "" || base == "." || base == ".." {
			fd, err := openatEINTR(dirFD, cur, unix.O_WRONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			if err != nil {
				return fmt.Errorf("error opening output: %w", err)
			}
			unix.Close(fd)
			return fmt.Errorf("error opening output: %w", unix.EISDIR)
		}
		if hasParent || dirFD == unix.AT_FDCWD {
			lookupDir := parent
			if !hasParent {
				lookupDir = "."
			}
			nextDirFD, err := openatEINTR(dirFD, lookupDir, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
			if err != nil {
				return fmt.Errorf("error resolving output: %w", err)
			}
			if dirFD != unix.AT_FDCWD {
				unix.Close(dirFD)
			}
			dirFD = nextDirFD
		}

		var dirStat unix.Stat_t
		if err := fstatEINTR(dirFD, &dirStat); err != nil {
			return fmt.Errorf("error statting output directory: %w", err)
		}
		// Procfs magic links (such as /proc/self/fd/1) cannot be replaced via
		// rename; open them directly through the pinned procfs directory FD.
		var sfs unix.Statfs_t
		if err := fstatfsEINTR(dirFD, &sfs); err == nil && sfs.Type == unix.PROC_SUPER_MAGIC {
			return writeDirectOpenat(dirFD, base, 0, outPath, inStat, false, writeFn)
		}

		pathFD, err := openatEINTR(dirFD, base, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			if errors.Is(err, unix.ENOENT) {
				return writeStagedNewFile(dirFD, base, writeFn)
			}
			return fmt.Errorf("error opening output: %w", err)
		}
		var lst unix.Stat_t
		if err := fstatEINTR(pathFD, &lst); err != nil {
			unix.Close(pathFD)
			return fmt.Errorf("error statting output: %w", err)
		}

		switch lst.Mode & unix.S_IFMT {
		case unix.S_IFLNK:
			if err := checkStickyDirOwnership(&dirStat, lst.Uid, os.Geteuid()); err != nil {
				unix.Close(pathFD)
				return fmt.Errorf("error resolving output: %w", err)
			}
			var followStat unix.Stat_t
			if err := fstatatEINTR(dirFD, base, &followStat, 0); err != nil && !errors.Is(err, unix.ENOENT) {
				unix.Close(pathFD)
				return fmt.Errorf("error resolving output: %w", err)
			}
			linkDest, err := readlinkFD(pathFD)
			unix.Close(pathFD)
			if err != nil {
				return fmt.Errorf("error reading output symlink: %w", err)
			}
			if linkDest == "" {
				return fmt.Errorf("error resolving output: %w", unix.ENOENT)
			}
			cur = linkDest
			continue

		case unix.S_IFREG:
			unix.Close(pathFD)
			if sameStat(inStat, &lst) {
				return fmt.Errorf("error: input and output refer to the same file: %s", outPath)
			}
			if err := checkStickyDirOwnership(&dirStat, lst.Uid, os.Geteuid()); err != nil {
				return fmt.Errorf("error opening output: %w", err)
			}
			return writeStagedExistingFile(dirFD, base, outPath, inStat, &lst, writeFn)

		case unix.S_IFDIR:
			unix.Close(pathFD)
			return fmt.Errorf("error opening output: %w", unix.EISDIR)

		default:
			// Non-regular targets (such as /dev/null or FIFOs) cannot be replaced
			// via rename; open them directly with O_NOFOLLOW and without O_TRUNC.
			unix.Close(pathFD)
			return writeDirectOpenat(dirFD, base, unix.O_NOFOLLOW, outPath, inStat, true, writeFn)
		}
	}
	return fmt.Errorf("error resolving output: %w", unix.ELOOP)
}

func writeDirectOpenat(dirFD int, base string, extraFlags int, outPath string, inStat *unix.Stat_t, requireNonRegular bool, writeFn func(*os.File) error) error {
	outFD, err := openatEINTR(dirFD, base, unix.O_WRONLY|unix.O_CLOEXEC|extraFlags, 0)
	if err != nil {
		return fmt.Errorf("error opening output: %w", err)
	}
	var outStat unix.Stat_t
	if err := fstatEINTR(outFD, &outStat); err != nil {
		unix.Close(outFD)
		return fmt.Errorf("error statting output: %w", err)
	}
	if sameStat(inStat, &outStat) {
		unix.Close(outFD)
		return fmt.Errorf("error: input and output refer to the same file: %s", outPath)
	}
	if requireNonRegular && outStat.Mode&unix.S_IFMT == unix.S_IFREG {
		unix.Close(outFD)
		return fmt.Errorf("error: output changed file type during open: %s", outPath)
	}
	outFile := os.NewFile(uintptr(outFD), outPath)
	writeErr := writeFn(outFile)
	closeErr := outFile.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return fmt.Errorf("error flushing output: %w", closeErr)
	}
	return nil
}

func createTempOutputAt(dirFD int, mode uint32) (*os.File, string, error) {
	for range 256 {
		var rnd [8]byte
		if _, err := rand.Read(rnd[:]); err != nil {
			return nil, "", err
		}
		tmpName := ".statefile-" + hex.EncodeToString(rnd[:])
		fd, err := openatEINTR(dirFD, tmpName, unix.O_CREAT|unix.O_EXCL|unix.O_WRONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, mode)
		if errors.Is(err, unix.EEXIST) {
			continue
		}
		if err != nil {
			return nil, "", err
		}
		return os.NewFile(uintptr(fd), tmpName), tmpName, nil
	}
	return nil, "", os.ErrExist
}

func writeStagedNewFile(dirFD int, base string, writeFn func(*os.File) error) error {
	// Pass 0644 to openat(O_CREAT|O_EXCL) so the kernel applies the process umask.
	tmpFile, tmpName, err := createTempOutputAt(dirFD, 0644)
	if err != nil {
		return fmt.Errorf("error creating temporary output: %w", err)
	}
	keepTemp := false
	defer func() {
		if !keepTemp {
			unix.Unlinkat(dirFD, tmpName, 0)
		}
	}()
	writeErr := writeFn(tmpFile)
	closeErr := tmpFile.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return fmt.Errorf("error flushing output: %w", closeErr)
	}
	if err := commitNewFileNoReplace(dirFD, tmpName, base); err != nil {
		return fmt.Errorf("error renaming output: %w", err)
	}
	keepTemp = true
	return nil
}

func commitNewFileNoReplace(dirFD int, tmpName, base string) error {
	err := unix.Renameat2(dirFD, tmpName, dirFD, base, unix.RENAME_NOREPLACE)
	if err == nil {
		return nil
	}
	if !errors.Is(err, unix.ENOSYS) && !errors.Is(err, unix.EINVAL) {
		return err
	}
	// Fall back to linkat (which never overwrites or follows symlinks at base)
	// followed by unlinkat of tmpName on filesystems without RENAME_NOREPLACE.
	if linkErr := unix.Linkat(dirFD, tmpName, dirFD, base, 0); linkErr == nil {
		unix.Unlinkat(dirFD, tmpName, 0)
		return nil
	} else if !errors.Is(linkErr, unix.EPERM) && !errors.Is(linkErr, unix.EOPNOTSUPP) && !errors.Is(linkErr, unix.EXDEV) {
		return linkErr
	}
	var st unix.Stat_t
	if statErr := fstatatEINTR(dirFD, base, &st, unix.AT_SYMLINK_NOFOLLOW); statErr == nil {
		return unix.EEXIST
	} else if !errors.Is(statErr, unix.ENOENT) {
		return statErr
	}
	return unix.Renameat(dirFD, tmpName, dirFD, base)
}

func writeStagedExistingFile(dirFD int, base, outPath string, inStat, origStat *unix.Stat_t, writeFn func(*os.File) error) error {
	// Open the existing regular file with O_WRONLY|O_CREAT|O_NOFOLLOW (without
	// O_TRUNC) so the kernel enforces DAC write permissions, LSM checks, and
	// fs.protected_regular before staging output.
	probeFD, err := openatEINTR(dirFD, base, unix.O_WRONLY|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0644)
	if err != nil {
		return fmt.Errorf("error opening output: %w", err)
	}
	defer unix.Close(probeFD)

	var probeStat unix.Stat_t
	if err := fstatEINTR(probeFD, &probeStat); err != nil {
		return fmt.Errorf("error statting output: %w", err)
	}
	if probeStat.Mode&unix.S_IFMT != unix.S_IFREG || !sameStat(origStat, &probeStat) {
		return fmt.Errorf("error: output changed during open: %s", outPath)
	}
	if sameStat(inStat, &probeStat) {
		return fmt.Errorf("error: input and output refer to the same file: %s", outPath)
	}

	tmpFile, tmpName, err := createTempOutputAt(dirFD, 0600)
	if err != nil {
		return fmt.Errorf("error creating temporary output: %w", err)
	}
	keepTemp := false
	defer func() {
		if !keepTemp {
			unix.Unlinkat(dirFD, tmpName, 0)
		}
	}()
	if err := unix.Fchmod(int(tmpFile.Fd()), uint32(probeStat.Mode&0777)); err != nil {
		tmpFile.Close()
		return fmt.Errorf("error setting temporary output permissions: %w", err)
	}
	writeErr := writeFn(tmpFile)
	closeErr := tmpFile.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return fmt.Errorf("error flushing output: %w", closeErr)
	}

	var curStat unix.Stat_t
	if err := fstatatEINTR(dirFD, base, &curStat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return fmt.Errorf("error verifying output before rename: %w", err)
	}
	if !sameStat(&probeStat, &curStat) {
		return fmt.Errorf("error: output changed before commit: %s", outPath)
	}
	if err := unix.Renameat(dirFD, tmpName, dirFD, base); err != nil {
		return fmt.Errorf("error renaming output: %w", err)
	}
	keepTemp = true
	return nil
}
