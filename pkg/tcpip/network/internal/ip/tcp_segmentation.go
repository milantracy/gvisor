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

package ip

import (
	"fmt"

	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/tcpip/checksum"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/seqnum"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
)

// TCPSegmenter splits a TCP packet that is too large for the outgoing link
// into segments that fit.
//
// This is the software equivalent of what a NIC does for a GSO packet, and
// exists for packets the stack did not generate itself. A forwarded packet can
// exceed the outgoing MTU whenever the receiving host coalesced segments on the
// way in (GRO), and because such packets carry DF they can be neither sent
// as-is nor fragmented. Segmenting is the only way to forward them; the
// alternative is to drop them, which strands the sender in RTO recovery.
//
// Unlike a fragmenter, each segment produced here is a complete TCP packet: the
// TCP header is repeated with an adjusted sequence number rather than carried
// only on the first piece.
type TCPSegmenter struct {
	// data is the TCP payload left to segment. It excludes the TCP header,
	// which is repeated on every segment rather than split across them.
	data buffer.Buffer

	// tcpHeader is the original TCP header, used as the template for every
	// segment's header.
	tcpHeader header.TCP

	reserve      int
	mss          int
	segmentCount int

	currentSegment int
	sequenceNumber seqnum.Value
	mark           uint32
}

// MakeTCPSegmenter prepares to split pkt into segments carrying at most mss
// bytes of TCP payload each. reserve is the number of bytes to reserve for the
// network and link headers of each segment, on top of the TCP header.
//
// pkt must have had its transport header parsed.
func MakeTCPSegmenter(pkt *stack.PacketBuffer, mss int, reserve int) TCPSegmenter {
	tcpHeader := header.TCP(pkt.TransportHeader().Slice())

	var data buffer.Buffer
	pktBuf := pkt.Data().ToBuffer()
	data.Merge(&pktBuf)

	// A zero-length payload still needs one segment: the caller only reaches
	// here for oversized packets, but rounding must not produce zero.
	segmentCount := (data.Size() + int64(mss) - 1) / int64(mss)
	if segmentCount == 0 {
		segmentCount = 1
	}

	return TCPSegmenter{
		data:           data,
		tcpHeader:      tcpHeader,
		reserve:        reserve + len(tcpHeader),
		mss:            mss,
		segmentCount:   int(segmentCount),
		sequenceNumber: seqnum.Value(tcpHeader.SequenceNumber()),
		mark:           pkt.Mark,
	}
}

// BuildNextSegment returns the next segment along with the number of payload
// bytes it carries and whether more segments remain. Calling it again after it
// reported no more segments panics.
//
// The returned packet has its TCP header populated and its network and link
// headers reserved but empty; the caller is responsible for pushing the network
// header and for the transport checksum.
func (ts *TCPSegmenter) BuildNextSegment() (*stack.PacketBuffer, int, bool) {
	if ts.currentSegment >= ts.segmentCount {
		panic("BuildNextSegment should not be called again after the last segment was returned")
	}

	segPkt := stack.NewPacketBuffer(stack.PacketBufferOptions{
		ReserveHeaderBytes: ts.reserve,
		Mark:               ts.mark,
	})

	copied := segPkt.Data().ReadFrom(&ts.data, ts.mss)

	ts.currentSegment++
	more := ts.currentSegment != ts.segmentCount

	segHeader := header.TCP(segPkt.TransportHeader().Push(len(ts.tcpHeader)))
	if n := copy(segHeader, ts.tcpHeader); n != len(ts.tcpHeader) {
		panic(fmt.Sprintf("wrong number of bytes copied into the segment's TCP header: got = %d, want = %d", n, len(ts.tcpHeader)))
	}
	segHeader.SetSequenceNumber(uint32(ts.sequenceNumber))
	ts.sequenceNumber = ts.sequenceNumber.Add(seqnum.Size(copied))

	if more {
		// FIN and PSH describe the end of the original packet's data, so they
		// belong only on the last segment. Leaving FIN on an earlier one would
		// close the connection early; leaving PSH on every one defeats the
		// receiver's ability to batch.
		segHeader.SetFlags(uint8(segHeader.Flags() &^ (header.TCPFlagFin | header.TCPFlagPsh)))
	}

	segPkt.TransportProtocolNumber = header.TCPProtocolNumber

	return segPkt, copied, more
}

// RemainingSegmentCount returns the number of segments left to be built.
func (ts *TCPSegmenter) RemainingSegmentCount() int {
	return ts.segmentCount - ts.currentSegment
}

// Release frees resources owned by the segmenter.
func (ts *TCPSegmenter) Release() {
	ts.data.Release()
}

// SetTCPChecksum computes and sets the TCP checksum of a segment built by a
// TCPSegmenter. pseudoHeaderChecksum is the checksum of this segment's
// pseudo-header, which only the network layer can compute.
//
// The checksum is always computed, even when the outgoing link offloads it. A
// segment inherits the original packet's checksum, which covered a payload it
// no longer carries, so leaving it in place would put a wrong checksum on the
// wire for any link that does not offload.
func SetTCPChecksum(pkt *stack.PacketBuffer, pseudoHeaderChecksum uint16) {
	tcpHeader := header.TCP(pkt.TransportHeader().Slice())
	tcpHeader.SetChecksum(0)
	xsum := checksum.Combine(pseudoHeaderChecksum, pkt.Data().Checksum())
	tcpHeader.SetChecksum(^tcpHeader.CalculateChecksum(xsum))
}

// TCPSegmentable reports whether pkt is a TCP packet that can be split by a
// TCPSegmenter: the transport header must have been parsed, and there must be
// more than one segment's worth of payload to split.
func TCPSegmentable(pkt *stack.PacketBuffer, mss int) bool {
	if mss <= 0 {
		return false
	}
	if pkt.TransportProtocolNumber != header.TCPProtocolNumber {
		return false
	}
	tcpHeader := header.TCP(pkt.TransportHeader().Slice())
	if len(tcpHeader) < header.TCPMinimumSize {
		return false
	}
	// Splitting a SYN or a segment carrying urgent data would require care we
	// do not take here, and neither can legitimately be oversized.
	if tcpHeader.Flags()&(header.TCPFlagSyn|header.TCPFlagRst|header.TCPFlagUrg) != 0 {
		return false
	}
	return pkt.Data().Size() > mss
}
