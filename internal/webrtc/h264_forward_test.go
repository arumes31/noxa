package webrtc

import (
	"bytes"
	"testing"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
)

func h264ForwardPacket(seq uint16, ts uint32, marker bool, payload ...byte) *rtp.Packet {
	return &rtp.Packet{Header: rtp.Header{Version: 2, SSRC: 7, SequenceNumber: seq, Timestamp: ts, Marker: marker}, Payload: payload}
}

func TestH264ForwardFrameCompleteAndOwned(t *testing.T) {
	var frame h264ForwardFrame
	now := time.Now()
	configuration := h264ForwardPacket(1, 90, false, 24, 0, 2, 0x67, 1, 0, 2, 0x68, 2)
	if packets, _, lost := frame.push(configuration, now); len(packets) != 0 || lost {
		t.Fatal("premature configuration output")
	}
	configuration.Payload[4] = 99
	if packets, _, lost := frame.push(h264ForwardPacket(2, 90, false, 0x7c, 0x85, 3), now); len(packets) != 0 || lost {
		t.Fatal("premature fragmented IDR output")
	}
	packets, key, lost := frame.push(h264ForwardPacket(3, 90, true, 0x7c, 0x45, 4), now)
	if len(packets) != 3 || !key || lost || packets[0].Payload[4] != 1 {
		t.Fatalf("incomplete or aliased keyframe: %d %v %v", len(packets), key, lost)
	}
}

func TestH264ForwardPacedKeyframeKeepsReceiverSequence(t *testing.T) {
	var frame h264ForwardFrame
	var stream videoContinuity
	now := time.Now()
	// An intact 33-packet keyframe paced over 960 ms is valid. The former
	// 500 ms total-age cutoff discarded its suffix and created a gap at every
	// receiver, even though the sender's receiver report showed no loss.
	frame.push(h264ForwardPacket(100, 90, false, 24, 0, 2, 0x67, 1, 0, 2, 0x68, 1), now)
	for index := 1; index <= 32; index++ {
		fu := byte(5)
		if index == 1 {
			fu |= 0x80
		}
		if index == 32 {
			fu |= 0x40
		}
		out, key, lost := frame.push(h264ForwardPacket(uint16(100+index), 90, index == 32, 0x7c, fu, 1), now.Add(time.Duration(index)*30*time.Millisecond))
		if lost {
			t.Fatalf("contiguous paced keyframe discarded at packet %d", index)
		}
		if index < 32 {
			if len(out) != 0 {
				t.Fatal("partial keyframe escaped")
			}
			continue
		}
		if len(out) != 33 || !key {
			t.Fatal("complete paced keyframe missing")
		}
		for i, packet := range out {
			wire, ok := stream.translateH264(packet, now.Add(time.Second), key && i == 0)
			if !ok || wire.SequenceNumber != uint16(100+i) {
				t.Fatal("paced keyframe created downstream sequence gap")
			}
		}
	}
}

func TestH264ForwardAssemblyTimeBounds(t *testing.T) {
	for _, mode := range []string{"idle", "trickle", "padding"} {
		t.Run(mode, func(t *testing.T) {
			now := time.Now()
			var reasons []h264RejectReason
			frame := h264ForwardFrame{onReject: func(reason h264RejectReason) { reasons = append(reasons, reason) }}
			frame.push(h264ForwardPacket(1, 90, false, 0x7c, 0x81, 1), now)
			expected := h264RejectIdle
			if mode == "idle" {
				frame.push(h264ForwardPacket(2, 90, true, 0x7c, 0x41, 1), now.Add(h264ForwardIdleWait+time.Millisecond))
			} else {
				if mode == "trickle" {
					expected = h264RejectAge
				}
				for i := 1; len(reasons) == 0 && i <= 21; i++ {
					packet := h264ForwardPacket(uint16(i+1), 90, false, 0x7c, 1, 1)
					if mode == "padding" {
						packet = recoveryPaddingPacket(7, uint16(i+1), 0)
					}
					frame.push(packet, now.Add(time.Duration(i)*100*time.Millisecond))
				}
			}
			if len(reasons) == 0 || reasons[0] != expected || len(frame.packets) != 0 {
				t.Fatalf("assembly not bounded: reasons=%v packets=%d", reasons, len(frame.packets))
			}
		})
	}
}

