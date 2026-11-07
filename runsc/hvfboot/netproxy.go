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

package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
	"gvisor.dev/gvisor/pkg/log"
	"gvisor.dev/gvisor/pkg/seatbelt"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
	"gvisor.dev/gvisor/pkg/waiter"
)

// `hvfboot net` is the network proxy of the sandbox. It does the job of the
// host kernel for runsc: it receives the IP packets of the sandbox on the
// packet link, ends their TCP and UDP flows in its own netstack, and makes a
// host connection for each flow:
//   - TCP and UDP to port 53 of any address go to the host DNS server (the
//     first nameserver in the host /etc/resolv.conf), so that the DNS
//     configuration of the host applies.
//   - Other TCP and UDP go to their destination.
//   - Destinations on the host itself (loopback, the unspecified address, and
//     by the Seatbelt profile, the addresses of host interfaces) and in the
//     sandbox network are refused.
//
// Only IPv4 TCP and UDP are supported. The proxy exits when the lifeline FD
// closes, which happens when the Sentry exits.

const (
	// udpIdleTimeout is the time after which a UDP flow with no packets in
	// either direction is closed.
	udpIdleTimeout = 60 * time.Second

	// dialTimeout is the timeout of a host TCP connection.
	dialTimeout = 30 * time.Second

	// tcpReceiveWindow and tcpMaxInFlight configure the TCP forwarder.
	tcpReceiveWindow = 0 // The default.
	tcpMaxInFlight   = 1024
)

// netProfile is the Seatbelt profile of the network proxy. It denies all
// file access, exec and IPC, and allows only outbound IP connections, except
// to the host itself: for Seatbelt, "localhost" is every address of the host,
// also the addresses of its network interfaces. The proxy refuses loopback
// destinations itself too.
const netProfile = `(version 1)
(deny default)
(allow network-outbound (remote ip "*:*"))
(deny network-outbound (remote ip "localhost:*"))
`

