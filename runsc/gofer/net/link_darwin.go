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

package net

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

// darwin has no network namespaces or veth devices. The only network access
// of the Sentry is a SOCK_DGRAM Unix socket pair to the netgofer (see
// runsc/sandbox/network_darwin.go). Each datagram is one IP packet; there is
// no link header.

const (
	// Socket buffer sizes of the packet link. darwin limits a datagram on a
	// Unix socket to the send buffer size, 2 KiB by default.
	linkSendBuffer    = 1 << 20
	linkReceiveBuffer = 4 << 20

	// linkQueueLen is the number of outbound packets that the link holds.
	linkQueueLen = 1024
)

// newPacketLink returns a link endpoint with the given MTU that sends and
// receives packets as datagrams on f, a SOCK_DGRAM Unix socket. The link stops when ctx is done
// or f fails.
func newPacketLink(ctx context.Context, f *os.File, mtu uint32) (*channel.Endpoint, error) {
	pc, err := net.FilePacketConn(f)
	f.Close()
	if err != nil {
		return nil, err
	}
	conn, ok := pc.(*net.UnixConn)
	if !ok {
		pc.Close()
		return nil, fmt.Errorf("packet link is %T, not a Unix socket", pc)
	}
	if err := conn.SetWriteBuffer(linkSendBuffer); err != nil {
		conn.Close()
		return nil, err
	}
	if err := conn.SetReadBuffer(linkReceiveBuffer); err != nil {
		conn.Close()
		return nil, err
	}
	ep := channel.New(linkQueueLen, mtu, "")
	go func() {
		defer conn.Close()
		for {
			pkt := ep.ReadContext(ctx)
			if pkt == nil {
				return
			}
			v := pkt.ToView()
			pkt.DecRef()
			_, err := conn.Write(v.AsSlice())
			v.Release()
			if err != nil {
				if errors.Is(err, unix.ECONNRESET) || errors.Is(err, unix.EPIPE) {
					// The other process exited.
					log.Debugf("Packet link write: %v", err)
				} else {
					log.Warningf("Packet link write: %v", err)
				}
				return
			}
		}
	}()
	go func() {
		buf := make([]byte, mtu)
		for {
			n, err := conn.Read(buf)
			if err != nil {
				log.Debugf("Packet link read: %v", err)
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
			ep.InjectInbound(proto, pkt)
			pkt.DecRef()
		}
	}()
	return ep, nil
}
