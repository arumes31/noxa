package webrtc

import (
	"bytes"
	"testing"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/rtp/codecs"

	"noxa/internal/framecrypt"
)

// nativeH264Packets encrypts one encoder access unit like the desktop native
// sender (CanonicalH264 + EncryptH264) and packetizes it with pion.
func nativeH264Packets(t *testing.T, enc *framecrypt.Encryptor, au, sps, pps []byte, seq *uint16, ts uint32) ([]*rtp.Packet, []byte, error) {
	t.Helper()
	canonical, _, err := framecrypt.CanonicalH264(au, sps, pps)
	if err != nil {
		return nil, nil, err
	}
	frame, err := enc.EncryptH264(canonical, 0x1234, ts)
	if err != nil {
		t.Fatal(err)
	}
	payloads := (&codecs.H264Payloader{}).Payload(1200, frame)
	packets := make([]*rtp.Packet, len(payloads))
	for i, payload := range payloads {
		*seq++
		packets[i] = &rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: 102, SequenceNumber: *seq, Timestamp: ts, SSRC: 0x1234, Marker: i == len(payloads)-1}, Payload: payload}
	}
	return packets, frame, nil
}

func TestNativeH264FramesPassEncryptedInspector(t *testing.T) {
	enc := framecrypt.NewEncryptor()
	if err := enc.SetKey(1, 3, bytes.Repeat([]byte{9}, 32)); err != nil {
		t.Fatal(err)
	}
	if err := enc.Activate(1); err != nil {
		t.Fatal(err)
	}
	sps, pps := h264TestSPS(1920, 1080), h264TestPPSID(0, 0)
	sc := []byte{0, 0, 1}
	slice := func(header byte, n int) []byte {
		out := []byte{header, 0x88}
		for i := range n {
			out = append(out, byte(i*31+7))
		}
		return out
	}
	// Encoder output shapes: AUD + SEI + in-band parameters + large IDR (FU-A),
	// IDR without parameters (inserted from the sequence header), deltas.
	units := [][]byte{
		bytes.Join([][]byte{sc, {0x09, 0x10}, sc, {0x06, 5, 1, 0x80}, sc, sps, sc, pps, sc, slice(0x65, 5000)}, nil),
		bytes.Join([][]byte{sc, slice(0x41, 300)}, nil),
		bytes.Join([][]byte{sc, {0x09, 0x30}, sc, slice(0x41, 3000)}, nil),
		bytes.Join([][]byte{sc, slice(0x65, 800)}, nil),
	}
	v := h264BoundsInspector{bounds: VideoBounds{1920, 1080}, encrypted: true}
	var seq uint16
	for i, au := range units {
		ts := uint32(3000 * (i + 1))
		packets, frame, err := nativeH264Packets(t, enc, au, sps, pps, &seq, ts)
		if err != nil {
			t.Fatal(err)
		}
		var unit *h264AccessUnit
		var rebuilt []byte
		depacketizer := &codecs.H264Packet{}
		for _, packet := range packets {
			if unit, err = v.push(packet, time.Now()); err != nil {
				t.Fatalf("unit %d rejected by SFU inspector: %v", i, err)
			}
			nal, err := depacketizer.Unmarshal(packet.Payload)
			if err != nil {
				t.Fatal(err)
			}
			rebuilt = append(rebuilt, nal...)
		}
		if unit == nil || unit.keyframe != bytes.Contains(au, []byte{0, 0, 1, 0x65}) {
			t.Fatalf("unit %d: %+v", i, unit)
		}
		// Receivers rebuild 4-byte start codes; the authenticated clear header must match.
		if !bytes.Equal(rebuilt, frame) {
			t.Fatalf("unit %d: depacketized frame differs from the encrypted frame", i)
		}
	}
}

func TestNativeH264InspectorRejectsUnsafeUnits(t *testing.T) {
	enc := framecrypt.NewEncryptor()
	_ = enc.SetKey(1, 0, bytes.Repeat([]byte{1}, 32))
	_ = enc.Activate(1)
	sps, pps := h264TestSPS(1920, 1080), h264TestPPSID(0, 0)
	idr := []byte{0, 0, 0, 1, 0x65, 0x88, 1, 2, 3}
	var seq uint16
	if _, _, err := nativeH264Packets(t, enc, idr, nil, nil, &seq, 3000); err == nil {
		t.Fatal("keyframe without SPS/PPS was packaged")
	}
	if _, _, err := nativeH264Packets(t, enc, append(append([]byte(nil), idr...), idr...), sps, pps, &seq, 3000); err == nil {
		t.Fatal("two-slice access unit was packaged")
	}
	// The inspector itself refuses an encrypted IDR without parameter sets.
	frame, err := enc.EncryptH264(append(idr, bytes.Repeat([]byte{7}, 40)...), 1, 3000)
	if err != nil {
		t.Fatal(err)
	}
	v := h264BoundsInspector{bounds: VideoBounds{1920, 1080}, encrypted: true}
	payloads := (&codecs.H264Payloader{}).Payload(1200, frame)
	if _, err := v.push(&rtp.Packet{Header: rtp.Header{Version: 2, SequenceNumber: 1, Timestamp: 3000, Marker: len(payloads) == 1}, Payload: payloads[0]}, time.Now()); err == nil {
		t.Fatal("encrypted IDR without SPS/PPS accepted")
	}
}