// netMain is `hvfboot net`.
func netMain(args []string) int {
	fs := flag.NewFlagSet("net", flag.ExitOnError)
	linkFD := fs.Int("link-fd", -1, "SOCK_DGRAM socket of the packet link")
	lifelineFD := fs.Int("lifeline-fd", -1, "socket that closes when the Sentry exits")
	debug := fs.Bool("debug", false, "log at debug level")
	fs.Parse(args)
	if *debug {
		log.SetLevel(log.Debug)
	}
	if *linkFD < 0 || *lifelineFD < 0 {
		fmt.Fprintf(os.Stderr, "hvfboot net: -link-fd and -lifeline-fd are required\n")
		return 2
	}
	dns, err := hostDNSServer("/etc/resolv.conf")
	if err != nil {
		log.Warningf("No host DNS server, DNS in the sandbox does not work: %v", err)
	}
	// Go reads /etc/localtime on first use, which the profile denies.
	_ = time.Local.String()
	if err := seatbelt.Init(netProfile); err != nil {
		fmt.Fprintf(os.Stderr, "hvfboot net: %v\n", err)
		return 1
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ep, err := newPacketLink(ctx, os.NewFile(uintptr(*linkFD), "packet link"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "hvfboot net: %v\n", err)
		return 1
	}
	s := stack.New(stack.Options{
		NetworkProtocols:   []stack.NetworkProtocolFactory{ipv4.NewProtocol},
		TransportProtocols: []stack.TransportProtocolFactory{tcp.NewProtocol, udp.NewProtocol},
	})
	defer s.Destroy()
	const nic = 1
	if err := s.CreateNIC(nic, ep); err != nil {
		fmt.Fprintf(os.Stderr, "hvfboot net: creating the NIC: %s\n", err)
		return 1
	}
	// Accept packets for any address, and send replies from it.
	if err := s.SetPromiscuousMode(nic, true); err != nil {
		fmt.Fprintf(os.Stderr, "hvfboot net: %s\n", err)
		return 1
	}
	if err := s.SetSpoofing(nic, true); err != nil {
		fmt.Fprintf(os.Stderr, "hvfboot net: %s\n", err)
		return 1
	}
	s.SetRouteTable([]tcpip.Route{{Destination: header.IPv4EmptySubnet, NIC: nic}})

	p := &proxy{dns: dns}
	tcpFwd := tcp.NewForwarder(s, tcpReceiveWindow, tcpMaxInFlight, p.handleTCP)
	s.SetTransportProtocolHandler(tcp.ProtocolNumber, tcpFwd.HandlePacket)
	udpFwd := udp.NewForwarder(s, p.handleUDP)
	s.SetTransportProtocolHandler(udp.ProtocolNumber, udpFwd.HandlePacket)

	// Wait for the Sentry to exit.
	lifeline := os.NewFile(uintptr(*lifelineFD), "lifeline")
	io.Copy(io.Discard, lifeline)
	return 0
}

// hostDNSServer returns the first nameserver in the resolv.conf at path.
func hostDNSServer(path string) (netip.Addr, error) {
	f, err := os.Open(path)
	if err != nil {
		return netip.Addr{}, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 2 || fields[0] != "nameserver" {
			continue
		}
		if addr, err := netip.ParseAddr(fields[1]); err == nil {
			return addr, nil
		}
	}
	if err := sc.Err(); err != nil {
		return netip.Addr{}, err
	}
	return netip.Addr{}, fmt.Errorf("no nameserver in %s", path)
}

// proxy makes host connections for the flows of the sandbox.
type proxy struct {
	// dns is the host DNS server. It is not valid if the host has none.
	dns netip.Addr
}

// sandboxPrefix is the sandbox network.
var sandboxPrefix = netip.PrefixFrom(netip.AddrFrom4(sandboxAddr.As4()), sandboxSubnet.PrefixLen).Masked()

// errRefused is the error for a destination that the proxy refuses.
var errRefused = errors.New("destination refused")

// hostAddr returns the host address for the destination id of a flow of the
// sandbox.
func (p *proxy) hostAddr(id stack.TransportEndpointID) (netip.AddrPort, error) {
	dst, ok := netip.AddrFromSlice(id.LocalAddress.AsSlice())
	if !ok {
		return netip.AddrPort{}, errRefused
	}
	if id.LocalPort == 53 {
		if !p.dns.IsValid() {
			return netip.AddrPort{}, errRefused
		}
		return netip.AddrPortFrom(p.dns, 53), nil
	}
	if dst.IsLoopback() || dst.IsUnspecified() || sandboxPrefix.Contains(dst) {
		return netip.AddrPort{}, errRefused
	}
	return netip.AddrPortFrom(dst, id.LocalPort), nil
}

// handleTCP handles a new TCP connection of the sandbox.
func (p *proxy) handleTCP(r *tcp.ForwarderRequest) {
	id := r.ID()
	addr, err := p.hostAddr(id)
	if err != nil {
		log.Debugf("TCP %s:%d: %v", id.LocalAddress, id.LocalPort, err)
		r.Complete(true /* sendReset */)
		return
	}
	host, err := net.DialTimeout("tcp", addr.String(), dialTimeout)
	if err != nil {
		log.Debugf("TCP %s: %v", addr, err)
		r.Complete(true /* sendReset */)
		return
	}
	var wq waiter.Queue
	ep, tcpErr := r.CreateEndpoint(&wq)
	if tcpErr != nil {
		log.Debugf("TCP %s: creating the endpoint: %s", addr, tcpErr)
		r.Complete(true /* sendReset */)
		host.Close()
		return
	}
	r.Complete(false /* sendReset */)
	sandbox := gonet.NewTCPConn(&wq, ep)
	relayTCP(sandbox, host.(*net.TCPConn))
}

// relayTCP copies data between the connections in both directions, and
// closes them when both directions are done.
func relayTCP(sandbox *gonet.TCPConn, host *net.TCPConn) {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		io.Copy(host, sandbox)
		host.CloseWrite()
	}()
	io.Copy(sandbox, host)
	sandbox.CloseWrite()
	wg.Wait()
	sandbox.Close()
	host.Close()
}

