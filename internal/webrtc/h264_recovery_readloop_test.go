package webrtc

import (
	"bytes"
	"testing"
	"time"

	"github.com/pion/rtp"
	"noxa/internal/netproto"
)

// Exercise the real reader/reorder/assembler/request path, not a duplicate
// of the lost/keyframe decision. A complete replacement already repairs the
// abandoned unit; incomplete replacements must still request recovery.
func TestH264ReadLoopCompleteReplacementRecovery(t *testing.T) {
	for _, tc := range []struct {
		name        string
		replacement []*rtp.Packet
		requests    uint64
		forwarded   int
		malformed   uint64
	}{
		{name: "complete SPS PPS IDR", replacement: []*rtp.Packet{h264ForwardPacket(2, 180, true, 24, 0, 2, 0x67, 1, 0, 2, 0x68, 1, 0, 2, 0x65, 1)}, forwarded: 1},
		{name: "delta", replacement: []*rtp.Packet{h264ForwardPacket(2, 180, true, 0x41, 1)}, requests: 1, forwarded: 1},
		{name: "IDR without configuration", replacement: []*rtp.Packet{h264ForwardPacket(2, 180, true, 0x65, 1)}, requests: 1, forwarded: 1},
		{name: "configuration after IDR", replacement: []*rtp.Packet{h264ForwardPacket(2, 180, true, 24, 0, 2, 0x65, 1, 0, 2, 0x67, 1, 0, 2, 0x68, 1)}, requests: 1, forwarded: 1},
		{name: "fragmented replacement", replacement: []*rtp.Packet{
			h264ForwardPacket(2, 180, false, 24, 0, 2, 0x67, 1, 0, 2, 0x68, 1),
			h264ForwardPacket(3, 180, false, 0x7c, 0x85, 1),
			h264ForwardPacket(4, 180, true, 0x7c, 0x45, 1),
		}, requests: 1, forwarded: 3},
		{name: "incomplete FU at marker", replacement: []*rtp.Packet{h264ForwardPacket(2, 180, true, 0x7c, 0x85, 1)}, requests: 1, malformed: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := NewRouter(nil)
			r.JoinChannel(1, "pub")
			r.JoinChannel(1, "recorder")
			r.SetTrackSlots("pub", map[string]string{"desktop": SlotScreen})
			output := &fakeTrackWriter{}
			r.addVideoOutput("recorder", output)
			writer := &fakeRTCPWriter{}
			r.rtcpWriters["pub"] = writer
			track := &keyframeDiagnosticH264Track{diagnosticVideoTrack: diagnosticVideoTrack{fakeVideoTrack: fakeVideoTrack{rid: "f", ssrc: 7}, packets: make(chan *rtp.Packet)}}
			done := make(chan struct{})
			go func() { defer close(done); r.ReadVideoLoop("pub", SlotScreen, track) }()
			t.Cleanup(func() {
				close(track.packets)
				select {
				case <-done:
				case <-time.After(time.Second):
					t.Error("reader did not stop")
				}
			})
			send := func(packet *rtp.Packet) {
				t.Helper()
				select {
				case track.packets <- packet:
				case <-time.After(time.Second):
					t.Fatal("reader did not accept packet")
				}
			}
			send(h264ForwardPacket(1, 90, false, 0x7c, 0x81, 1))
			for _, packet := range tc.replacement {
				send(packet)
			}
			// The nil read is a barrier: all prior contiguous packets finished their
			// synchronous forwarding and diagnostics, without injecting another AU.
			send(nil)
			r.mu.RLock()
			input := r.videoIngress[videoIngressKey{"pub", SlotScreen, "f"}]
			r.mu.RUnlock()
			if input == nil {
				t.Fatal("reader ingress missing")
			}
			input.mu.Lock()
			rejects, requests := input.h264RejectSnapshot(), input.keyframeRequestSnapshot()
			input.mu.Unlock()
			var assembly netproto.VideoKeyframeRequestCounters
			if requests != nil {
				assembly = requests.H264Assembly
			}
			if assembly.Requests != tc.requests || assembly.WriteSucceeded != tc.requests || assembly.Coalesced != 0 || assembly.WriteFailed != 0 || writer.pliCount() != int(tc.requests) {
				t.Fatalf("redundant or missing recovery: assembly=%+v, writes=%d, want=%d", assembly, writer.pliCount(), tc.requests)
			}
			if rejects == nil || rejects.Discontinuity != 1 || rejects.Malformed != tc.malformed || rejects.DiscontinuityMasks[1] != 1 {
				t.Fatalf("abandoned AU rejection changed: %+v", rejects)
			}
			output.mu.Lock()
			defer output.mu.Unlock()
			if len(output.packets) != tc.forwarded {
				t.Fatalf("forwarded %d packets, want %d", len(output.packets), tc.forwarded)
			}
			for i, packet := range output.packets {
				want := tc.replacement[i]
				if packet.SequenceNumber != want.SequenceNumber || packet.Timestamp != want.Timestamp || packet.Marker != want.Marker || !bytes.Equal(packet.Payload, want.Payload) {
					t.Fatal("replacement forwarding changed")
				}
			}
			if tc.requests == 0 {
				// Suppressing the redundant assembly request must not spend the throttle
				// or suppress a later independent request for this source.
				r.RequestKeyframe("pub", SlotScreen, "f")
				input.mu.Lock()
				explicit := input.keyframeRequestSnapshot().Explicit
				input.mu.Unlock()
				if writer.pliCount() != 1 || explicit != (netproto.VideoKeyframeRequestCounters{Requests: 1, WriteSucceeded: 1}) {
					t.Fatal("independent request suppressed")
				}
			}
		})
	}
}
