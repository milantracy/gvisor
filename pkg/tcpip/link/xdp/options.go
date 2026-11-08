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

package xdp

import "gvisor.dev/gvisor/pkg/tcpip"

// Options specify the details about the fd-based endpoint to be created.
type Options struct {
	// FD is used to read/write packets.
	FD int

	// ClosedFunc is a function to be called when an endpoint's peer (if
	// any) closes its end of the communication pipe.
	ClosedFunc func(tcpip.Error)

	// Address is the link address for this endpoint.
	Address tcpip.LinkAddress

	// SaveRestore if true, indicates that this NIC capability set should
	// include CapabilitySaveRestore
	SaveRestore bool

	// TXChecksumOffload if true, indicates that this endpoints capability
	// set should include CapabilityTXChecksumOffload.
	TXChecksumOffload bool

	// RXChecksumOffload if true, indicates that this endpoints capability
	// set should include CapabilityRXChecksumOffload.
	RXChecksumOffload bool

	// InterfaceIndex is the interface index of the underlying device.
	InterfaceIndex int

	// Bind is true when we're responsible for binding the AF_XDP socket to
	// a device. When false, another process is expected to bind for us.
	Bind bool

	// GRO enables generic receive offload.
	GRO bool

	// QueueID is the ID of the RX queue to which the AF_XDP socket is attached.
	QueueID uint32
}
