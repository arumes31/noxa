package webrtc

import (
	"reflect"
	"testing"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/rtp/codecs"
	"github.com/pion/webrtc/v4"
)

func recoveryPaddingPacket(ssrc uint32, sequence uint16, timestamp uint32) *rtp.Packet {
	return &rtp.Packet{Header: rtp.Header{Version: 2, SSRC: ssrc, SequenceNumber: sequence, Timestamp: timestamp, Padding: true, PaddingSize: 16}}
}

func TestVideoBoundsPaddingPreservesReferenceWithoutHidingSequenceGap(t *testing.T) {
	for _, gap := range []bool{false, true} {
		name := "contiguous"
		if gap {
			name = "missing_media_before_padding"
		}
		t.Run(name, func(t *testing.T) {
			inspector := vp8BoundsInspector{bounds: VideoBounds{1280, 720}}
			if !inspector.accept(boundsKeyPacket(1, 1, 640, 360)) {
				t.Fatal("valid keyframe rejected")
			}
			sequence := uint16(2)
			if gap {
				sequence++
			}
			if !inspector.accept(recoveryPaddingPacket(42, sequence, 1)) {
				t.Error("valid padding rejected")
			}
			if got := inspector.accept(boundsDeltaPacket(sequence+1, 2)); got == gap {
				t.Errorf("delta accepted = %v, want %v after padding", got, !gap)
			}
		})
	}
}

func TestVideoBoundsPaddingDoesNotRequestKeyframe(t *testing.T) {
	r, err := NewRouterWithVideoBounds(nil, VideoBounds{1280, 720})
	if err != nil {
		t.Fatal(err)
	}
	r.JoinChannel(1, "publisher")
	r.JoinChannel(1, "recorder")
	tap := &fakeTrackWriter{}
	r.addVideoOutput("recorder", tap)
	feedback := &fakeRTCPWriter{}
	r.rtcpWriters["publisher"] = feedback
	packets := []*rtp.Packet{boundsKeyPacket(1, 1, 640, 360), recoveryPaddingPacket(42, 2, 1), boundsDeltaPacket(3, 2)}
	for _, packet := range packets {
		packet.SSRC = 42
	}
	track := &boundsVideoTrack{fakeVideoTrack: fakeVideoTrack{ssrc: 42, fakeTrackReader: fakeTrackReader{packets: packets}}, mime: webrtc.MimeTypeVP8}
	r.ReadVideoLoop("publisher", SlotCam, track)
	if feedback.pliCount() != 0 {
		t.Errorf("padding requested %d unnecessary keyframes", feedback.pliCount())
	}
	foundDelta := false
	for _, packet := range tap.packets {
		foundDelta = foundDelta || packet.SequenceNumber == 3
	}
	if !foundDelta {
		t.Error("padding caused the following valid delta frame to be dropped")
	}
}

func TestCurrentLayerPaddingPreservesSequenceWithoutKeepingLayerAlive(t *testing.T) {
	e, err := New(testLogger(), nil, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close() })
	r := NewRouter(nil)
	attachFakePeer(t, e, r, "viewer")
	r.JoinChannel(1, "viewer")
	r.JoinChannel(1, "pub")
	testVideoPublication(t, r, "pub", "viewer", SlotCam)
	registerVideoSource(r, "pub", SlotCam, "h", 10)
	registerVideoSource(r, "pub", SlotCam, "q", 20)
	output := pubTrackFor(r, "viewer", "pub").video[SlotCam]
	capture := &extensionCapture{}
	_, err = output.track.Bind(extensionTrackContext{codec: webrtc.RTPCodecParameters{RTPCodecCapability: output.track.Codec(), PayloadType: 96}, writer: capture})
	if err != nil {
		t.Fatal(err)
	}
	if r.ForwardVideo("pub", SlotCam, "h", continuityPacket(10, 100, 90000, 10, true)) != 1 {
		t.Fatal("initial current layer was not forwarded")
	}
	feedback := &fakeRTCPWriter{}
	r.rtcpWriters["pub"] = feedback
	ref := videoSourceRef{"pub", SlotCam}
	lastFrame := time.Now().Add(-2 * time.Second)
	r.mu.Lock()
	r.videoSeen[ref]["h"] = lastFrame
	r.videoSeen[ref]["q"] = time.Now()
	r.mu.Unlock()
	if got := r.ForwardVideo("pub", SlotCam, "h", recoveryPaddingPacket(10, 101, 90000)); got != 1 {
		t.Errorf("current-source padding forwarded to %d subscribers, want 1", got)
	} else {
		wirePacket := &rtp.Packet{Header: capture.header, Payload: capture.payload}
		wire, err := wirePacket.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		var decoded rtp.Packet
		if err := decoded.Unmarshal(wire); err != nil {
			t.Fatal(err)
		}
		if decoded.SequenceNumber != 101 || !decoded.Padding || decoded.Header.PaddingSize != 16 || len(decoded.Payload) != 0 {
			t.Errorf("padding lost sequence or wire representation: %+v", decoded)
		}
	}
	r.mu.RLock()
	seen := r.videoSeen[ref]["h"]
	preferred := r.preferredRIDLocked("viewer", "pub", SlotCam)
	r.mu.RUnlock()
	if !seen.Equal(lastFrame) || preferred != "q" {
		t.Errorf("padding kept silent high layer alive: seen = %v, preferred = %q", seen, preferred)
	}
	if feedback.pliCount() != 0 {
		t.Errorf("padding triggered %d unnecessary keyframes", feedback.pliCount())
	}
}

