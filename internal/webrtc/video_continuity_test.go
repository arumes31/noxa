package webrtc

import (
	"bytes"
	"testing"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
)

func continuityPacket(ssrc uint32, seq uint16, ts uint32, pic uint16, key bool) *rtp.Packet {
	frame := byte(1)
	if key {
		frame = 0
	}
	return &rtp.Packet{Header: rtp.Header{SSRC: ssrc, SequenceNumber: seq, Timestamp: ts}, Payload: []byte{0x90, 0xe0, 0x80 | byte(pic>>8), byte(pic), byte(pic), 0, frame, 0, 0, 0x9d, 1, 0x2a, 0x80, 2, 0x68, 1}}
}

func TestVideoContinuitySevenBitWrap(t *testing.T) {
	var stream videoContinuity
	for i, id := range []byte{126, 127, 0, 1} {
		pkt := continuityPacket(10, uint16(i), uint32(i*3000), uint16(id), true)
		pkt.Payload = append([]byte{0x90, 0xe0, id}, pkt.Payload[4:]...)
		out, ok := stream.translate(pkt, time.Now())
		if !ok || out.Payload[2] != 0x80 || out.Payload[3] != byte(126+i) {
			t.Fatalf("seven-bit wrap %d: %x", i, out.Payload)
		}
	}
}

func TestVideoContinuityMalformedAndOldEpoch(t *testing.T) {
	var stream videoContinuity
	now := time.Now()
	_, _ = stream.translate(continuityPacket(10, 100, 90000, 10, true), now)
	bad := continuityPacket(10, 101, 93000, 11, false)
	bad.Payload = []byte{0x80, 0x80}
	if _, ok := stream.translate(bad, now); ok {
		t.Fatal("malformed descriptor accepted")
	}
	out, ok := stream.translate(continuityPacket(20, 1000, 900000, 100, true), now.Add(time.Second/30))
	if !ok || out.SequenceNumber != 101 {
		t.Fatal("malformed packet poisoned continuity")
	}
	if _, ok := stream.translate(continuityPacket(20, 999, 897000, 99, false), now); ok {
		t.Fatal("packet from before switch entered new epoch")
	}
}

func TestVideoContinuitySevenBitLargeGap(t *testing.T) {
	var stream videoContinuity
	for i, id := range []byte{10, 90, 70} {
		pkt := continuityPacket(10, []uint16{10, 90, 70}[i], uint32(id)*3000, uint16(id), true)
		pkt.Payload = append([]byte{0x90, 0xe0, id}, pkt.Payload[4:]...)
		out, ok := stream.translate(pkt, time.Now())
		if !ok || out.Payload[2] != 0x80 || out.Payload[3] != id {
			t.Fatalf("picture gap/reorder: %+v", out)
		}
	}
}

func TestVideoContinuityTimestampUsesFrameStart(t *testing.T) {
	var stream videoContinuity
	now := time.Now()
	_, _ = stream.translate(continuityPacket(10, 100, 90000, 10, true), now)
	_, _ = stream.translate(continuityPacket(10, 101, 90000, 10, false), now.Add(20*time.Millisecond))
	out, ok := stream.translate(continuityPacket(20, 1000, 900000, 100, true), now.Add(33*time.Millisecond))
	if !ok || out.Timestamp != 92970 {
		t.Fatalf("frame fragments compressed timestamp: %+v", out)
	}
}

func TestSubscriberSimulcastOutputContinuity(t *testing.T) {
	e, err := New(testLogger(), nil, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close() })
	r := NewRouter(nil)
	attachFakePeer(t, e, r, "sub")
	r.JoinChannel(1, "sub")
	r.JoinChannel(1, "pub")
	testVideoPublication(t, r, "pub", "sub", SlotCam)
	registerVideoSource(r, "pub", SlotCam, "h", 10)
	registerVideoSource(r, "pub", SlotCam, "f", 20)
	output := pubTrackFor(r, "sub", "pub").video[SlotCam]
	capture := &extensionCapture{}
	_, err = output.track.Bind(extensionTrackContext{codec: webrtc.RTPCodecParameters{RTPCodecCapability: output.track.Codec(), PayloadType: 96}, writer: capture})
	if err != nil {
		t.Fatal(err)
	}
	if r.ForwardVideo("pub", SlotCam, "h", continuityPacket(10, 100, 90000, 10, true)) != 1 {
		t.Fatal("initial output missing")
	}
	if err := r.SetVideoQuality("sub", "high"); err != nil {
		t.Fatal(err)
	}
	if r.ForwardVideo("pub", SlotCam, "f", continuityPacket(20, 60000, 9000000, 999, false)) != 0 {
		t.Fatal("delta frame switched layers")
	}
	if r.ForwardVideo("pub", SlotCam, "h", continuityPacket(10, 101, 93000, 11, false)) != 1 {
		t.Fatal("working current layer stopped before replacement keyframe")
	}
	if r.ForwardVideo("pub", SlotCam, "f", continuityPacket(20, 60001, 9003000, 1000, true)) != 1 {
		t.Fatal("keyframe switch missing")
	}
	if capture.header.SequenceNumber != 102 || capture.header.SSRC != 12345 || capture.payload[3] != 12 {
		t.Fatalf("binding continuity broken: %+v %x", capture.header, capture.payload)
	}
	if r.ForwardVideo("pub", SlotCam, "h", continuityPacket(10, 102, 96000, 12, true)) != 0 {
		t.Fatal("retired layer switched back after desired keyframe")
	}
	if len(r.pubPeers["sub"].pc.GetTransceivers()) != 2 {
		t.Fatal("layer switch allocated transceivers")
	}
}

