// Copyright 2018 The gVisor Authors.
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

// Package specutils contains utility functions for working with OCI runtime
// specs.
//go:build linux

package specutils

import (
	"encoding/json"
	"fmt"

	"os"
	"path"
	"path/filepath"

	"strconv"

	"github.com/cenkalti/backoff"
	"time"

	specs "github.com/opencontainers/runtime-spec/specs-go"

	"golang.org/x/sys/unix"
)

// CopySpec returns a sealed (read-only) copy of the given OCI runtime spec,
// encoded as JSON. It can be passed around between processes to ensure they
// all see the same spec as the caller.
func CopySpec(spec *specs.Spec) (*os.File, error) {
	data, err := json.Marshal(spec)
	if err != nil {
		return nil, fmt.Errorf("cannot encode spec: %w", err)
	}
	fd, err := unix.MemfdCreate("config.json", unix.MFD_CLOEXEC|unix.MFD_ALLOW_SEALING)
	if err != nil {
		return nil, fmt.Errorf("cannot create memfd: %w", err)
	}
	f := os.NewFile(uintptr(fd), "config.json")
	if _, err := f.WriteAt(data, 0); err != nil {
		f.Close()
		return nil, fmt.Errorf("cannot write spec copy: %w", err)
	}
	if _, err := unix.FcntlInt(f.Fd(), unix.F_ADD_SEALS, unix.F_SEAL_SEAL|unix.F_SEAL_SHRINK|unix.F_SEAL_GROW|unix.F_SEAL_WRITE); err != nil {
		f.Close()
		return nil, fmt.Errorf("cannot seal spec copy: %w", err)
	}
	return f, nil
}

// WaitForReady waits for a process to become ready. The process is ready when
// the 'ready' function returns true. It continues to wait if 'ready' returns
// false. It returns error on timeout, if the process stops or if 'ready' fails.
func WaitForReady(pid int, timeout time.Duration, ready func() (bool, error)) error {
	b := backoff.NewExponentialBackOff()
	b.InitialInterval = 1 * time.Millisecond
	b.MaxInterval = 1 * time.Second
	b.MaxElapsedTime = timeout

	op := func() error {
		if ok, err := ready(); err != nil {
			return backoff.Permanent(err)
		} else if ok {
			return nil
		}

		// Check if the process is still running.
		// If the process is alive, child is 0 because of the NOHANG option.
		// If the process has terminated, child equals the process id.
		var ws unix.WaitStatus
		var ru unix.Rusage
		child, err := unix.Wait4(pid, &ws, unix.WNOHANG, &ru)
		if err != nil {
			return backoff.Permanent(fmt.Errorf("error waiting for process: %v", err))
		} else if child == pid {
			return backoff.Permanent(fmt.Errorf("process %d has terminated", pid))
		}
		return fmt.Errorf("process %d not running yet", pid)
	}
	return backoff.Retry(op, b)
}

// IsProcessRunning returns true if the process with the given PID and
// ProcessIdentity is running, rather than a zombie. Nonpositive PIDs and
// processes that no longer exist return false.
func IsProcessRunning(pid int, identity uint64) (bool, error) {
	if pid <= 0 {
		return false, nil
	}
	pidfd, err := OpenProcess(pid, identity)
	if err != nil {
		if err == unix.ESRCH {
			return false, nil
		}
		return false, fmt.Errorf("cannot open pidfd for process %d: %w", pid, err)
	}
	defer unix.Close(pidfd)

	pfds := []unix.PollFd{{Fd: int32(pidfd), Events: unix.POLLIN}}
	for {
		n, err := unix.Poll(pfds, 0)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return false, fmt.Errorf("polling pidfd for process %d: %w", pid, err)
		}
		// A pidfd becomes readable (POLLIN) when the process exits (including when it is a zombie).
		return n == 0, nil
	}
}

// ProcessIdentity returns an identifier of the process with the given PID
// that is stable and unique. Returns 0 on hosts without `pidfs`.
func ProcessIdentity(pid int) (uint64, error) {
	pidfd, err := OpenProcess(pid, 0)
	if err != nil {
		return 0, fmt.Errorf("cannot open pidfd for process %d: %w", pid, err)
	}
	defer unix.Close(pidfd)
	return pidfdIdentity(pidfd)
}

func pidfdIdentity(pidfd int) (uint64, error) {
	var fs unix.Statfs_t
	if err := unix.Fstatfs(pidfd, &fs); err != nil {
		return 0, fmt.Errorf("cannot statfs pidfd: %w", err)
	}
	if fs.Type != PIDFS_MAGIC {
		return 0, nil
	}
	var st unix.Stat_t
	if err := unix.Fstat(pidfd, &st); err != nil {
		return 0, fmt.Errorf("cannot stat pidfd: %w", err)
	}
	return st.Ino, nil
}

// OpenProcess returns a pidfd for the process with the given PID and
// identity, or `unix.ESRCH` if there is no such process.
// If `identity` is 0, is matches any process with the given PID.
func OpenProcess(pid int, identity uint64) (int, error) {
	pidfd, err := unix.PidfdOpen(pid, 0)
	if err == unix.EINVAL {
		return -1, unix.ESRCH // pid is a thread, not a process.
	}
	if err != nil || identity == 0 {
		return pidfd, err
	}
	got, err := pidfdIdentity(pidfd)
	if err == nil && got != identity {
		err = unix.ESRCH
	}
	if err != nil {
		unix.Close(pidfd)
		return -1, err
	}
	return pidfd, nil
}

