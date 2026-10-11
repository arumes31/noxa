package webrtc

import (
	"fmt"
	"testing"
	"time"

	"github.com/pion/rtp"
)

func TestH264DiscontinuityDiagnosticsMatchExistingPredicate(t *testing.T) {
	for mask := h264DiscontinuityMask(0); mask <= 7; mask++ {
		t.Run(fmt.Sprintf("mask-%d", mask), func(t *testing.T) {
			var input videoIngressDiagnostic
			frame := h264ForwardFrame{onReject: input.rejectH264}
			now := time.Unix(1, 0)
			frame.push(h264ForwardPacket(10, 90, false, 0x41, 1), now)
			next := h264ForwardPacket(11, 90, true, 0x41, 2)
			if mask&h264DiscontinuitySSRC != 0 {
				next.SSRC++
			}
			if mask&h264DiscontinuityTimestamp != 0 {
				next.Timestamp++
			}
			if mask&h264DiscontinuitySequence != 0 {
				next.SequenceNumber++
			}
			packets, keyframe, lost := frame.push(next, now)
			if mask == 0 {
				if len(packets) != 2 || lost || input.h264RejectSnapshot() != nil {
					t.Fatal("contiguous access unit acquired a rejection")
				}
				return
			}
			// A same-timestamp/source gap quarantines the remainder. A new
			// timestamp/source may start its own complete unit, never the old one.
			wantPackets := 1
			if mask == h264DiscontinuitySequence {
				wantPackets = 0
			}
			if !lost || keyframe || len(packets) != wantPackets || (len(packets) > 0 && packets[0].SequenceNumber != next.SequenceNumber) {
				t.Fatal("diagnostic changed incomplete-unit rejection or recovery")
			}
			snapshot := input.h264RejectSnapshot()
			var want [7]uint64
			want[mask-1] = 1
			if snapshot == nil || snapshot.Discontinuity != 1 || snapshot.DiscontinuityMasks == nil || *snapshot.DiscontinuityMasks != want || snapshot.Malformed != 0 || snapshot.Capacity != 0 || snapshot.Idle != 0 || snapshot.Age != 0 {
				t.Fatalf("wrong combination or aggregate: %+v", snapshot)
			}
			// Both snapshots own their nested arrays, in both directions.
			snapshot.DiscontinuityMasks[mask-1] = 99
			second := input.h264RejectSnapshot()
			if *second.DiscontinuityMasks != want {
				t.Fatal("mutable combination counters escaped")
			}
			input.rejectH264(h264RejectDiscontinuity, mask)
			if *second.DiscontinuityMasks != want || input.h264RejectSnapshot().DiscontinuityMasks[mask-1] != 2 {
				t.Fatal("later rejection mutated an earlier snapshot")
			}
		})
	}
}

func TestH264DiscontinuityDiagnosticsIgnoreRepairedPaddingAndWrap(t *testing.T) {
	var input videoIngressDiagnostic
	frame := h264ForwardFrame{onReject: input.rejectH264}
	start := h264ForwardPacket(65534, 90, false, 0x7c, 0x81)
	padding := recoveryPaddingPacket(7, 65535, 12345)
	end := h264ForwardPacket(0, 90, true, 0x7c, 0x41)
	track := &fakeVideoTrack{fakeTrackReader: fakeTrackReader{packets: []*rtp.Packet{start, end, padding}}}
	var delivered []*rtp.Packet
	readOrderedVideo(track, func() bool { return true }, func() string { return "video/H264" }, func(packet *rtp.Packet, _ string) {
		packets, _, lost := frame.push(packet, time.Now())
		if lost {
			t.Fatal("reordered padding, sequence wrap or empty FU manufactured loss")
		}
		delivered = append(delivered, packets...)
	}, nil)
	if len(delivered) != 3 || delivered[0].SequenceNumber != 65534 || delivered[1].SequenceNumber != 65535 || delivered[2].SequenceNumber != 0 || !isVideoPadding(delivered[1]) || input.h264RejectSnapshot() != nil {
		t.Fatal("valid access unit lost sequence continuity or gained a rejection")
	}
}