func TestVideoPaddingCannotStartSwitchOrResumeContinuity(t *testing.T) {
	now := time.Unix(1000, 0)
	var active videoContinuity
	if _, ok := active.translate(continuityPacket(10, 100, 90000, 10, true), now); !ok {
		t.Fatal("initial keyframe rejected")
	}
	pending := active
	pending.beginEpoch()
	for _, test := range []struct {
		name   string
		stream videoContinuity
		packet *rtp.Packet
	}{
		{"initial", videoContinuity{}, recoveryPaddingPacket(10, 101, 90000)},
		{"other_source", active, recoveryPaddingPacket(20, 101, 90000)},
		{"pending_epoch", pending, recoveryPaddingPacket(10, 101, 90000)},
		{"empty_without_padding_flag", active, &rtp.Packet{Header: rtp.Header{SSRC: 10, SequenceNumber: 101, PaddingSize: 16}}},
		{"padding_without_size", active, &rtp.Packet{Header: rtp.Header{SSRC: 10, SequenceNumber: 101, Padding: true}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			before := test.stream
			if _, accepted := test.stream.translate(test.packet, now.Add(time.Second)); accepted {
				t.Error("padding or malformed empty packet started a media epoch")
			}
			if test.stream != before {
				t.Error("rejected packet changed continuity state")
			}
		})
	}
}

func TestVideoPaddingAfterSwitchPreservesOffsetsAndFrameState(t *testing.T) {
	for _, legacySize := range []bool{false, true} {
		name := "header_size"
		if legacySize {
			name = "legacy_packet_size"
		}
		t.Run(name, func(t *testing.T) {
			now := time.Unix(1000, 0)
			var stream videoContinuity
			if _, ok := stream.translate(continuityPacket(10, 100, 90000, 10, true), now); !ok {
				t.Fatal("initial keyframe rejected")
			}
			key, ok := stream.translate(continuityPacket(20, 1000, 900000, 100, true), now.Add(time.Second))
			if !ok {
				t.Fatal("switch keyframe rejected")
			}
			packet := recoveryPaddingPacket(20, 1001, 903000)
			if legacySize {
				packet.PaddingSize = packet.Header.PaddingSize
				packet.Header.PaddingSize = 0
			}
			original := packet.Clone()
			before := stream
			padding, accepted := stream.translate(packet, now.Add(2*time.Second))
			if !accepted {
				t.Fatal("current-source padding rejected after switch")
			}
			if padding.SequenceNumber != key.SequenceNumber+1 || padding.Timestamp != key.Timestamp+3000 {
				t.Errorf("padding lost switched offsets: key = %+v, padding = %+v", key.Header, padding.Header)
			}
			if !reflect.DeepEqual(packet, original) {
				t.Error("translation modified the shared source packet")
			}
			// Padding occupies the sequence space without becoming a frame or
			// advancing any VP8 reference or the last frame's arrival time.
			expected := before
			expected.sequence++
			expected.epochPackets++
			if stream != expected {
				t.Errorf("padding changed frame state: before = %+v, after = %+v", before, stream)
			}
			if _, accepted := stream.translate(recoveryPaddingPacket(20, 999, 897000), now); accepted {
				t.Error("padding from before the switched epoch was accepted")
			}
			if stream != expected {
				t.Error("rejected old padding changed continuity")
			}
			next, accepted := stream.translate(continuityPacket(20, 1002, 906000, 101, false), now.Add(3*time.Second))
			if !accepted || next.SequenceNumber != padding.SequenceNumber+1 || next.Timestamp != key.Timestamp+6000 {
				t.Fatal("padding introduced a sequence gap or corrupted the next media timestamp")
			}
		})
	}
}

func TestVideoPaddingReorderingPreservesSevenBitPictureID(t *testing.T) {
	now := time.Unix(1000, 0)
	var stream videoContinuity
	first := continuityPacket(10, 100, 90000, 10, true)
	first.Payload = append([]byte{0x90, 0xe0, 10}, first.Payload[4:]...)
	if _, accepted := stream.translate(first, now); !accepted {
		t.Fatal("initial keyframe rejected")
	}
	if _, accepted := stream.translate(recoveryPaddingPacket(10, 102, 93000), now.Add(time.Second)); !accepted {
		t.Fatal("current-source padding rejected")
	}
	// Padding can overtake the next media packet. Its sequence advances, but
	// the newest translated picture is still 10 when picture 11 arrives.
	next := continuityPacket(10, 101, 93000, 11, false)
	next.Payload = append([]byte{0x90, 0xe0, 11}, next.Payload[4:]...)
	out, accepted := stream.translate(next, now.Add(2*time.Second))
	if !accepted {
		t.Fatal("reordered media after padding rejected")
	}
	var descriptor codecs.VP8Packet
	if _, err := descriptor.Unmarshal(out.Payload); err != nil {
		t.Fatal(err)
	}
	if descriptor.PictureID != 11 {
		t.Fatalf("padding reordering corrupted picture ID: got %d, want 11", descriptor.PictureID)
	}
}
