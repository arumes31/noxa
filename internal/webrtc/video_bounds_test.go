package webrtc

import (
	"encoding/binary"
	"strings"
	"testing"

	"github.com/pion/interceptor"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
)

func boundsKeyPacket(seq uint16, timestamp uint32, width, height uint16) *rtp.Packet {
	data := []byte{0x10, 0x10, 0, 0, 0x9d, 1, 0x2a, 0, 0, 0, 0}
	binary.LittleEndian.PutUint16(data[7:9], width)
	binary.LittleEndian.PutUint16(data[9:11], height)
	return &rtp.Packet{Header: rtp.Header{SequenceNumber: seq, Timestamp: timestamp}, Payload: data}
}

func boundsDeltaPacket(seq uint16, timestamp uint32) *rtp.Packet {
	return &rtp.Packet{Header: rtp.Header{SequenceNumber: seq, Timestamp: timestamp, Marker: true}, Payload: []byte{0x10, 0x11, 0, 0}}
}

type boundsVideoTrack struct {
	fakeVideoTrack
	mime string
}

type switchingBoundsTrack struct{ boundsVideoTrack }

func (t *switchingBoundsTrack) ReadRTP() (*rtp.Packet, interceptor.Attributes, error) {
	p, attributes, err := t.fakeTrackReader.ReadRTP()
	if t.reads() > 1 {
		t.mime = webrtc.MimeTypeOpus
	}
	return p, attributes, err
}

func TestVideoBoundsRejectCodecChangeDuringTrack(t *testing.T) {
	r, err := NewRouterWithVideoBounds(nil, VideoBounds{1280, 720})
	if err != nil {
		t.Fatal(err)
	}
	r.JoinChannel(1, "publisher")
	r.JoinChannel(1, "recorder")
	tap := &fakeTrackWriter{}
	r.addVideoOutput("recorder", tap)
	track := &switchingBoundsTrack{boundsVideoTrack{fakeVideoTrack: fakeVideoTrack{ssrc: 42, fakeTrackReader: fakeTrackReader{packets: []*rtp.Packet{boundsKeyPacket(1, 1, 640, 360), boundsDeltaPacket(2, 2)}}}, mime: webrtc.MimeTypeVP8}}
	r.ReadVideoLoop("publisher", SlotCam, track)
	if tap.count() != 1 {
		t.Fatalf("changed codec passed VP8 inspection: %d", tap.count())
	}
}

func (t *boundsVideoTrack) Codec() webrtc.RTPCodecParameters {
	return webrtc.RTPCodecParameters{RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: t.mime}}
}

func TestVideoBoundsBlockOversizeAndUnknownReferences(t *testing.T) {
	for _, slot := range []string{SlotCam, SlotScreen} {
		t.Run(slot, func(t *testing.T) {
			r, err := NewRouterWithVideoBounds(nil, VideoBounds{1280, 720})
			if err != nil {
				t.Fatal(err)
			}
			r.JoinChannel(1, "publisher")
			r.JoinChannel(1, "recorder")
			r.SetTrackSlots("publisher", map[string]string{"video": slot})
			tap := &fakeTrackWriter{}
			feedback := &fakeRTCPWriter{}
			r.rtcpWriters["publisher"] = feedback
			r.addVideoOutput("recorder", tap)
			packets := []*rtp.Packet{
				boundsKeyPacket(1, 1, 1920, 1080), boundsDeltaPacket(2, 2),
				boundsKeyPacket(3, 3, 1280, 720),
				{Header: rtp.Header{SequenceNumber: 4, Timestamp: 3, Marker: true}, Payload: []byte{0, 5}},
				boundsDeltaPacket(5, 4), boundsKeyPacket(6, 5, 1281, 720),
				boundsDeltaPacket(7, 6), boundsKeyPacket(8, 7, 640, 360),
			}
			track := &boundsVideoTrack{fakeVideoTrack: fakeVideoTrack{ssrc: 42, fakeTrackReader: fakeTrackReader{packets: packets}}, mime: webrtc.MimeTypeVP8}
			r.ReadVideoLoop("publisher", slot, track)
			if tap.count() != 4 {
				t.Fatalf("dimension filter wrote %d packets, want 4", tap.count())
			}
			if feedback.pliCount() != 1 {
				t.Fatalf("keyframe recovery missing/unbounded: %d", feedback.pliCount())
			}
			for i, seq := range []uint16{3, 4, 5, 8} {
				if tap.packets[i].SequenceNumber != seq {
					t.Fatalf("oversized/unknown-reference frame reached tap: %d", tap.packets[i].SequenceNumber)
				}
			}
		})
	}
}

