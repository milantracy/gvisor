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

package specutils

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	specs "github.com/opencontainers/runtime-spec/specs-go"
	"golang.org/x/sys/unix"
)

// darwin has no pidfds. A process is identified by its PID and its start
// time, which the kernel reports in kinfo_proc.

// szomb is the p_stat value of a zombie process (SZOMB in <sys/proc.h>).
const szomb = 5

// CopySpec returns a read-only copy of the given OCI runtime spec, encoded as
// JSON. It can be passed around between processes to ensure they all see the
// same spec as the caller.
//
// darwin has no memfd. The copy is an unlinked temporary file, and the
// returned FD is open for reading only.
func CopySpec(spec *specs.Spec) (*os.File, error) {
	data, err := json.Marshal(spec)
	if err != nil {
		return nil, fmt.Errorf("cannot encode spec: %w", err)
	}
	w, err := os.CreateTemp("", "config.json-")
	if err != nil {
		return nil, fmt.Errorf("cannot create spec copy: %w", err)
	}
	defer os.Remove(w.Name())
	defer w.Close()
	if _, err := w.Write(data); err != nil {
		return nil, fmt.Errorf("cannot write spec copy: %w", err)
	}
	r, err := os.Open(w.Name())
	if err != nil {
		return nil, fmt.Errorf("cannot reopen spec copy: %w", err)
	}
	return r, nil
}

// kinfoProc returns the kinfo_proc of pid, or unix.ESRCH if there is no such
// process.
func kinfoProc(pid int) (*unix.KinfoProc, error) {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err == unix.EIO {
		// The sysctl returns no entry for a PID with no process, which
		// SysctlKinfoProc reports as EIO.
		return nil, unix.ESRCH
	}
	if err != nil {
		return nil, err
	}
	if int(kp.Proc.P_pid) != pid {
		return nil, unix.ESRCH
	}
	return kp, nil
}

func kinfoIdentity(kp *unix.KinfoProc) uint64 {
	t := kp.Proc.P_starttime
	return uint64(t.Sec)*1000000 + uint64(t.Usec)
}

// ProcessIdentity returns an identifier of the process with the given PID
// that is stable and unique: its start time in microseconds.
func ProcessIdentity(pid int) (uint64, error) {
	kp, err := kinfoProc(pid)
	if err != nil {
		return 0, fmt.Errorf("cannot look up process %d: %w", pid, err)
	}
	return kinfoIdentity(kp), nil
}

// lookupProcess returns the kinfo_proc of the process with the given PID and
// identity, or unix.ESRCH if there is no such process. If identity is 0, it
// matches any process with the given PID.
func lookupProcess(pid int, identity uint64) (*unix.KinfoProc, error) {
	kp, err := kinfoProc(pid)
	if err != nil {
		return nil, err
	}
	if identity != 0 && kinfoIdentity(kp) != identity {
		return nil, unix.ESRCH
	}
	return kp, nil
}

// IsProcessRunning returns true if the process with the given PID and
// ProcessIdentity is running, rather than a zombie. Nonpositive PIDs and
// processes that no longer exist return false.
func IsProcessRunning(pid int, identity uint64) (bool, error) {
	if pid <= 0 {
		return false, nil
	}
	kp, err := lookupProcess(pid, identity)
	if err == unix.ESRCH {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("cannot look up process %d: %w", pid, err)
	}
	return kp.Proc.P_stat != szomb, nil
}

// KillProcess sends SIGKILL to the process with the given PID and
// ProcessIdentity, if it exists.
// If `identity` is zero, the process with PID `pid` is killed regardless
// of its identity.
//
// Unlike a pidfd, the PID can be reused between the check and the kill.
func KillProcess(pid int, identity uint64) error {
	if _, err := lookupProcess(pid, identity); err == unix.ESRCH {
		return nil
	} else if err != nil {
		return fmt.Errorf("cannot look up process %d: %w", pid, err)
	}
	if err := unix.Kill(pid, unix.SIGKILL); err != nil && err != unix.ESRCH {
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
	kq, err := unix.Kqueue()
	if err != nil {
		return err
	}
	defer unix.Close(kq)
	changes := []unix.Kevent_t{{
		Ident:  uint64(pid),
		Filter: unix.EVFILT_PROC,
		Flags:  unix.EV_ADD | unix.EV_ONESHOT,
		Fflags: unix.NOTE_EXIT,
	}}
	if _, err := unix.Kevent(kq, changes, nil, nil); err == unix.ESRCH {
		return nil // Already gone.
	} else if err != nil {
		return fmt.Errorf("watching process %d: %w", pid, err)
	}
	// Check the identity after the kevent is added, so that an exit between
	// the two is not missed.
	if kp, err := lookupProcess(pid, identity); err == unix.ESRCH || (err == nil && kp.Proc.P_stat == szomb) {
		return nil
	}
	deadline := time.Now().Add(timeout)
	events := make([]unix.Kevent_t, 1)
	for {
		left := time.Until(deadline)
		if left <= 0 {
			return fmt.Errorf("process %d is still running after %v", pid, timeout)
		}
		ts := unix.NsecToTimespec(left.Nanoseconds())
		n, err := unix.Kevent(kq, nil, events, &ts)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return fmt.Errorf("waiting for process %d: %w", pid, err)
		}
		if n > 0 {
			return nil
		}
	}
}

// MaybeRunAsRoot does nothing on darwin. runsc needs no privileges there:
// the Sentry and the gofer run as the user, confined by Seatbelt profiles.
func MaybeRunAsRoot() error {
	return nil
}

// UnmountNullNetNS does nothing on darwin, which has no network namespaces.
func UnmountNullNetNS(sharedRootDir string) {}

// selfExePath returns the path of the running binary. darwin has no
// /proc/self/exe; the path is the one that the kernel reported at exec time.
func selfExePath() string {
	p, err := os.Executable()
	if err != nil {
		panic(fmt.Sprintf("cannot find the path of the running binary: %v", err))
	}
	return p
}
