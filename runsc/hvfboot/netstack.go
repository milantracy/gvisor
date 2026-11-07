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
	"fmt"

	"gvisor.dev/gvisor/pkg/abi/linux"
	"gvisor.dev/gvisor/pkg/sentry/inet"
	"gvisor.dev/gvisor/pkg/sentry/kernel/auth"
	"gvisor.dev/gvisor/pkg/sentry/socket/netfilter"
	"gvisor.dev/gvisor/pkg/sentry/socket/netstack"
	"gvisor.dev/gvisor/pkg/sentry/uniqueid"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/link/ethernet"
	"gvisor.dev/gvisor/pkg/tcpip/link/loopback"
	"gvisor.dev/gvisor/pkg/tcpip/network/arp"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv6"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/icmp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/raw"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
)

// The Sentry network stack follows newEmptySandboxNetworkStack and
// sandboxNetstackCreator in runsc/boot/loader.go, without nftables.

const (
	loopbackNIC tcpip.NICID = linux.LOOPBACK_IFINDEX
	linkNIC     tcpip.NICID = loopbackNIC + 1
)

var (
	loopbackV4 = tcpip.AddressWithPrefix{Address: tcpip.AddrFrom4([4]byte{127, 0, 0, 1}), PrefixLen: 8}
	loopbackV6 = tcpip.AddressWithPrefix{Address: tcpip.AddrFrom16([16]byte{15: 1}), PrefixLen: 128}
)

// newNetstack returns a netstack with no NICs.
func newNetstack(uid uniqueid.Provider) (*netstack.Stack, error) {
	s := netstack.NewStack(stack.New(stack.Options{
		NetworkProtocols:   []stack.NetworkProtocolFactory{ipv4.NewProtocol, ipv6.NewProtocol, arp.NewProtocol},
		TransportProtocols: []stack.TransportProtocolFactory{tcp.NewProtocol, udp.NewProtocol, icmp.NewProtocol4, icmp.NewProtocol6},
		Clock:              tcpip.NewStdClock(),
		Stats:              netstack.Metrics,
		HandleLocal:        true,
		RawFactory:         raw.EndpointFactory{},
		DefaultIPTables:    netfilter.DefaultLinuxTables,
	}), uid.UniqueID())
	sack := tcpip.TCPSACKEnabled(true)
	if err := s.Stack.SetTransportProtocolOption(tcp.ProtocolNumber, &sack); err != nil {
		return nil, fmt.Errorf("enabling TCP SACK: %s", err)
	}
	ttl := tcpip.DefaultTTLOption(netstack.DefaultTTL)
	for _, proto := range []tcpip.NetworkProtocolNumber{ipv4.ProtocolNumber, ipv6.ProtocolNumber} {
		if err := s.Stack.SetNetworkProtocolOption(proto, &ttl); err != nil {
			return nil, fmt.Errorf("setting the default TTL: %s", err)
		}
	}
	moderate := tcpip.TCPModerateReceiveBufferOption(true)
	if err := s.Stack.SetTransportProtocolOption(tcp.ProtocolNumber, &moderate); err != nil {
		return nil, fmt.Errorf("enabling TCP receive buffer auto-tuning: %s", err)
	}
	return s, nil
}

// addNIC adds a NIC with addrs to s.
func addNIC(s *stack.Stack, id tcpip.NICID, name string, ep stack.LinkEndpoint, addrs ...tcpip.AddressWithPrefix) error {
	if err := s.CreateNICWithOptions(id, ep, stack.NICOptions{Name: name, DeliverLinkPackets: true}); err != nil {
		return fmt.Errorf("creating NIC %s: %s", name, err)
	}
	for _, addr := range addrs {
		proto := ipv4.ProtocolNumber
		if addr.Address.Len() == header.IPv6AddressSize {
			proto = ipv6.ProtocolNumber
		}
		pa := tcpip.ProtocolAddress{Protocol: proto, AddressWithPrefix: addr}
		if err := s.AddProtocolAddress(id, pa, stack.AddressProperties{}); err != nil {
			return fmt.Errorf("adding %s to NIC %s: %s", addr, name, err)
		}
	}
	return nil
}

// newLoopbackNetstack returns a netstack with only a loopback NIC.
func newLoopbackNetstack(uid uniqueid.Provider) (*netstack.Stack, error) {
	s, err := newNetstack(uid)
	if err != nil {
		return nil, err
	}
	if err := addNIC(s.Stack, loopbackNIC, "lo", ethernet.New(loopback.New()), loopbackV4, loopbackV6); err != nil {
		return nil, err
	}
	s.Stack.SetRouteTable([]tcpip.Route{
		{Destination: loopbackV4.Subnet(), NIC: loopbackNIC},
		{Destination: loopbackV6.Subnet(), NIC: loopbackNIC},
	})
	return s, nil
}

// loopbackStackCreator implements kernel.NetworkStackCreator. A new network
// namespace has only a loopback NIC, as in runsc.
type loopbackStackCreator struct {
	uid uniqueid.Provider
}

// CreateStack implements kernel.NetworkStackCreator.CreateStack.
func (c *loopbackStackCreator) CreateStack() (inet.Stack, error) {
	return newLoopbackNetstack(c.uid)
}

// newRootNetworkNamespace returns the root network namespace: a loopback NIC
// and, if link is not nil, an eth0 NIC on link with the default route.
func newRootNetworkNamespace(uid uniqueid.Provider, userns *auth.UserNamespace, link stack.LinkEndpoint) (*inet.Namespace, error) {
	s, err := newLoopbackNetstack(uid)
	if err != nil {
		return nil, err
	}
	if link != nil {
		if err := addNIC(s.Stack, linkNIC, "eth0", link, sandboxSubnet); err != nil {
			return nil, err
		}
		s.Stack.AddRoute(tcpip.Route{Destination: sandboxSubnet.Subnet(), NIC: linkNIC})
		s.Stack.AddRoute(tcpip.Route{Destination: header.IPv4EmptySubnet, Gateway: gatewayAddr, NIC: linkNIC})
	}
	return inet.NewRootNamespace(s, &loopbackStackCreator{uid: uid}, userns), nil
}
