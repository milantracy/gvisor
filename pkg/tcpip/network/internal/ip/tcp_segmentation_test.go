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

package ip_test

import (
	"bytes"
	"testing"

	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/checksum"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/network/internal/ip"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
)

const (
	testSrcPort = 1234
	testDstPort = 5678
	testSeq     = 0xfffffe00 // Close enough to wrapping to exercise it.
	testReserve = 40         // Room a caller would leave for network/link headers.
)

var (
	testSrcAddr = tcpip.AddrFrom4([4]byte{192, 0, 2, 1})
	testDstAddr = tcpip.AddrFrom4([4]byte{192, 0, 2, 2})
)

// testPayload returns a payload whose every byte is distinguishable from its
// neighbours, so that a segment carrying the wrong slice of it is detectable.
func testPayload(size int) []byte {
	payload := make([]byte, size)
	for i := range payload {
		payload[i] = byte(i)
	}
	return payload
}

// makeTCPPacket returns a packet holding a parsed TCP header with the given
// flags and options followed by payload, as writePacketPostRouting would see a
// forwarded packet.
func makeTCPPacket(flags header.TCPFlags, options []byte, payload []byte) *stack.PacketBuffer {
	hdrLen := header.TCPMinimumSize + len(options)
	pkt := stack.NewPacketBuffer(stack.PacketBufferOptions{
		ReserveHeaderBytes: testReserve + hdrLen,
		Payload:            buffer.MakeWithData(payload),
	})
	pkt.TransportProtocolNumber = header.TCPProtocolNumber

	tcpHeader := header.TCP(pkt.TransportHeader().Push(hdrLen))
	tcpHeader.Encode(&header.TCPFields{
		SrcPort:    testSrcPort,
		DstPort:    testDstPort,
		SeqNum:     testSeq,
		AckNum:     1,
		DataOffset: uint8(hdrLen),
		Flags:      flags,
		WindowSize: 65535,
	})
	copy(tcpHeader[header.TCPMinimumSize:], options)
	return pkt
}

// segment is the flattened form of a packet built by a TCPSegmenter, so that
// tests can compare whole segments rather than poke at packet buffers.
type segment struct {
	seq     uint32
	flags   header.TCPFlags
	options []byte
	payload []byte
}

// segmentAll drains ts, returning every segment it produces. It fails the test
// if the reported payload sizes or remaining counts are inconsistent.
func segmentAll(t *testing.T, ts *ip.TCPSegmenter) []segment {
	t.Helper()

	var segments []segment
	for {
		want := ts.RemainingSegmentCount()
		segPkt, copied, more := ts.BuildNextSegment()
		if got := ts.RemainingSegmentCount(); got != want-1 {
			t.Errorf("after building segment %d, RemainingSegmentCount() = %d, want = %d", len(segments), got, want-1)
		}

		tcpHeader := header.TCP(segPkt.TransportHeader().Slice())
		payload := segPkt.Data().AsRange().ToSlice()
		if len(payload) != copied {
			t.Errorf("segment %d: BuildNextSegment() reported %d payload bytes, but the segment carries %d", len(segments), copied, len(payload))
		}
		if got, want := segPkt.AvailableHeaderBytes(), testReserve; got != want {
			t.Errorf("segment %d: AvailableHeaderBytes() = %d, want = %d", len(segments), got, want)
		}
		segments = append(segments, segment{
			seq:     tcpHeader.SequenceNumber(),
			flags:   tcpHeader.Flags(),
			options: append([]byte(nil), tcpHeader[header.TCPMinimumSize:]...),
			payload: payload,
		})
		segPkt.DecRef()

		if !more {
			if got := ts.RemainingSegmentCount(); got != 0 {
				t.Errorf("after the last segment, RemainingSegmentCount() = %d, want = 0", got)
			}
			return segments
		}
	}
}

func TestTCPSegmenter(t *testing.T) {
	const mss = 100

	tests := []struct {
		name        string
		payloadSize int
		flags       header.TCPFlags
		// wantSizes is the payload size of each expected segment.
		wantSizes []int
	}{
		{
			name:        "payload smaller than mss",
			payloadSize: 1,
			flags:       header.TCPFlagAck,
			wantSizes:   []int{1},
		},
		{
			name:        "payload exactly mss",
			payloadSize: mss,
			flags:       header.TCPFlagAck,
			wantSizes:   []int{mss},
		},
		{
			name:        "payload an exact multiple of mss",
			payloadSize: 3 * mss,
			flags:       header.TCPFlagAck,
			wantSizes:   []int{mss, mss, mss},
		},
		{
			name:        "payload with a short final segment",
			payloadSize: 2*mss + 7,
			flags:       header.TCPFlagAck,
			wantSizes:   []int{mss, mss, 7},
		},
		{
			name:        "flags that belong only on the last segment",
			payloadSize: 2 * mss,
			flags:       header.TCPFlagAck | header.TCPFlagPsh | header.TCPFlagFin,
			wantSizes:   []int{mss, mss},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			payload := testPayload(test.payloadSize)
			pkt := makeTCPPacket(test.flags, nil /* options */, payload)
			defer pkt.DecRef()

			ts := ip.MakeTCPSegmenter(pkt, mss, testReserve)
			defer ts.Release()

			segments := segmentAll(t, &ts)
			if len(segments) != len(test.wantSizes) {
				t.Fatalf("got %d segments, want = %d", len(segments), len(test.wantSizes))
			}

			var (
				gotPayload bytes.Buffer
				wantSeq    = uint32(testSeq)
			)
			for i, seg := range segments {
				last := i == len(segments)-1

				if len(seg.payload) != test.wantSizes[i] {
					t.Errorf("segment %d carries %d payload bytes, want = %d", i, len(seg.payload), test.wantSizes[i])
				}
				if seg.seq != wantSeq {
					t.Errorf("segment %d has sequence number %d, want = %d", i, seg.seq, wantSeq)
				}
				// FIN and PSH describe the end of the original packet's data, so
				// they belong on the last segment only. Every other flag is
				// repeated on all of them.
				wantFlags := test.flags
				if !last {
					wantFlags &^= header.TCPFlagFin | header.TCPFlagPsh
				}
				if seg.flags != wantFlags {
					t.Errorf("segment %d has flags %s, want = %s", i, seg.flags, wantFlags)
				}

				gotPayload.Write(seg.payload)
				wantSeq += uint32(len(seg.payload))
			}

			if !bytes.Equal(gotPayload.Bytes(), payload) {
				t.Errorf("the concatenated segment payloads do not reproduce the original payload")
			}
		})
	}
}