func TestH264ForwardAcceptsEmptyFUAndIgnoresReservedBit(t *testing.T) {
	for _, reserved := range []byte{0, 0x20} {
		var frame h264ForwardFrame
		now := time.Now()
		for index, payload := range [][]byte{{0x7c, 0x81 | reserved}, {0x7c, 1 | reserved, 1}, {0x7c, 0x41 | reserved}} {
			out, _, lost := frame.push(h264ForwardPacket(uint16(index+1), 90, index == 2, payload...), now)
			if lost || (index == 2 && len(out) != 3) {
				t.Fatalf("RFC 6184 valid FU rejected: index=%d reserved=%x", index, reserved)
			}
		}
	}
}

func TestH264ForwardFrameDetectsLossBetweenCompleteUnits(t *testing.T) {
	for _, test := range []struct {
		name        string
		first, next uint16
		padding     bool
		lost        bool
	}{
		{name: "contiguous", first: 10, next: 11},
		{name: "sequence wrap", first: 65535, next: 0},
		{name: "whole frame missing", first: 10, next: 12, lost: true},
		{name: "gap before padding", first: 10, next: 12, padding: true, lost: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var frame h264ForwardFrame
			now := time.Now()
			frame.push(h264ForwardPacket(test.first, 90, true, 0x41, 1), now)
			packet := h264ForwardPacket(test.next, 180, true, 0x41, 2)
			if test.padding {
				packet = recoveryPaddingPacket(7, test.next, 0)
			}
			packets, _, lost := frame.push(packet, now.Add(time.Millisecond))
			if len(packets) != 1 || lost != test.lost {
				t.Fatalf("complete-unit gap recovery: packets=%d lost=%v want=%v", len(packets), lost, test.lost)
			}
			if _, _, lost := frame.push(h264ForwardPacket(test.next+1, 270, true, 0x41, 3), now.Add(2*time.Millisecond)); lost {
				t.Fatal("one gap kept requesting recovery on contiguous packets")
			}
		})
	}
}

func TestH264ForwardFrameRejectsBrokenUnits(t *testing.T) {
	for _, payload := range [][]byte{{24, 0, 5, 0x67, 1}, {0x7c, 0x45, 1}, {0x7c, 0xc5, 1}, {0xe7, 1}, {25, 1}} {
		var frame h264ForwardFrame
		if packets, _, lost := frame.push(h264ForwardPacket(1, 90, true, payload...), time.Now()); len(packets) != 0 || !lost {
			t.Fatalf("malformed RTP accepted: %x", payload)
		}
	}
	var frame h264ForwardFrame
	now := time.Now()
	frame.push(h264ForwardPacket(1, 90, false, 0x7c, 0x85, 1), now)
	if packets, _, lost := frame.push(h264ForwardPacket(3, 90, true, 0x7c, 0x45, 1), now); len(packets) != 0 || !lost {
		t.Fatal("fragment gap accepted")
	}
	frame.push(h264ForwardPacket(4, 180, false, 0x67, 1), now)
	if _, key, lost := frame.push(h264ForwardPacket(5, 180, true, 0x65, 1), now.Add(time.Second)); key || !lost {
		t.Fatal("expired configuration accepted")
	}
}

func TestH264ForwardFrameInterleavedPadding(t *testing.T) {
	for _, gap := range []uint16{0, 1} {
		var frame h264ForwardFrame
		now := time.Now()
		frame.push(h264ForwardPacket(1, 90, false, 0x7c, 0x81, 1), now)
		padding := h264ForwardPacket(2+gap, 0, false)
		padding.Padding = true
		padding.PaddingSize = 1
		frame.push(padding, now)
		packets, _, lost := frame.push(h264ForwardPacket(3+gap, 90, true, 0x7c, 0x41, 2), now)
		if gap == 0 && (len(packets) != 3 || lost || !isVideoPadding(packets[1])) {
			t.Fatal("padding discarded an intact fragmented frame")
		}
		if gap != 0 && (len(packets) != 0 || !lost) {
			t.Fatal("padding hid a missing media packet")
		}
	}
}

