package webrtc

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestH264RejectTruncatedHeadersAndOverflow(t *testing.T) {
	// ue(v) for 2^32+39 wrapped to 39 in the dependency's uint32 reader.
	oversizeWidth := strings.Repeat("0", 32) + "1" + fmt.Sprintf("%032b", 40)
	spsBits := h264TestUE(0) + h264TestUE(0) + h264TestUE(2) + h264TestUE(1) + "0" +
		oversizeWidth + h264TestUE(22) + "1100"
	badSPS := append([]byte{0x67, 66, 0xe0, 31}, h264TestBits(spsBits)...)
	for _, tc := range []struct {
		name            string
		sps, pps, slice []byte
	}{
		{"SPS dimension overflow", badSPS, h264TestPPS, h264TestIDR},
		{"truncated PPS", h264TestSPS(640, 360), []byte{0x68, 0xc0}, h264TestIDR},
		{"truncated IDR", h264TestSPS(640, 360), h264TestPPS, []byte{0x65, 0xb8}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := h264BoundsInspector{bounds: VideoBounds{1280, 720}}
			unit, err := v.push(h264TestPacket(1, 1, true, h264TestSTAP(tc.sps, tc.pps, tc.slice)), time.Now())
			if err == nil || unit != nil || v.known {
				t.Fatalf("invalid syntax accepted: %+v %v", unit, err)
			}
		})
	}
}

func TestH264BoundsRecoveryIsSpecificToParameterChain(t *testing.T) {
	v := h264BoundsInspector{bounds: VideoBounds{1280, 720}}
	now := time.Now()
	for i, payload := range [][]byte{
		h264TestSTAP(h264TestSPSID(0, 640, 360), h264TestPPSID(0, 0), h264TestSPSID(1, 320, 240), h264TestPPSID(1, 1), h264TestSlice(0, true)),
		h264TestSTAP(h264TestSPSID(0, 960, 540)),
		h264TestSlice(1, true),
	} {
		if _, err := v.push(h264TestPacket(uint16(i+1), uint32(i+1), true, payload), now); err != nil {
			t.Fatal(err)
		}
	}
	unit, err := v.push(h264TestPacket(4, 4, true, h264TestSlice(0, false)), now)
	if err == nil || unit != nil {
		t.Fatal("IDR in one chain recovered another chain")
	}
}

func TestH264BoundsSameTimestampFragmentExpires(t *testing.T) {
	v := h264BoundsInspector{bounds: VideoBounds{1280, 720}}
	now := time.Now()
	_, err := v.push(h264TestPacket(1, 1, true, h264TestSTAP(h264TestSPS(640, 360), h264TestPPS, h264TestIDR)), now)
	if err != nil {
		t.Fatal(err)
	}
	_, err = v.push(h264TestPacket(2, 2, false, []byte{0x7c, 0x85, h264TestIDR[1]}), now)
	if err != nil {
		t.Fatal(err)
	}
	unit, err := v.push(h264TestPacket(3, 2, true, append([]byte{0x7c, 0x45}, h264TestIDR[2:]...)), now.Add(h264AccessUnitWait+time.Millisecond))
	if err == nil || unit != nil || len(v.packets) > 0 || len(v.fragment) > 0 {
		t.Fatal("expired fragment retained or admitted")
	}
}