// TestTCPSegmenterPreservesOptions checks that a header longer than the minimum
// is repeated whole, since options such as timestamps must appear on every
// segment.
func TestTCPSegmenterPreservesOptions(t *testing.T) {
	const mss = 100

	options := make([]byte, header.TCPOptionTSLength+2 /* NOP padding */)
	header.EncodeTSOption(1, 2, options)
	payload := testPayload(2 * mss)
	pkt := makeTCPPacket(header.TCPFlagAck, options, payload)
	defer pkt.DecRef()

	ts := ip.MakeTCPSegmenter(pkt, mss, testReserve)
	defer ts.Release()

	segments := segmentAll(t, &ts)
	if len(segments) != 2 {
		t.Fatalf("got %d segments, want = 2", len(segments))
	}
	for i, seg := range segments {
		if !bytes.Equal(seg.options, options) {
			t.Errorf("segment %d has options %x, want = %x", i, seg.options, options)
		}
	}
}

// TestSetTCPChecksum checks that a segment's checksum covers the payload the
// segment actually carries, not the original packet's.
func TestSetTCPChecksum(t *testing.T) {
	const mss = 100

	payload := testPayload(2*mss + 7)
	pkt := makeTCPPacket(header.TCPFlagAck, nil /* options */, payload)
	defer pkt.DecRef()

	ts := ip.MakeTCPSegmenter(pkt, mss, testReserve)
	defer ts.Release()

	for i := 0; ; i++ {
		segPkt, _, more := ts.BuildNextSegment()
		ip.SetTCPChecksum(segPkt, header.PseudoHeaderChecksum(
			header.TCPProtocolNumber,
			testSrcAddr,
			testDstAddr,
			uint16(segPkt.Size()),
		))

		segPayload := segPkt.Data().AsRange().ToSlice()
		tcpHeader := header.TCP(segPkt.TransportHeader().Slice())
		if !tcpHeader.IsChecksumValid(testSrcAddr, testDstAddr, checksum.Checksum(segPayload, 0), uint16(len(segPayload))) {
			t.Errorf("segment %d has an invalid checksum", i)
		}
		segPkt.DecRef()

		if !more {
			break
		}
	}
}

func TestTCPSegmentable(t *testing.T) {
	const mss = 100

	tests := []struct {
		name string
		// pkt is built fresh per test case so that each gets its own refcount.
		pkt  func() *stack.PacketBuffer
		mss  int
		want bool
	}{
		{
			name: "oversized data segment",
			pkt:  func() *stack.PacketBuffer { return makeTCPPacket(header.TCPFlagAck, nil, testPayload(mss+1)) },
			mss:  mss,
			want: true,
		},
		{
			name: "payload fits in one segment",
			pkt:  func() *stack.PacketBuffer { return makeTCPPacket(header.TCPFlagAck, nil, testPayload(mss)) },
			mss:  mss,
			want: false,
		},
		{
			name: "mss leaves no room for payload",
			pkt:  func() *stack.PacketBuffer { return makeTCPPacket(header.TCPFlagAck, nil, testPayload(mss+1)) },
			mss:  0,
			want: false,
		},
		{
			name: "syn",
			pkt:  func() *stack.PacketBuffer { return makeTCPPacket(header.TCPFlagSyn, nil, testPayload(mss+1)) },
			mss:  mss,
			want: false,
		},
		{
			name: "rst",
			pkt:  func() *stack.PacketBuffer { return makeTCPPacket(header.TCPFlagRst, nil, testPayload(mss+1)) },
			mss:  mss,
			want: false,
		},
		{
			name: "urgent data",
			pkt:  func() *stack.PacketBuffer { return makeTCPPacket(header.TCPFlagUrg, nil, testPayload(mss+1)) },
			mss:  mss,
			want: false,
		},
		{
			name: "not tcp",
			pkt: func() *stack.PacketBuffer {
				pkt := makeTCPPacket(header.TCPFlagAck, nil, testPayload(mss+1))
				pkt.TransportProtocolNumber = header.UDPProtocolNumber
				return pkt
			},
			mss:  mss,
			want: false,
		},
		{
			name: "transport header not parsed",
			pkt: func() *stack.PacketBuffer {
				pkt := stack.NewPacketBuffer(stack.PacketBufferOptions{
					Payload: buffer.MakeWithData(testPayload(mss + 1)),
				})
				pkt.TransportProtocolNumber = header.TCPProtocolNumber
				return pkt
			},
			mss:  mss,
			want: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pkt := test.pkt()
			defer pkt.DecRef()
			if got := ip.TCPSegmentable(pkt, test.mss); got != test.want {
				t.Errorf("ip.TCPSegmentable(pkt, %d) = %t, want = %t", test.mss, got, test.want)
			}
		})
	}
}