func TestVideoBoundsCodecAndNewTrackFailClosed(t *testing.T) {
	r, err := NewRouterWithVideoBounds(nil, VideoBounds{1280, 720})
	if err != nil {
		t.Fatal(err)
	}
	r.JoinChannel(1, "publisher")
	r.JoinChannel(1, "recorder")
	tap := &fakeTrackWriter{}
	r.addVideoOutput("recorder", tap)
	for _, mime := range []string{webrtc.MimeTypeH264, webrtc.MimeTypeVP9, webrtc.MimeTypeAV1, ""} {
		track := &boundsVideoTrack{fakeVideoTrack: fakeVideoTrack{fakeTrackReader: fakeTrackReader{packets: []*rtp.Packet{boundsKeyPacket(1, 1, 640, 360)}}}, mime: mime}
		r.ReadVideoLoop("publisher", SlotCam, track)
		if track.reads() != 0 {
			t.Fatalf("unsupported codec reached inspector: %s", mime)
		}
	}
	r.ReadVideoLoop("publisher", SlotCam, &fakeVideoTrack{fakeTrackReader: fakeTrackReader{packets: []*rtp.Packet{boundsKeyPacket(1, 1, 640, 360)}}})
	if tap.count() != 0 {
		t.Fatal("unknown codec metadata forwarded")
	}
	for _, packets := range [][]*rtp.Packet{{boundsKeyPacket(1, 1, 640, 360)}, {boundsDeltaPacket(2, 2)}} {
		r.ReadVideoLoop("publisher", SlotCam, &boundsVideoTrack{fakeVideoTrack: fakeVideoTrack{ssrc: 42, fakeTrackReader: fakeTrackReader{packets: packets}}, mime: webrtc.MimeTypeVP8})
	}
	if tap.count() != 1 {
		t.Fatal("new track inherited previous dimensions")
	}
}

func FuzzVP8BoundsInspector(f *testing.F) {
	f.Add([]byte{0x10, 0x11, 0, 0}, uint16(2), uint32(2), true)
	f.Add(boundsKeyPacket(1, 1, 1920, 1080).Payload, uint16(2), uint32(2), true)
	f.Add([]byte{0x80, 0xf0, 0x80}, uint16(2), uint32(1), false)
	f.Fuzz(func(t *testing.T, payload []byte, seq uint16, timestamp uint32, marker bool) {
		v := vp8BoundsInspector{bounds: VideoBounds{1280, 720}}
		if !v.accept(boundsKeyPacket(1, 1, 640, 360)) {
			t.Fatal("valid seed rejected")
		}
		v.accept(&rtp.Packet{Header: rtp.Header{SequenceNumber: seq, Timestamp: timestamp, Marker: marker}, Payload: payload})
		v.accept(boundsDeltaPacket(seq+1, timestamp+1))
	})
}

func TestVideoBoundsRequireFreshKeyAfterGapOrMalformedPacket(t *testing.T) {
	v := vp8BoundsInspector{bounds: VideoBounds{1280, 720}}
	if !v.accept(boundsKeyPacket(65535, 1, 640, 360)) || !v.accept(boundsDeltaPacket(0, 2)) {
		t.Fatal("sequence wrap rejected")
	}
	if v.accept(boundsDeltaPacket(2, 3)) || v.accept(boundsDeltaPacket(3, 4)) {
		t.Fatal("sequence gap retained unknown keyframe dimensions")
	}
	if !v.accept(boundsKeyPacket(4, 5, 640, 360)) {
		t.Fatal("fresh key did not recover")
	}
	for _, data := range [][]byte{nil, {0x80}, {0x10, 0}, {0x10, 0, 0, 0, 0, 0, 0, 1, 0, 1, 0}} {
		v = vp8BoundsInspector{bounds: VideoBounds{1280, 720}}
		if !v.accept(boundsKeyPacket(1, 1, 640, 360)) {
			t.Fatal("valid seed rejected")
		}
		v.accept(&rtp.Packet{Header: rtp.Header{SequenceNumber: 2, Timestamp: 2}, Payload: data})
		if v.accept(boundsDeltaPacket(3, 3)) {
			t.Fatal("malformed packet retained known dimensions")
		}
	}
	for _, dimensions := range [][2]uint16{{0, 100}, {100, 0}, {1280, 721}, {0x4001, 1}, {1, 0x4001}} {
		if v.accept(boundsKeyPacket(7, 8, dimensions[0], dimensions[1])) {
			t.Fatal("invalid/scaled dimensions passed")
		}
	}
}

func TestVideoBoundsNegotiateOnlyInspectedCodec(t *testing.T) {
	for _, limited := range []bool{false, true} {
		bounds := VideoBounds{}
		if limited {
			bounds = VideoBounds{1920, 1080}
		}
		e, err := NewWithVideoBounds(testLogger(), nil, true, NetworkConfig{}, bounds)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = e.Close() }()
		peer, err := e.NewPeerConnection("video")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := peer.pc.AddTransceiverFromKind(webrtc.RTPCodecTypeVideo); err != nil {
			t.Fatal(err)
		}
		offer, err := peer.CreateOffer()
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(offer, "VP8/90000") {
			t.Fatal("VP8 missing")
		}
		for _, codec := range []string{"VP9/90000", "H264/90000", "AV1/90000"} {
			if strings.Contains(offer, codec) == limited {
				t.Fatalf("limited=%t unexpected codec %s", limited, codec)
			}
		}
		if limited && !strings.Contains(offer, "max-fs=8160") {
			t.Fatal("missing VP8 receiver preference")
		}
	}
}