// Chromium sends padding probes in the same RTP sequence space as media.
// Dropping them manufactures downstream loss and repeated, unrepairable NACKs.
func TestH264PaddingPreservesReceiverSequenceAndFrameState(t *testing.T) {
	now := time.Now()
	var frame h264ForwardFrame
	var stream videoContinuity
	packets := []*rtp.Packet{
		h264ForwardPacket(100, 90, true, 24, 0, 2, 0x67, 1, 0, 2, 0x68, 1, 0, 2, 0x65, 1),
		recoveryPaddingPacket(7, 101, 0),
		h264ForwardPacket(102, 180, false, 0x7c, 0x81, 1),
		recoveryPaddingPacket(7, 103, 0),
		h264ForwardPacket(104, 180, true, 0x7c, 0x41, 2),
	}
	next := uint16(100)
	for _, input := range packets {
		out, key, lost := frame.push(input, now)
		if lost {
			t.Fatal("intact media with padding requested recovery")
		}
		for i, packet := range out {
			previous := stream
			wire, accepted := stream.translateH264(packet, now.Add(time.Second), key && i == 0)
			if !accepted || wire.SequenceNumber != next {
				t.Fatalf("receiver sequence gap: want %d, got %+v", next, wire)
			}
			if isVideoPadding(packet) && (stream.timestamp != previous.timestamp || !stream.last.Equal(previous.last)) {
				t.Fatal("padding advanced the last media frame")
			}
			next++
		}
	}
	if next != 105 {
		t.Fatalf("receiver is missing RTP packets: next=%d", next)
	}
	stream.beginEpoch()
	if _, ok := stream.translateH264(recoveryPaddingPacket(7, 105, 270), now, true); ok {
		t.Fatal("padding resumed a paused receiver")
	}
}

func TestH264InterleavedPaddingRemainsBounded(t *testing.T) {
	var frame h264ForwardFrame
	now := time.Now()
	frame.push(h264ForwardPacket(0, 90, false, 0x7c, 0x81, 1), now)
	for sequence := 1; sequence <= h264AccessUnitPackets; sequence++ {
		packets, _, lost := frame.push(recoveryPaddingPacket(7, uint16(sequence), 0), now)
		if len(packets) != 0 || lost != (sequence == h264AccessUnitPackets) {
			t.Fatalf("unbounded or prematurely forwarded incomplete unit at %d", sequence)
		}
	}
	if len(frame.packets) != 0 || frame.bytes != 0 {
		t.Fatal("padding exhaustion retained the abandoned frame")
	}
}

func TestH264ForwardFrameRequiresConfigurationBeforeIDR(t *testing.T) {
	var frame h264ForwardFrame
	now := time.Now()
	frame.push(h264ForwardPacket(1, 90, false, 0x65, 1), now)
	_, key, lost := frame.push(h264ForwardPacket(2, 90, true, 24, 0, 2, 0x67, 1, 0, 2, 0x68, 1), now)
	if key || lost {
		t.Fatal("configuration after IDR must not start a receiver")
	}
}

