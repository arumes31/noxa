package webrtc

import (
	"encoding/binary"
	"strings"
	"testing"
	"time"

	"github.com/pion/rtp"
)

// Build minimal progressive constrained-baseline parameter sets independently
// of the parser. Cropping exercises 360-line images coded as 368 lines.
func h264TestUE(value uint32) string {
	n := value + 1
	var b strings.Builder
	for x := n; x > 1; x >>= 1 {
		b.WriteByte('0')
	}
	for bit := 31; bit >= 0; bit-- {
		if n>>bit == 0 {
			continue
		}
		for ; bit >= 0; bit-- {
			if n&(1<<bit) != 0 {
				b.WriteByte('1')
			} else {
				b.WriteByte('0')
			}
		}
	}
	return b.String()
}

func h264TestBits(bits string) []byte {
	bits += "1"
	out := make([]byte, (len(bits)+7)/8)
	for i, b := range bits {
		if b == '1' {
			out[i/8] |= 1 << (7 - i%8)
		}
	}
	return out
}

func h264TestSPS(width, height uint32) []byte {
	return h264TestSPSID(0, width, height)
}

func h264TestSPSID(id, width, height uint32) []byte {
	bits := h264TestUE(id) + h264TestUE(0) + h264TestUE(2) + h264TestUE(1) + "0" +
		h264TestUE((width+15)/16-1) + h264TestUE((height+15)/16-1) + "11"
	if width%16 != 0 || height%16 != 0 {
		bits += "1" + h264TestUE(0) + h264TestUE(((width+15)/16*16-width)/2) + h264TestUE(0) + h264TestUE(((height+15)/16*16-height)/2)
	} else {
		bits += "0"
	}
	return append([]byte{0x67, 66, 0xe0, 31}, h264TestBits(bits+"0")...)
}

func h264TestPPSID(id, sps uint32) []byte {
	return append([]byte{0x68}, h264TestBits(h264TestUE(id)+h264TestUE(sps)+"00"+
		h264TestUE(0)+h264TestUE(0)+h264TestUE(0)+"000"+h264TestUE(0)+h264TestUE(0)+h264TestUE(0)+"100")...)
}

func h264TestSlice(pps uint32, key bool) []byte {
	header, typ, tail := byte(0x41), uint32(0), "0001"+"000"
	if key {
		header, typ, tail = 0x65, 2, "0000"+h264TestUE(0)+"00"
	}
	return append([]byte{header}, h264TestBits(h264TestUE(0)+h264TestUE(typ)+h264TestUE(pps)+tail+
		h264TestUE(0)+h264TestUE(1)+"101010")...)
}

var h264TestPPS = h264TestPPSID(0, 0)
var h264TestIDR = h264TestSlice(0, true)
var h264TestDelta = h264TestSlice(0, false)

func h264TestSTAP(nals ...[]byte) []byte {
	out := []byte{0x78}
	for _, nal := range nals {
		out = binary.BigEndian.AppendUint16(out, uint16(len(nal)))
		out = append(out, nal...)
	}
	return out
}

func h264TestPacket(seq uint16, timestamp uint32, marker bool, payload []byte) *rtp.Packet {
	return &rtp.Packet{Header: rtp.Header{Version: 2, SSRC: 42, SequenceNumber: seq, Timestamp: timestamp, Marker: marker}, Payload: payload}
}

func TestH264BoundsCompleteUnitsAndFragmentation(t *testing.T) {
	now := time.Now()
	v := h264BoundsInspector{bounds: VideoBounds{1280, 720}}
	unit, err := v.push(h264TestPacket(1, 1, true, h264TestSTAP(h264TestSPS(640, 360), h264TestPPS, h264TestIDR)), now)
	if err != nil || unit == nil || !unit.keyframe || unit.width != 640 || unit.height != 360 || len(unit.packets) != 1 {
		t.Fatalf("keyframe: %+v, %v", unit, err)
	}
	start := h264TestPacket(2, 2, false, []byte{0x7c, 0x85, 0xb8})
	if unit, err = v.push(start, now); unit != nil || err != nil {
		t.Fatalf("partial fragment escaped: %+v %v", unit, err)
	}
	start.Payload[2] = 0 // buffered packets must own their bytes
	unit, err = v.push(h264TestPacket(3, 2, true, append([]byte{0x7c, 0x45}, h264TestIDR[2:]...)), now)
	if err != nil || unit == nil || !unit.keyframe || len(unit.packets) != 2 || unit.packets[0].Payload[2] != 0xb8 {
		t.Fatalf("fragmented IDR: %+v %v", unit, err)
	}
}

func TestH264BoundsRejectInvalidUnits(t *testing.T) {
	for _, tc := range []struct {
		name    string
		payload []byte
	}{
		{"oversize", h264TestSTAP(h264TestSPS(1920, 1080), h264TestPPS, h264TestIDR)},
		{"unknown PPS", []byte{0x65, 0xb4, 0x80}},
		{"truncated STAP", []byte{0x78, 0, 4, 0x65}},
		{"nested STAP", h264TestSTAP([]byte{0x78, 0, 0})},
		{"interleaved packetization", []byte{0x79, 0, 0, 0}},
		{"forbidden bit", []byte{0xe5, 0xb8, 0x40}},
		{"FU end without start", []byte{0x7c, 0x45, 0xb8}},
		{"FU start and end", []byte{0x7c, 0xc5, 0xb8}},
		{"unfinished FU at marker", []byte{0x7c, 0x85, 0xb8}},
		{"parameter set after VCL", h264TestSTAP(h264TestSPS(640, 360), h264TestPPS, h264TestIDR, h264TestSPS(1920, 1080))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := h264BoundsInspector{bounds: VideoBounds{1280, 720}}
			if unit, err := v.push(h264TestPacket(1, 1, true, tc.payload), time.Now()); err == nil || unit != nil {
				t.Fatalf("invalid unit admitted: %+v %v", unit, err)
			}
			if unit, _ := v.push(h264TestPacket(2, 2, true, h264TestIDR), time.Now()); unit != nil {
				t.Fatal("failed unit leaked parameter sets")
			}
		})
	}
}

