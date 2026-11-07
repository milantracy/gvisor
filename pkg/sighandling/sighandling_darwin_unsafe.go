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

package sighandling

import (
	"fmt"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// saNoCldStop is SA_NOCLDSTOP from <sys/signal.h> on darwin.
const saNoCldStop = 0x8

// sigaction is struct sigaction from <signal.h> on darwin, as libSystem's
// sigaction(2) wrapper takes it. The wrapper fills in the kernel's sa_tramp
// field, so the handler is called through libSystem's _sigtramp, which calls
// sigreturn(2) when the handler returns.
type sigaction struct {
	handler uintptr
	mask    uint32
	flags   int32
}

// libcSigaction calls libSystem's sigaction(2).
func libcSigaction(sig unix.Signal, act, oldact *sigaction) error {
	_, _, e := syscall_rawSyscall(libc_sigaction_trampoline_addr, uintptr(sig), uintptr(unsafe.Pointer(act)), uintptr(unsafe.Pointer(oldact)))
	if e != 0 {
		return e
	}
	return nil
}

// IgnoreChildStop sets the SA_NOCLDSTOP flag, causing child processes to not
// generate SIGCHLD when they stop.
func IgnoreChildStop() error {
	var sa sigaction
	if err := libcSigaction(unix.SIGCHLD, nil, &sa); err != nil {
		return err
	}
	sa.flags |= saNoCldStop
	return libcSigaction(unix.SIGCHLD, &sa, nil)
}

// ReplaceSignalHandler replaces the existing signal handler for the provided
// signal with the function pointer at `handler`. This bypasses the Go runtime
// signal handlers, and should only be used for low-level signal handlers where
// use of signal.Notify is not appropriate.
//
// It stores the value of the previously set handler in previous. handler is
// called with the C calling convention: the signal number, a siginfo_t pointer
// and a ucontext_t pointer.
func ReplaceSignalHandler(sig unix.Signal, handler uintptr, previous *uintptr) error {
	var sa sigaction
	if err := libcSigaction(sig, nil, &sa); err != nil {
		return err
	}
	// Fail if there isn't a previous handler.
	if sa.handler == 0 {
		return fmt.Errorf("previous handler for signal %x isn't set", sig)
	}
	*previous = sa.handler
	sa.handler = handler
	return libcSigaction(sig, &sa, nil)
}

// syscall_rawSyscall calls the libSystem function at fn without telling the Go
// scheduler, as x/sys/unix does on darwin.
//
//go:linkname syscall_rawSyscall syscall.rawSyscall
func syscall_rawSyscall(fn, a1, a2, a3 uintptr) (r1, r2 uintptr, err syscall.Errno)

var libc_sigaction_trampoline_addr uintptr

//go:cgo_import_dynamic libc_sigaction sigaction "/usr/lib/libSystem.B.dylib"