func TestInactiveSimulcastLayerFallsBack(t *testing.T) {
	r := NewRouter(nil)
	registerVideoSource(r, "pub", SlotCam, "h", 10)
	registerVideoSource(r, "pub", SlotCam, "q", 20)
	r.mu.Lock()
	r.videoSeen[videoSourceRef{"pub", SlotCam}] = map[string]time.Time{"h": time.Now().Add(-2 * time.Second), "q": time.Now()}
	got := r.preferredRIDLocked("sub", "pub", SlotCam)
	r.mu.Unlock()
	if got != "q" {
		t.Fatalf("inactive mid layer selected: %s", got)
	}
}

func TestVideoContinuitySwitch(t *testing.T) {
	var stream videoContinuity
	now := time.Now()
	first := continuityPacket(10, 65535, 90000, 32767, true)
	if _, ok := stream.translate(first, now); !ok {
		t.Fatal("initial keyframe rejected")
	}
	if _, ok := stream.translate(continuityPacket(20, 1234, 500000, 90, false), now); ok {
		t.Fatal("layer switched on delta frame")
	}
	next := continuityPacket(20, 1235, 503000, 91, true)
	original := append([]byte(nil), next.Payload...)
	out, ok := stream.translate(next, now.Add(time.Second/30))
	if !ok || out.SequenceNumber != 0 || out.Timestamp < 92999 || out.Timestamp > 93001 {
		t.Fatalf("discontinuous switch: %+v, %v", out, ok)
	}
	if out.Payload[2] != 0x80 || out.Payload[3] != 0 || out.Payload[4] != 0 {
		t.Fatalf("VP8 frame IDs not translated: %x", out.Payload)
	}
	if !bytes.Equal(original, next.Payload) {
		t.Fatal("mutated shared source payload")
	}
	// Preserve a source gap for NACK/loss detection, and retain its mapping
	// when the missing packet arrives out of order.
	out, ok = stream.translate(continuityPacket(20, 1237, 506000, 92, false), now.Add(time.Second/15))
	if !ok || out.SequenceNumber != 2 || out.Payload[3] != 1 {
		t.Fatalf("lost source gap: %+v", out)
	}
	out, ok = stream.translate(continuityPacket(20, 1236, 503000, 91, false), now.Add(time.Second/15))
	if !ok || out.SequenceNumber != 1 || out.Payload[3] != 0 {
		t.Fatalf("late packet mapping changed: %+v", out)
	}
	// Returning to an earlier source must open a new output epoch.
	out, ok = stream.translate(continuityPacket(10, 2, 99000, 2, true), now.Add(time.Second/10))
	if !ok || out.SequenceNumber != 3 || out.Payload[3] != 2 {
		t.Fatalf("return switch: %+v", out)
	}
}

func TestVideoContinuityWatchResume(t *testing.T) {
	var stream videoContinuity
	now := time.Now()
	stream.beginEpoch()
	if _, ok := stream.translate(continuityPacket(10, 99, 87000, 9, false), now); ok {
		t.Fatal("first watch started without a keyframe")
	}
	if _, ok := stream.translate(continuityPacket(10, 100, 90000, 10, true), now); !ok {
		t.Fatal("first watch rejected keyframe")
	}
	stream.beginEpoch()
	if _, ok := stream.translate(continuityPacket(10, 999, 897000, 99, false), now.Add(time.Second)); ok {
		t.Fatal("same-source resume accepted a delta frame")
	}
	out, ok := stream.translate(continuityPacket(10, 1000, 900000, 100, true), now.Add(time.Second))
	if !ok || out.SequenceNumber != 101 || out.Timestamp != 180000 || out.Payload[3] != 11 {
		t.Fatalf("resume lost output continuity: %+v", out)
	}
	if _, ok := stream.translate(continuityPacket(10, 998, 894000, 98, false), now); ok {
		t.Fatal("old epoch packet accepted after resume")
	}
}
