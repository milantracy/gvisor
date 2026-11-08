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
	"os"
	"os/signal"

	"golang.org/x/sys/unix"
	"gvisor.dev/gvisor/pkg/abi/linux"
)

// hostSignals maps the Linux signals to forward to the darwin signals with
// the same meaning. darwin numbers signals differently from Linux, and has no
// SIGSTKFLT or SIGPWR.
var hostSignals = map[linux.Signal]unix.Signal{
	linux.SIGHUP:    unix.SIGHUP,
	linux.SIGINT:    unix.SIGINT,
	linux.SIGQUIT:   unix.SIGQUIT,
	linux.SIGILL:    unix.SIGILL,
	linux.SIGTRAP:   unix.SIGTRAP,
	linux.SIGABRT:   unix.SIGABRT,
	linux.SIGBUS:    unix.SIGBUS,
	linux.SIGFPE:    unix.SIGFPE,
	linux.SIGKILL:   unix.SIGKILL,
	linux.SIGUSR1:   unix.SIGUSR1,
	linux.SIGSEGV:   unix.SIGSEGV,
	linux.SIGUSR2:   unix.SIGUSR2,
	linux.SIGALRM:   unix.SIGALRM,
	linux.SIGTERM:   unix.SIGTERM,
	linux.SIGCONT:   unix.SIGCONT,
	linux.SIGSTOP:   unix.SIGSTOP,
	linux.SIGTSTP:   unix.SIGTSTP,
	linux.SIGTTIN:   unix.SIGTTIN,
	linux.SIGTTOU:   unix.SIGTTOU,
	linux.SIGXCPU:   unix.SIGXCPU,
	linux.SIGXFSZ:   unix.SIGXFSZ,
	linux.SIGVTALRM: unix.SIGVTALRM,
	linux.SIGPROF:   unix.SIGPROF,
	linux.SIGWINCH:  unix.SIGWINCH,
	linux.SIGIO:     unix.SIGIO,
	linux.SIGSYS:    unix.SIGSYS,
}

// StartSignalForwarding ensures that synchronous signals are passed to the
// given handler function and returns a callback that stops signal delivery.
// The handler gets the Linux number of each host signal.
//
// Note that this function permanently takes over signal handling. After the
// stop callback, signals revert to the default Go runtime behavior, which
// cannot be overridden with external calls to signal.Notify.
func StartSignalForwarding(handler func(linux.Signal)) func() {
	stop := make(chan struct{})
	done := make(chan struct{})

	// As on Linux, there is one channel per Linux standard signal, and
	// sigchans[N-1] is the channel of Linux signal N. Signals that Linux
	// does not forward (SIGURG, SIGPIPE, SIGCHLD) are not in hostSignals.
	var sigchans []chan os.Signal
	for sig := 1; sig <= numSignals+1; sig++ {
		sigchan := make(chan os.Signal, 1)
		sigchans = append(sigchans, sigchan)
		if hostSig, ok := hostSignals[linux.Signal(sig)]; ok {
			signal.Notify(sigchan, hostSig)
		}
	}
	go handleSignals(sigchans, handler, stop, done) // S/R-SAFE: synchronized by Kernel.extMu.

	return func() {
		close(stop)
		<-done
	}
}