// KillProcess sends SIGKILL to the process with the given PID and
// ProcessIdentity, if it exists.
// If `identity` is zero, the process with PID `pid` is killed regardless
// of its identity.
func KillProcess(pid int, identity uint64) error {
	pidfd, err := OpenProcess(pid, identity)
	if err == unix.ESRCH {
		return nil
	}
	if err != nil {
		return fmt.Errorf("cannot open pidfd for process %d: %w", pid, err)
	}
	defer unix.Close(pidfd)
	if err := unix.PidfdSendSignal(pidfd, unix.SIGKILL, nil, 0); err != nil && err != unix.ESRCH {
		return fmt.Errorf("cannot kill process %d: %w", pid, err)
	}
	return nil
}

// WaitForNonChildExit waits for the process with the given PID and
// `identity` to exit, up to `timeout`
// If `identity` is zero, match processes on `pid` alone.
// `pid` must not be a child of the current process.
// Returns immediately if the process does not exist.
func WaitForNonChildExit(pid int, identity uint64, timeout time.Duration) error {
	if pid <= 0 {
		return fmt.Errorf("invalid pid: %d", pid)
	}
	pidfd, err := OpenProcess(pid, identity)
	if err == unix.ESRCH {
		return nil // Already gone.
	}
	if err != nil {
		return fmt.Errorf("cannot open pidfd for process %d: %w", pid, err)
	}
	defer unix.Close(pidfd)
	deadline := time.Now().Add(timeout)
	for {
		timeoutMS := int(time.Until(deadline).Milliseconds())
		if timeoutMS <= 0 {
			return fmt.Errorf("process %d is still running after %v", pid, timeout)
		}
		pfds := []unix.PollFd{{Fd: int32(pidfd), Events: unix.POLLIN}}
		n, err := unix.Poll(pfds, timeoutMS)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return fmt.Errorf("polling pidfd for process %d: %v", pid, err)
		}
		if n > 0 {
			return nil // The pidfd becomes "readable" when the process exits.
		}
	}
}

// SafeSetupAndMount creates the mount point and calls Mount with the given
// flags. procPath is the path to procfs. If it is "", procfs is assumed to be
// mounted at /proc.
func SafeSetupAndMount(src, dst, typ string, flags uint32, procPath string) error {
	// Create the mount point inside. The type must be the same as the source
	// (file or directory).
	var isDir bool
	if typ == "proc" {
		// Special case, as there is no source directory for proc mounts.
		isDir = true
	} else if fi, err := os.Stat(src); err != nil {
		return fmt.Errorf("stat(%q) failed: %v", src, err)
	} else {
		isDir = fi.IsDir()
	}

	if isDir {
		// Create the destination directory.
		if err := os.MkdirAll(dst, 0777); err != nil {
			return fmt.Errorf("mkdir(%q) failed: %v", dst, err)
		}
	} else {
		// Create the parent destination directory.
		parent := path.Dir(dst)
		if err := os.MkdirAll(parent, 0777); err != nil {
			return fmt.Errorf("mkdir(%q) failed: %v", parent, err)
		}
		// Create the destination file if it does not exist.
		f, err := os.OpenFile(dst, unix.O_CREAT, 0777)
		if err != nil {
			return fmt.Errorf("open(%q) failed: %v", dst, err)
		}
		f.Close()
	}

	// Do the mount.
	if err := SafeMount(src, dst, typ, uintptr(flags), "", procPath); err != nil {
		return fmt.Errorf("mount(%q, %q, %d) failed: %v", src, dst, flags, err)
	}
	return nil
}

// SafeMount is like unix.Mount, but will fail if dst is a symlink. procPath is
// the path to procfs. If it is "", procfs is assumed to be mounted at /proc.
//
// SafeMount can fail when dst contains a symlink. However, it is called in the
// normal case with a destination consisting of a known root (/proc/root) and
// symlink-free path (from resolveSymlink).
func SafeMount(src, dst, fstype string, flags uintptr, data, procPath string) error {
	// Open the destination.
	fd, err := unix.Open(dst, unix.O_PATH|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("failed to safely mount: Open(%s, _, _): %w", dst, err)
	}
	defer unix.Close(fd)

	// Use /proc/self/fd/ to verify that we opened the intended destination. This
	// guards against dst being a symlink, in which case we could accidentally
	// mount over the symlink's target.
	if procPath == "" {
		procPath = "/proc"
	}
	safePath := filepath.Join(procPath, "self/fd", strconv.Itoa(fd))
	target, err := os.Readlink(safePath)
	if err != nil {
		return fmt.Errorf("failed to safely mount: Readlink(%s): %w", safePath, err)
	}
	if dst != target {
		return &ErrSymlinkMount{fmt.Errorf("failed to safely mount: expected to open %s, but found %s", dst, target)}
	}

	mountErr := unix.Mount(src, safePath, fstype, flags, data)
	if mountErr != nil {
		return fmt.Errorf("mount(%q, %q, %q, %#x, %q) failed: %w", src, safePath, fstype, flags, data, mountErr)
	}
	return nil
}
