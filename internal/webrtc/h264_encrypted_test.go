package webrtc

import (
	"bytes"
	"encoding/hex"
	"testing"
	"time"
)

func encryptedH264Slice(key bool) []byte {
	header := byte(0x41)
	if key {
		header = 0x65
	}
	nal := append([]byte{header, 0x88}, bytes.Repeat([]byte{0x55}, 48)...)
	return append(nal, 12, 0)
}
func TestH264EncryptedDeclaredBoundsAndCiphertext(t *testing.T) {
	v := h264BoundsInspector{bounds: VideoBounds{1920, 1080}, encrypted: true}
	now := time.Now()
	key := h264TestSTAP(h264TestSPS(1920, 1080), h264TestPPSID(0, 0), encryptedH264Slice(true))
	unit, err := v.push(h264TestPacket(1, 3000, true, key), now)
	if err != nil || unit == nil || !unit.keyframe || unit.width != 1920 || unit.height != 1080 {
		t.Fatalf("valid encrypted keyframe: %+v %v", unit, err)
	}
	delta := encryptedH264Slice(false)
	unit, err = v.push(h264TestPacket(2, 6000, true, delta), now)
	if err != nil || unit == nil || unit.keyframe {
		t.Fatalf("ciphertext delta was parsed as plaintext: %+v %v", unit, err)
	}
	if !bytes.Equal(unit.packets[0].Payload, delta) {
		t.Fatal("encrypted payload changed")
	}
}
func TestH264EncryptedRejectsMissingConfigurationAndInvalidTrailer(t *testing.T) {
	for _, payload := range [][]byte{encryptedH264Slice(false), encryptedH264Slice(true), h264TestSTAP(h264TestSPS(3840, 2160), h264TestPPSID(0, 0), encryptedH264Slice(true))} {
		v := h264BoundsInspector{bounds: VideoBounds{1920, 1080}, encrypted: true}
		if _, err := v.push(h264TestPacket(1, 3000, true, payload), time.Now()); err == nil {
			t.Fatal("unsafe encrypted access unit accepted")
		}
	}
	bad := encryptedH264Slice(true)
	bad[len(bad)-2] = 0
	v := h264BoundsInspector{bounds: VideoBounds{1920, 1080}, encrypted: true}
	if _, err := v.push(h264TestPacket(1, 3000, true, h264TestSTAP(h264TestSPS(1920, 1080), h264TestPPSID(0, 0), bad)), time.Now()); err == nil {
		t.Fatal("missing frame trailer accepted")
	}
}

func TestH264EncryptedTemporalPrefix(t *testing.T) {
	for _, prefix := range [][]byte{{0x6e, 0xc0, 0x80, 0x07, 0xc0}, {0x6e, 0xc0, 0x81, 0x07, 0xc0}, {0x6e, 0xc0, 0x80, 0x17, 0xc0}, {0x4e, 0x80, 0x80, 0x0f, 0x20}} {
		payload := h264TestSTAP(h264TestSPS(1920, 1080), h264TestPPSID(0, 0), prefix, encryptedH264Slice(true))
		v := h264BoundsInspector{bounds: VideoBounds{1920, 1080}, encrypted: true}
		unit, err := v.push(h264TestPacket(1, 3000, true, payload), time.Now())
		valid := bytes.Equal(prefix, []byte{0x6e, 0xc0, 0x80, 0x07, 0xc0})
		if valid && (err != nil || unit == nil) || !valid && err == nil {
			t.Fatalf("prefix %x: unit=%v err=%v", prefix, unit, err)
		}
		if valid {
			f := h264ForwardFrame{}
			packets, key, lost := f.push(h264TestPacket(1, 3000, true, payload), time.Now())
			if len(packets) != 1 || !key || lost || !bytes.Equal(packets[0].Payload, payload) {
				t.Fatal("forwarder changed temporal prefix")
			}
		}
	}
}

// Captured parameter bytes from Chromium's own synthetic test pattern after
// a joining viewer requests a keyframe. The final zero belongs to Annex-B,
// not SPS syntax; removing it from forwarded media would break its AEAD AAD.
func TestH264EncryptedAnnexBParameterPaddingPreserved(t *testing.T) {
	sps, err := hex.DecodeString("6742c01f8c680a02ff966e021a020f08846a00")
	if err != nil {
		t.Fatal(err)
	}
	pps := append(h264TestPPSID(0, 0), 0)
	payload := h264TestSTAP(sps, pps, encryptedH264Slice(true))
	v := h264BoundsInspector{bounds: VideoBounds{1920, 1080}, encrypted: true}
	unit, err := v.push(h264TestPacket(1, 3000, true, payload), time.Now())
	if err != nil || unit == nil {
		t.Fatalf("valid Annex-B parameter padding rejected: %v", err)
	}
	if !bytes.Equal(unit.packets[0].Payload, payload) || !bytes.Equal(unit.configuration[0], sps) || !bytes.Equal(unit.configuration[1], pps) {
		t.Fatal("authenticated header bytes changed")
	}
	oversized := bytes.Clone(sps)
	oversized = append(oversized, bytes.Repeat([]byte{0}, h264ParameterBytes)...)
	v = h264BoundsInspector{bounds: VideoBounds{1920, 1080}, encrypted: true}
	if _, err = v.push(h264TestPacket(1, 3000, true, h264TestSTAP(oversized, pps, encryptedH264Slice(true))), time.Now()); err == nil {
		t.Fatal("trailing zeros bypassed parameter size limit")
	}
}
