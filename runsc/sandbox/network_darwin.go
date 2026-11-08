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

package sandbox

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"

	"golang.org/x/sys/unix"
	"gvisor.dev/gvisor/pkg/log"
	"gvisor.dev/gvisor/pkg/urpc"
	"gvisor.dev/gvisor/runsc/boot/sentryapi"
	"gvisor.dev/gvisor/runsc/config"
	"gvisor.dev/gvisor/runsc/gvisorbinaries"
)

// darwin has no network namespaces or veth devices to copy the network of
// the sandbox from. With --network=sandbox, the only network access of the
// Sentry is one packet link to a network proxy process (the runsc-netgofer
// sidecar), as with --network-proxy-path on Linux. The proxy ends the TCP and
// UDP flows of the sandbox in its own netstack and makes host connections
// for them.
//
// The link is a SOCK_DGRAM Unix socket pair: darwin has no SOCK_SEQPACKET
// for Unix sockets. Each datagram is one IP packet; there is no link header.

const (
	// proxyLinkMTU is the MTU of the proxy link. The proxy ends every TCP
	// connection, so the MTU of the host network does not apply.
	proxyLinkMTU = 65520

	// Socket buffer sizes of the proxy link. darwin limits a datagram on a
	// Unix socket to the send buffer size, 2 KiB by default.
	proxyLinkSendBuffer    = 1 << 20
	proxyLinkReceiveBuffer = 4 << 20

	proxyLinkName = "eth0"
)

// Addresses of the sandbox network, as in QEMU user networking.
var (
	proxySandboxAddr = net.IPv4(10, 0, 2, 15).To4()
	proxyGatewayAddr = net.IPv4(10, 0, 2, 2).To4()
	proxyPrefixLen   = 24
)

// setupNetwork configures the network stack of the sandbox. See above.
func setupNetwork(conn *urpc.Client, pid int, conf *config.Config, disableIPv6 bool, isRestore bool) error {
	log.Infof("Setting up network")
	switch conf.Network {
	case config.NetworkNone:
		log.Infof("Network is disabled, create loopback interface only")
		if err := createDefaultLoopbackInterface(conf, conn, isRestore); err != nil {
			return fmt.Errorf("creating default loopback interface: %v", err)
		}
	case config.NetworkSandbox:
		if conf.UseNetworkProxy() {
			return fmt.Errorf("--network-proxy-path is not supported on darwin; runsc starts its own network proxy")
		}
		if err := createProxyLink(conn, pid, conf, isRestore); err != nil {
			return fmt.Errorf("creating the network proxy link: %w", err)
		}
	default:
		return fmt.Errorf("network type %v is not supported on darwin", conf.Network)
	}
	return nil
}

// createProxyLink starts the network proxy for the sandbox process pid and
// gives the Sentry its link.
func createProxyLink(conn *urpc.Client, pid int, conf *config.Config, isRestore bool) error {
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_DGRAM, 0)
	if err != nil {
		return err
	}
	sentryEnd := os.NewFile(uintptr(fds[0]), "sentry network link")
	proxyEnd := os.NewFile(uintptr(fds[1]), "proxy network link")
	defer sentryEnd.Close()
	defer proxyEnd.Close()
	for _, fd := range fds {
		unix.CloseOnExec(fd)
		if err := unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_SNDBUF, proxyLinkSendBuffer); err != nil {
			return fmt.Errorf("setting the send buffer of the network link: %w", err)
		}
		if err := unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_RCVBUF, proxyLinkReceiveBuffer); err != nil {
			return fmt.Errorf("setting the receive buffer of the network link: %w", err)
		}
	}
	if err := startNetworkProxy(conf, pid, proxyEnd); err != nil {
		return err
	}

	subnet := net.IPNet{IP: proxySandboxAddr.Mask(net.CIDRMask(proxyPrefixLen, 32)), Mask: net.CIDRMask(proxyPrefixLen, 32)}
	loopback := sentryapi.DefaultLoopbackLink
	loopback.GVisorGRO = conf.GVisorGRO
	link := sentryapi.FDBasedLink{
		Name:              proxyLinkName,
		MTU:               proxyLinkMTU,
		Addresses:         []sentryapi.IPWithPrefix{{Address: proxySandboxAddr, PrefixLen: proxyPrefixLen}},
		Routes:            []sentryapi.Route{{Destination: subnet}},
		NumChannels:       conf.NumNetworkChannels,
		TXChecksumOffload: conf.TXChecksumOffload,
		RXChecksumOffload: conf.RXChecksumOffload,
	}
	configureProxyLink(&link)
	args := sentryapi.CreateLinksAndRoutesArgs{
		LoopbackLinks: []sentryapi.LoopbackLink{loopback},
		FDBasedLinks:  []sentryapi.FDBasedLink{link},
		Defaultv4Gateway: sentryapi.DefaultRoute{
			Route: sentryapi.Route{
				Destination: net.IPNet{IP: net.IPv4zero.To4(), Mask: net.CIDRMask(0, 32)},
				Gateway:     proxyGatewayAddr,
			},
			Name: proxyLinkName,
		},
		IsRestore:  isRestore,
		LogPackets: conf.LogPackets,
	}
	args.FilePayload.Files = []*os.File{sentryEnd}
	if err := conn.Call(sentryapi.ContMgrSetNetworkArgs, &args, nil); err != nil {
		return fmt.Errorf("creating links and routes: %w", err)
	}
	return nil
}

// startNetworkProxy starts the network proxy sidecar on link for the sandbox
// process pid. The proxy exits when the sandbox process exits.
func startNetworkProxy(conf *config.Config, pid int, link *os.File) error {
	path, err := gvisorbinaries.Netgofer.Path()
	if err != nil {
		return fmt.Errorf("sidecar %q not usable: %w", gvisorbinaries.Netgofer.Name, err)
	}
	args := []string{"--root=" + conf.RootDir}
	if conf.Debug {
		args = append(args, "--debug")
	}
	if conf.DebugLog != "" {
		args = append(args, "--debug-log="+conf.DebugLog)
	}
	args = append(args, "netgofer",
		"--uds-fd=3",
		"--mtu="+strconv.Itoa(proxyLinkMTU),
		"--sandbox-pid="+strconv.Itoa(pid),
		"--sandbox-addr="+proxySandboxAddr.String(),
		"--gateway-addr="+proxyGatewayAddr.String(),
		"--prefix-len="+strconv.Itoa(proxyPrefixLen),
	)
	cmd := exec.Command(path, args...)
	cmd.Args[0] = "runsc-netgofer"
	cmd.ExtraFiles = []*os.File{link}
	cmd.Env = os.Environ()
	// Detach from this session: the proxy outlives `runsc start`.
	cmd.SysProcAttr = &unix.SysProcAttr{Setsid: true}
	log.Debugf("Starting network proxy: %s %v", path, cmd.Args)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting the network proxy: %w", err)
	}
	log.Infof("Network proxy started, PID: %d", cmd.Process.Pid)
	// Do not wait for the proxy. It is reparented to launchd when runsc
	// exits.
	go cmd.Wait()
	return nil
}