// handleUDP handles the first packet of a new UDP flow of the sandbox.
func (p *proxy) handleUDP(r *udp.ForwarderRequest) bool {
	id := r.ID()
	addr, err := p.hostAddr(id)
	if err != nil {
		log.Debugf("UDP %s:%d: %v", id.LocalAddress, id.LocalPort, err)
		return true // Drop the packet.
	}
	host, err := net.DialUDP("udp", nil, net.UDPAddrFromAddrPort(addr))
	if err != nil {
		log.Debugf("UDP %s: %v", addr, err)
		return true
	}
	var wq waiter.Queue
	ep, tcpErr := r.CreateEndpoint(&wq)
	if tcpErr != nil {
		log.Debugf("UDP %s: creating the endpoint: %s", addr, tcpErr)
		host.Close()
		return true
	}
	go relayUDP(gonet.NewUDPConn(&wq, ep), host)
	return true
}

// relayUDP copies datagrams between the connections in both directions, until
// the flow is idle for udpIdleTimeout.
func relayUDP(sandbox *gonet.UDPConn, host *net.UDPConn) {
	defer sandbox.Close()
	defer host.Close()
	var (
		mu       sync.Mutex
		lastUsed = time.Now()
	)
	touch := func() {
		mu.Lock()
		lastUsed = time.Now()
		mu.Unlock()
	}
	idle := func() bool {
		mu.Lock()
		defer mu.Unlock()
		return time.Since(lastUsed) >= udpIdleTimeout
	}
	copyDatagrams := func(dst, src net.Conn) {
		buf := make([]byte, 65535)
		for {
			src.SetReadDeadline(time.Now().Add(udpIdleTimeout))
			n, err := src.Read(buf)
			if err != nil {
				var ne net.Error
				if errors.As(err, &ne) && ne.Timeout() && !idle() {
					continue
				}
				// Stop the other direction too.
				dst.SetReadDeadline(time.Now())
				return
			}
			touch()
			if _, err := dst.Write(buf[:n]); err != nil {
				log.Debugf("UDP write: %v", err)
			}
		}
	}
	done := make(chan struct{})
	go func() {
		copyDatagrams(host, sandbox)
		close(done)
	}()
	copyDatagrams(sandbox, host)
	<-done
}

// startNetProxy starts `hvfboot net`, and returns the Sentry end of the packet
// link. The proxy exits when the Sentry exits.
func startNetProxy(debug bool) (*os.File, error) {
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	link, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_DGRAM, 0)
	if err != nil {
		return nil, err
	}
	lifeline, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		unix.Close(link[0])
		unix.Close(link[1])
		return nil, err
	}
	for _, fd := range []int{link[0], link[1], lifeline[0], lifeline[1]} {
		unix.CloseOnExec(fd)
	}
	sentryLink := os.NewFile(uintptr(link[0]), "packet link")
	proxyLink := os.NewFile(uintptr(link[1]), "packet link")
	proxyLifeline := os.NewFile(uintptr(lifeline[1]), "lifeline")
	defer proxyLink.Close()
	defer proxyLifeline.Close()
	// The Sentry keeps its end of the lifeline open until it exits. It is a
	// raw FD, so no finalizer closes it.

	cmd := exec.Command(self, "net", "-link-fd", "3", "-lifeline-fd", "4")
	if debug {
		cmd.Args = append(cmd.Args, "-debug")
	}
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	cmd.ExtraFiles = []*os.File{proxyLink, proxyLifeline}
	if err := cmd.Start(); err != nil {
		sentryLink.Close()
		return nil, fmt.Errorf("starting the network proxy: %w", err)
	}
	return sentryLink, nil
}
