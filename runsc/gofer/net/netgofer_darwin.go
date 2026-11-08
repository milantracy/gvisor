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

// Package net implements the netgofer sidecar network proxy.
package net

import (
	"context"
	"net/netip"
	"os"
	"runtime/debug"
	"time"

	"github.com/google/subcommands"
	"golang.org/x/sys/unix"
	"gvisor.dev/gvisor/pkg/log"
	"gvisor.dev/gvisor/pkg/seatbelt"
	"gvisor.dev/gvisor/runsc/cmd/util"
	"gvisor.dev/gvisor/runsc/config"
	"gvisor.dev/gvisor/runsc/flag"
)

// DefaultMTU is the MTU assumed for a network link when none is otherwise
// known. It matches the conventional Ethernet MTU.
const DefaultMTU = 1500

// Netgofer implements subcommands.Command for the "netgofer" command.
//
// On darwin, netgofer is the network of the sandbox. See proxy_darwin.go.
// It runs confined by a Seatbelt profile that allows only outbound IP
// connections, and exits when the sandbox process exits.
type Netgofer struct {
	util.InternalSubCommand

	udsFD       int
	mtu         int
	sandboxPID  int
	sandboxAddr string
	prefixLen   int
}

// Name implements subcommands.Command.
func (*Netgofer) Name() string {
	return "netgofer"
}

// Synopsis implements subcommands.Command.
func (*Netgofer) Synopsis() string {
	return "launch a netgofer process that proxies network traffic for all containers in the sandbox"
}

// Usage implements subcommands.Command.
func (*Netgofer) Usage() string {
	return "netgofer [flags]\n"
}

// SetFlags implements subcommands.Command.
func (n *Netgofer) SetFlags(f *flag.FlagSet) {
	f.IntVar(&n.udsFD, "uds-fd", -1, "FD of the SOCK_DGRAM packet link to the sandbox")
	f.IntVar(&n.mtu, "mtu", DefaultMTU, "MTU for the network interface")
	f.IntVar(&n.sandboxPID, "sandbox-pid", -1, "PID of the sandbox process; netgofer exits when it exits")
	f.StringVar(&n.sandboxAddr, "sandbox-addr", "", "IPv4 address of the sandbox")
	f.IntVar(&n.prefixLen, "prefix-len", 24, "prefix length of the sandbox network")
}

// Execute implements subcommands.Command.
func (n *Netgofer) Execute(_ context.Context, f *flag.FlagSet, args ...any) subcommands.ExitStatus {
	if n.udsFD < 0 || n.sandboxPID <= 0 || f.NArg() != 0 {
		f.Usage()
		return subcommands.ExitUsageError
	}
	conf := args[0].(*config.Config)
	debug.SetTraceback(conf.Traceback)

	addr, err := netip.ParseAddr(n.sandboxAddr)
	if err != nil || !addr.Is4() {
		return util.Errorf("invalid --sandbox-addr %q", n.sandboxAddr)
	}
	prefix, err := addr.Prefix(n.prefixLen)
	if err != nil {
		return util.Errorf("invalid --prefix-len %d: %v", n.prefixLen, err)
	}
	p := &proxy{sandboxPrefix: prefix}
	if p.dns, err = hostDNSServer("/etc/resolv.conf"); err != nil {
		log.Warningf("No host DNS server, DNS in the sandbox does not work: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := cancelOnExit(n.sandboxPID, cancel); err != nil {
		return util.Errorf("watching the sandbox process %d: %v", n.sandboxPID, err)
	}

	// Go reads /etc/localtime on first use, which the profile denies.
	_ = time.Local.String()
	if err := seatbelt.Init(netProfile); err != nil {
		return util.Errorf("confining netgofer: %v", err)
	}
	log.Infof("Netgofer confined; serving sandbox network %v", prefix)

	if err := serveProxy(ctx, os.NewFile(uintptr(n.udsFD), "packet link"), uint32(n.mtu), p); err != nil {
		return util.Errorf("%v", err)
	}
	log.Infof("Sandbox process %d exited; netgofer exits", n.sandboxPID)
	return subcommands.ExitSuccess
}

// cancelOnExit calls cancel when the process pid exits.
func cancelOnExit(pid int, cancel func()) error {
	kq, err := unix.Kqueue()
	if err != nil {
		return err
	}
	changes := []unix.Kevent_t{{
		Ident:  uint64(pid),
		Filter: unix.EVFILT_PROC,
		Flags:  unix.EV_ADD | unix.EV_ONESHOT,
		Fflags: unix.NOTE_EXIT,
	}}
	if _, err := unix.Kevent(kq, changes, nil, nil); err != nil {
		unix.Close(kq)
		return err
	}
	go func() {
		defer unix.Close(kq)
		events := make([]unix.Kevent_t, 1)
		for {
			n, err := unix.Kevent(kq, nil, events, nil)
			if err == unix.EINTR {
				continue
			}
			if err != nil {
				log.Warningf("Waiting for process %d: %v", pid, err)
			}
			if err != nil || n == 1 {
				cancel()
				return
			}
		}
	}()
	return nil
}
