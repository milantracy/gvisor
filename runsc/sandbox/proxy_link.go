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

package sandbox

import (
	"fmt"

	"gvisor.dev/gvisor/pkg/log"
	"gvisor.dev/gvisor/pkg/urpc"
	"gvisor.dev/gvisor/runsc/boot/sentryapi"
	"gvisor.dev/gvisor/runsc/config"
)

// configureProxyLink configures link to operate over an external UDS proxy FD.
func configureProxyLink(link *sentryapi.FDBasedLink) {
	link.IsProxy = true
	if link.NumChannels != 1 {
		log.Warningf("Ignoring --num-network-channels=%d for interface %q: a network UDS link always uses a single channel", link.NumChannels, link.Name)
		link.NumChannels = 1
	}
	// A peer told not to verify checksums is assumed not to compute them
	// either, so accept packets with uncomputed L4 checksums. The offload
	// flags are part of the host-side configuration that the peer is
	// expected to be aligned with out-of-band.
	if link.TXChecksumOffload && !link.RXChecksumOffload {
		log.Infof("Enabling RX checksum offload on interface %q because TX checksum offload is enabled: the network UDS peer is expected to send packets without computed L4 checksums", link.Name)
		link.RXChecksumOffload = true
	}
}

func createDefaultLoopbackInterface(conf *config.Config, conn *urpc.Client, isRestore bool) error {
	link := sentryapi.DefaultLoopbackLink
	link.GVisorGRO = conf.GVisorGRO
	if err := conn.Call(sentryapi.ContMgrSetNetworkArgs, &sentryapi.CreateLinksAndRoutesArgs{
		LoopbackLinks: []sentryapi.LoopbackLink{link},
		IsRestore:     isRestore,
		LogPackets:    conf.LogPackets,
	}, nil); err != nil {
		return fmt.Errorf("creating loopback link and routes: %v", err)
	}
	return nil
}