func TestH264BoundsLossTimeoutAndReplacementRequireFreshConfiguration(t *testing.T) {
	for _, cause := range []string{"gap", "timeout", "timestamp", "replacement"} {
		t.Run(cause, func(t *testing.T) {
			now := time.Now()
			v := h264BoundsInspector{bounds: VideoBounds{1280, 720}}
			_, err := v.push(h264TestPacket(65534, 1, true, h264TestSTAP(h264TestSPS(640, 360), h264TestPPS, h264TestIDR)), now)
			if err != nil {
				t.Fatal(err)
			}
			seq := uint16(65535)
			switch cause {
			case "gap":
				seq = 0
			case "timeout":
				_, _ = v.push(h264TestPacket(seq, 2, false, []byte{0x7c, 0x85, 0xb8}), now)
				seq++
				now = now.Add(time.Second)
			case "timestamp":
				_, _ = v.push(h264TestPacket(seq, 2, false, []byte{0x7c, 0x85, 0xb8}), now)
				seq++
			case "replacement":
				_, _ = v.push(h264TestPacket(seq, 2, true, h264TestSTAP(h264TestSPS(320, 240))), now)
				seq++
			}
			if unit, _ := v.push(h264TestPacket(seq, 3, true, h264TestDelta), now); unit != nil {
				t.Fatal("unknown reference survived discontinuity")
			}
			unit, err := v.push(h264TestPacket(seq+1, 4, true, h264TestSTAP(h264TestSPS(640, 360), h264TestPPS, h264TestIDR)), now)
			if err != nil || unit == nil {
				t.Fatalf("fresh recovery: %+v %v", unit, err)
			}
		})
	}
}

func TestH264BoundsMemoryLimits(t *testing.T) {
	v := h264BoundsInspector{bounds: VideoBounds{1280, 720}}
	payload := make([]byte, h264AccessUnitBytes+1)
	payload[0] = 0x65
	if unit, err := v.push(h264TestPacket(1, 1, true, payload), time.Now()); unit != nil || err == nil {
		t.Fatal("oversized unit admitted")
	}
	if len(v.packets) != 0 || len(v.fragment) != 0 {
		t.Fatal("rejected unit retained buffers")
	}
}

func TestH264BoundsPaddingDoesNotTerminateFragment(t *testing.T) {
	v := h264BoundsInspector{bounds: VideoBounds{1280, 720}}
	now := time.Now()
	_, err := v.push(h264TestPacket(1, 1, true, h264TestSTAP(h264TestSPS(640, 360), h264TestPPS, h264TestIDR)), now)
	if err != nil {
		t.Fatal(err)
	}
	_, err = v.push(h264TestPacket(2, 2, false, []byte{0x7c, 0x85, 0xb8}), now)
	if err != nil {
		t.Fatal(err)
	}
	padding := h264TestPacket(3, 999, false, nil)
	padding.Padding, padding.Header.PaddingSize = true, 8
	if unit, err := v.push(padding, now); unit != nil || err != nil {
		t.Fatalf("padding: %+v %v", unit, err)
	}
	unit, err := v.push(h264TestPacket(4, 2, true, append([]byte{0x7c, 0x45}, h264TestIDR[2:]...)), now)
	if err != nil || unit == nil {
		t.Fatalf("padding broke fragment: %+v %v", unit, err)
	}
}

func FuzzH264BoundsInspector(f *testing.F) {
	f.Add(h264TestSTAP(h264TestSPS(640, 360), h264TestPPS, h264TestIDR))
	f.Add([]byte{0x7c, 0x85, 0xb8})
	f.Add([]byte{0x78, 0, 255, 0x67})
	f.Fuzz(func(t *testing.T, payload []byte) {
		v := h264BoundsInspector{bounds: VideoBounds{1280, 720}}
		now := time.Now()
		_, _ = v.push(h264TestPacket(1, 1, true, h264TestSTAP(h264TestSPS(640, 360), h264TestPPS, h264TestIDR)), now)
		for i := 0; i < 3; i++ {
			unit, _ := v.push(h264TestPacket(uint16(i+2), 2, i == 2, payload), now)
			if v.bytes > h264AccessUnitBytes || len(v.packets) > h264AccessUnitPackets || len(v.nals) > h264AccessUnitNALs || len(v.fragment) > h264AccessUnitBytes || len(v.sps) > 32 || len(v.pps) > 256 {
				t.Fatal("unbounded buffering")
			}
			if unit != nil && (unit.width < 1 || unit.width > 1280 || unit.height < 1 || unit.height > 720) {
				t.Fatalf("unbounded dimensions: %+v", unit)
			}
		}
	})
}
