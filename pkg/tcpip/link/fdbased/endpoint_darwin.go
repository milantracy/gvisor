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

package fdbased

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"

	"golang.org/x/sys/unix"
	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/log"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/link/channel"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
)

// On darwin, the fd-based endpoint supports only a network proxy link: one
// FD, a Unix socket that keeps message boundaries, where each message is one
// IP packet without a link header. darwin has no AF_PACKET sockets, and no
// SOCK_SEQPACKET Unix sockets, so the socket is SOCK_DGRAM.

// queueLen is the number of outbound packets that an endpoint holds.
const queueLen = 1024

// endpoint is a channel.Endpoint with goroutines that copy packets between
// it and the socket.
type endpoint struct {
	*channel.Endpoint
	conn   *net.UnixConn
	cancel context.CancelFunc
}

// New creates a new fd-based endpoint.
func New(opts *Options) (stack.LinkEndpoint, error) {
	if len(opts.FDs) != 1 {
		return nil, fmt.Errorf("fdbased: %d FDs: darwin supports one", len(opts.FDs))
	}
	if opts.EthernetHeader {
		return nil, fmt.Errorf("fdbased: darwin does not support Ethernet links")
	}
	if opts.PacketDispatchMode != Readv {
		return nil, fmt.Errorf("fdbased: dispatch mode %s: darwin supports %s", opts.PacketDispatchMode, Readv)
	}
	f := os.NewFile(uintptr(opts.FDs[0]), "fdbased")
	pc, err := net.FilePacketConn(f)
	f.Close()
	if err != nil {
		return nil, fmt.Errorf("fdbased: %w", err)
	}
	conn, ok := pc.(*net.UnixConn)
	if !ok {
		pc.Close()
		return nil, fmt.Errorf("fdbased: FD is %T, not a Unix socket", pc)
	}
	ch := channel.New(queueLen, opts.MTU, "")
	if opts.RXChecksumOffload {
		ch.LinkEPCapabilities |= stack.CapabilityRXChecksumOffload
	}
	if opts.TXChecksumOffload {
		ch.LinkEPCapabilities |= stack.CapabilityTXChecksumOffload
	}
	ctx, cancel := context.WithCancel(context.Background())
	e := &endpoint{Endpoint: ch, conn: conn, cancel: cancel}
	go e.writeLoop(ctx, opts.ClosedFunc)
	go e.readLoop(opts.MTU, opts.ClosedFunc)
	return e, nil
}

// Close implements stack.LinkEndpoint.Close.
func (e *endpoint) Close() {
	e.cancel()
	e.conn.Close()
	e.Endpoint.Close()
}

// closed reports the error of the socket to closedFunc, if not nil.
func closed(closedFunc func(tcpip.Error), err error) {
	if errors.Is(err, net.ErrClosed) {
		return
	}
	if errors.Is(err, unix.ECONNRESET) || errors.Is(err, unix.EPIPE) || errors.Is(err, unix.ENOTCONN) {
		// The peer exited.
		log.Debugf("fdbased: %v", err)
	} else {
		log.Warningf("fdbased: %v", err)
	}
	if closedFunc != nil {
		closedFunc(&tcpip.ErrConnectionAborted{})
	}
}

// writeLoop sends the outbound packets of e, one message per packet.
func (e *endpoint) writeLoop(ctx context.Context, closedFunc func(tcpip.Error)) {
	for {
		pkt := e.ReadContext(ctx)
		if pkt == nil {
			return
		}
		v := pkt.ToView()
		pkt.DecRef()
		_, err := e.conn.Write(v.AsSlice())
		v.Release()
		if err != nil {
			closed(closedFunc, err)
			return
		}
	}
}

// readLoop delivers each message on the socket as an inbound IP packet.
func (e *endpoint) readLoop(mtu uint32, closedFunc func(tcpip.Error)) {
	buf := make([]byte, mtu)
	for {
		n, err := e.conn.Read(buf)
		if err != nil {
			closed(closedFunc, err)
			return
		}
		var proto tcpip.NetworkProtocolNumber
		switch header.IPVersion(buf[:n]) {
		case header.IPv4Version:
			proto = header.IPv4ProtocolNumber
		case header.IPv6Version:
			proto = header.IPv6ProtocolNumber
		default:
			continue
		}
		pkt := stack.NewPacketBuffer(stack.PacketBufferOptions{
			Payload: buffer.MakeWithData(append([]byte(nil), buf[:n]...)),
		})
		e.InjectInbound(proto, pkt)
		pkt.DecRef()
	}
}

// IsPacketSocket returns false: darwin has no AF_PACKET sockets.
func IsPacketSocket(fd int, isSocket bool) (bool, error) {
	return false, nil
}

// CreatePacketFanoutGroup returns an error: darwin has no AF_PACKET sockets.
func CreatePacketFanoutGroup(fd int) (int32, error) {
	return 0, fmt.Errorf("fdbased: packet fanout is not supported on darwin")
}

// JoinPacketFanoutGroup returns an error: darwin has no AF_PACKET sockets.
func JoinPacketFanoutGroup(fd int, fID int32) error {
	return fmt.Errorf("fdbased: packet fanout is not supported on darwin")
}

// IsSocketFD returns true if fd is a socket.
func IsSocketFD(fd int) (bool, error) {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return false, fmt.Errorf("unix.Fstat(%v,...) failed: %v", fd, err)
	}
	return (stat.Mode & unix.S_IFMT) == unix.S_IFSOCK, nil
}