func TestH264ContinuityRequiresKeyframeOnResume(t *testing.T) {
	var continuity videoContinuity
	now := time.Now()
	if _, ok := continuity.translateH264(h264ForwardPacket(1, 90, true, 0x41, 1), now, false); ok {
		t.Fatal("delta starts receiver")
	}
	first := h264ForwardPacket(2, 180, false, 0x67, 1)
	out, ok := continuity.translateH264(first, now, true)
	if !ok || !bytes.Equal(out.Payload, first.Payload) {
		t.Fatal("H264 payload rewritten")
	}
	continuity.beginEpoch()
	if _, ok := continuity.translateH264(h264ForwardPacket(3, 270, true, 0x41, 1), now, false); ok {
		t.Fatal("delta resumes receiver")
	}
	resumed := h264ForwardPacket(200, 9000, false, 0x67, 2)
	out, ok = continuity.translateH264(resumed, now.Add(time.Second), true)
	if !ok || out.SequenceNumber != 3 || out.Timestamp != 90180 {
		t.Fatalf("broken resume continuity: %+v", out)
	}
	if _, ok := continuity.translateH264(h264ForwardPacket(199, 8900, true, 0x41, 1), now, false); ok {
		t.Fatal("old epoch accepted")
	}
}

func TestRouterVideoCodecBindingAndLateJoin(t *testing.T) {
	engine, err := New(testLogger(), nil, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = engine.Close() })
	router := NewRouter(nil)
	attachFakePeer(t, engine, router, "sub")
	router.JoinChannel(1, "sub")
	router.JoinChannel(1, "pub")
	generation := testVideoPublication(t, router, "pub", "sub", SlotCam)
	if _, err := router.WatchVideoWithCodecs("sub", "pub", SlotCam, generation, 2, router.VideoWatchSession("sub"), true, []string{"h264", "vp8"}); err != nil {
		t.Fatal(err)
	}
	token, ok := router.claimSlot("pub", SlotCam+slotSep)
	if !ok {
		t.Fatal("claim failed")
	}
	codec := webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000, SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42001f"}
	renegotiations := 0
	router.SetRenegotiateHook(func(id string) {
		if id == "sub" {
			renegotiations++
		}
	})
	if !router.setVideoCodec("pub", SlotCam, "", token, codec) || renegotiations != 1 {
		t.Fatal("codec change not negotiated")
	}
	output := pubTrackFor(router, "sub", "pub").video[SlotCam]
	capture := &extensionCapture{}
	if _, err := output.track.Bind(extensionTrackContext{codec: webrtc.RTPCodecParameters{RTPCodecCapability: codec, PayloadType: 102}, writer: capture}); err != nil {
		t.Fatal(err)
	}
	packet := h264ForwardPacket(5, 90, true, 0x65, 99)
	if router.forwardVideoFrameAtPolicyCodec("pub", SlotCam, "", packet, time.Now(), router.videoPolicySnapshot(), true, webrtc.MimeTypeH264, true) != 1 {
		t.Fatal("H264 did not reach subscriber")
	}
	if capture.header.PayloadType != 102 || !bytes.Equal(capture.payload, packet.Payload) {
		t.Fatal("H264 mislabeled or corrupted")
	}
	if router.ForwardVideo("pub", SlotCam, "", continuityPacket(7, 6, 180, 1, true)) != 0 {
		t.Fatal("VP8 entered H264 output")
	}
	if !router.setVideoCodec("pub", SlotCam, "", token, codec) || renegotiations != 1 {
		t.Fatal("unchanged codec renegotiated")
	}
	attachFakePeer(t, engine, router, "late")
	router.JoinChannel(1, "late")
	if pubTrackFor(router, "late", "pub").video[SlotCam].track.Codec().MimeType != webrtc.MimeTypeVP8 {
		t.Fatal("unknown viewer cannot negotiate a safe initial track")
	}
	if _, err := router.WatchVideoWithCodecs("late", "pub", SlotCam, generation, 1, router.VideoWatchSession("late"), true, []string{"h264", "vp8"}); err != nil {
		t.Fatal(err)
	}
	if !sameVideoCodec(pubTrackFor(router, "late", "pub").video[SlotCam].track.Codec(), codec) {
		t.Fatal("late viewer got wrong codec")
	}
	router.releaseSlot("pub", SlotCam+slotSep, token)
	if router.setVideoCodec("pub", SlotCam, "", token, webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8, ClockRate: 90000}) {
		t.Fatal("retired source changed codec")
	}
}
