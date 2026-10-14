package webrtc

import (
	"bytes"
	"encoding/binary"
	"testing"
	"time"

	"github.com/pion/rtp"
)

func hevcNAL(kind byte, first bool) []byte {
	b := byte(1)
	if first {
		b = 0x80
	}
	return []byte{kind << 1, 1, b}
}
func hevcAP(nals ...[]byte) []byte {
	out := []byte{48 << 1, 1}
	for _, n := range nals {
		out = binary.BigEndian.AppendUint16(out, uint16(len(n)))
		out = append(out, n...)
	}
	return out
}
func hevcPacket(seq uint16, ts uint32, marker bool, data ...byte) *rtp.Packet {
	return &rtp.Packet{Header: rtp.Header{Version: 2, SSRC: 7, SequenceNumber: seq, Timestamp: ts, Marker: marker}, Payload: data}
}
func hevcKey(kind byte) []byte {
	return hevcAP(hevcNAL(32, false), hevcNAL(33, false), hevcNAL(34, false), hevcNAL(kind, true))
}

func TestH265ForwardCompleteOwnedIRAP(t *testing.T) {
	for kind := byte(16); kind <= 21; kind++ {
		var f h265ForwardFrame
		now := time.Now()
		config := hevcPacket(65534, 90, false, hevcAP(hevcNAL(32, false), hevcNAL(33, false), hevcNAL(34, false))...)
		if out, _, lost := f.push(config, now); len(out) != 0 || lost {
			t.Fatal("premature configuration")
		}
		config.Payload[6] = 99
		if out, _, lost := f.push(hevcPacket(65535, 90, false, 98, 1, 0x80|kind, 0x80), now); len(out) != 0 || lost {
			t.Fatal("premature fragmented IRAP")
		}
		out, key, lost := f.push(hevcPacket(0, 90, true, 98, 1, 0x40|kind, 2), now)
		if len(out) != 3 || !key || lost || out[0].Payload[6] != 1 {
			t.Fatalf("kind %d: owned complete IRAP missing: %d %v %v", kind, len(out), key, lost)
		}
	}
}

func TestH265ForwardRejectsMalformedPacketization(t *testing.T) {
	cases := map[string][]byte{
		"short": {38}, "forbidden": {0x80 | 38, 1, 0x80}, "zero temporal": {38, 0, 0x80}, "multilayer": {38, 9, 0x80},
		"empty AP": {96, 1}, "one NAL AP": hevcAP(hevcNAL(19, true)), "short AP length": {96, 1, 0}, "oversize AP": {96, 1, 0, 9, 38, 1, 0x80},
		"nested AP": hevcAP(hevcNAL(32, false), []byte{96, 1, 1}), "nested FU": hevcAP(hevcNAL(32, false), []byte{98, 1, 0x81, 0x80}),
		"PACI": {100, 1, 0, 0}, "reserved VCL": hevcNAL(22, true), "empty FU": {98, 1, 0x93}, "FU start end": {98, 1, 0xd3, 0x80}, "orphan FU end": {98, 1, 0x53, 1},
		"later slice alone": hevcNAL(19, false), "IRAP temporal layer": {38, 2, 0x80},
		"VPS temporal layer": {64, 2, 1},
		"SPS temporal layer": {66, 2, 1},
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			var f h265ForwardFrame
			out, key, lost := f.push(hevcPacket(1, 90, true, data...), time.Now())
			if len(out) != 0 || key || !lost {
				t.Fatal("malformed packet accepted")
			}
		})
	}
}

func TestH265ForwardRejectsBrokenUnitsAndRecovers(t *testing.T) {
	for _, mode := range []string{"gap", "ssrc", "timestamp", "fragment type", "fragment temporal", "marker before end", "nested start", "idle", "age", "bytes", "padding gap"} {
		t.Run(mode, func(t *testing.T) {
			var f h265ForwardFrame
			now := time.Now()
			f.push(hevcPacket(1, 90, false, 98, 1, 0x93, 0x80), now)
			p := hevcPacket(2, 90, true, 98, 1, 0x53, 1)
			switch mode {
			case "gap":
				p.SequenceNumber = 3
			case "ssrc":
				p.SSRC = 8
			case "timestamp":
				p.Timestamp = 180
			case "fragment type":
				p.Payload[2] = 0x54
			case "fragment temporal":
				p.Payload[1] = 2
			case "marker before end":
				p.Payload[2] = 19
			case "nested start":
				p.Payload[2] = 0x93
			case "idle":
				now = now.Add(h264ForwardIdleWait + time.Millisecond)
			case "age":
				now = now.Add(h264ForwardMaxWait + time.Millisecond)
			case "bytes":
				p.Payload = make([]byte, h264AccessUnitBytes)
			case "padding gap":
				p = recoveryPaddingPacket(7, 3, 0)
			}
			if out, key, lost := f.push(p, now); len(out) != 0 || key || !lost {
				t.Fatalf("broken %s accepted", mode)
			}
			// No later same-unit slice may undo a rejection.
			later := hevcPacket(p.SequenceNumber+1, p.Timestamp, true, hevcNAL(19, true)...)
			later.SSRC = p.SSRC
			if mode == "padding gap" {
				later.Timestamp = 90
			}
			if out, key, _ := f.push(later, now); len(out) != 0 || key {
				t.Fatal("rejected timestamp reopened")
			}
			if out, key, _ := f.push(hevcPacket(p.SequenceNumber+2, 270, true, hevcKey(19)...), now); len(out) != 1 || !key {
				t.Fatal("new intact IRAP failed recovery")
			}
		})
	}
}

func TestH265ForwardRequiresCompletePictureAndPriorConfiguration(t *testing.T) {
	for _, nals := range [][][]byte{
		{hevcNAL(19, true), hevcNAL(32, false), hevcNAL(33, false), hevcNAL(34, false)},
		{hevcNAL(33, false), hevcNAL(34, false), hevcNAL(19, true)},
	} {
		var f h265ForwardFrame
		out, key, lost := f.push(hevcPacket(1, 90, true, hevcAP(nals...)...), time.Now())
		if len(out) != 1 || key || lost {
			t.Fatal("configuration without preceding VPS/SPS/PPS certified fresh join")
		}
	}
	for _, nals := range [][][]byte{
		{hevcNAL(19, true), hevcNAL(19, true)}, {hevcNAL(19, true), hevcNAL(1, false)}, {hevcNAL(19, false), hevcNAL(19, false)},
	} {
		var f h265ForwardFrame
		out, _, lost := f.push(hevcPacket(1, 90, true, hevcAP(nals...)...), time.Now())
		if len(out) != 0 || !lost {
			t.Fatal("inconsistent picture accepted")
		}
	}
}

func TestH265ForwardPaddingAndBounds(t *testing.T) {
	now := time.Now()
	var f h265ForwardFrame
	f.push(hevcPacket(1, 90, false, 98, 1, 0x81, 0x80), now)
	if out, _, lost := f.push(recoveryPaddingPacket(7, 2, 0), now); len(out) != 0 || lost {
		t.Fatal("padding escaped incomplete frame")
	}
	if out, key, lost := f.push(hevcPacket(3, 90, true, 98, 1, 0x41, 1), now); len(out) != 3 || key || lost || !isVideoPadding(out[1]) {
		t.Fatal("interleaved padding corrupted frame")
	}
	f.push(hevcPacket(4, 180, false, 98, 1, 0x81, 0x80), now)
	for i := 1; i <= h264AccessUnitPackets; i++ {
		out, _, lost := f.push(recoveryPaddingPacket(7, uint16(i+4), 0), now)
		if len(out) != 0 || lost != (i == h264AccessUnitPackets) {
			t.Fatal("packet retention not bounded")
		}
	}
	if len(f.packets) != 0 || f.bytes != 0 {
		t.Fatal("rejected capacity retained packets")
	}
	var nals [][]byte
	for i := 0; i <= h264AccessUnitNALs; i++ {
		nals = append(nals, hevcNAL(39, false))
	}
	f = h265ForwardFrame{}
	if out, _, lost := f.push(hevcPacket(1, 90, true, hevcAP(nals...)...), now); len(out) != 0 || !lost {
		t.Fatal("NAL retention not bounded")
	}
}

func TestH265ForwardLossBetweenUnits(t *testing.T) {
	var f h265ForwardFrame
	now := time.Now()
	f.push(hevcPacket(1, 90, true, hevcNAL(1, true)...), now)
	if _, _, lost := f.push(hevcPacket(3, 180, true, hevcNAL(1, true)...), now); !lost {
		t.Fatal("whole frame loss hidden")
	}
	if _, _, lost := f.push(hevcPacket(4, 270, true, hevcNAL(1, true)...), now); lost {
		t.Fatal("old loss persisted")
	}
}

func TestH265ContinuityHeaderOnly(t *testing.T) {
	var v videoContinuity
	now := time.Now()
	if _, ok := v.translateH265(hevcPacket(1, 90, true, hevcNAL(1, true)...), now, false); ok {
		t.Fatal("delta started viewer")
	}
	p := hevcPacket(2, 180, true, hevcKey(19)...)
	out, ok := v.translateH265(p, now, true)
	if !ok || !bytes.Equal(out.Payload, p.Payload) {
		t.Fatal("HEVC payload altered")
	}
	v.beginEpoch()
	if _, ok := v.translateH265(hevcPacket(3, 270, true, hevcNAL(1, true)...), now, false); ok {
		t.Fatal("delta resumed viewer")
	}
	p = hevcPacket(200, 9000, true, hevcKey(19)...)
	out, ok = v.translateH265(p, now.Add(time.Second), true)
	if !ok || out.SequenceNumber != 3 || out.Timestamp != 90180 || !bytes.Equal(out.Payload, p.Payload) {
		t.Fatal("HEVC epoch continuity failed")
	}
}

func TestH265ForwardSeparateConfigurationUnits(t *testing.T) {
	var f h265ForwardFrame
	now := time.Now()
	var out []*rtp.Packet
	var key, lost bool
	for i, kind := range []byte{32, 33, 34, 19} {
		out, key, lost = f.push(hevcPacket(uint16(i+1), uint32(90*(i+1)), true, hevcNAL(kind, kind == 19)...), now)
		if lost {
			t.Fatal("intact separate configuration rejected")
		}
		if i < 3 && len(out) != 0 {
			t.Fatal("parameter prefix escaped before decodable picture")
		}
	}
	if len(out) != 4 || !key || out[0].Timestamp != 90 || out[3].Timestamp != 360 {
		t.Fatal("separate parameter units not preserved")
	}
	var v videoContinuity
	if !v.beginH265Frame(out, key) {
		t.Fatal("configured IRAP not admitted")
	}
	for i, p := range out {
		wire, ok := v.translateH265(p, now, key && i == 0)
		if !ok || wire.SequenceNumber != p.SequenceNumber || wire.Timestamp != p.Timestamp {
			t.Fatal("separate parameters rewrote source ordering")
		}
	}
}

func TestH265ContinuitySuppressesOnlyUndecodableLeadingPictures(t *testing.T) {
	now := time.Now()
	var fresh, existing videoContinuity
	existing.seen, existing.ssrc, existing.sequence = true, 7, 9
	// Both see CRA; only the fresh receiver needs to discard its RASL.
	cra := []*rtp.Packet{hevcPacket(10, 900, true, hevcKey(21)...)}
	for _, v := range []*videoContinuity{&fresh, &existing} {
		if !v.beginH265Frame(cra, true) {
			t.Fatal("CRA rejected")
		}
		if _, ok := v.translateH265(cra[0], now, true); !ok {
			t.Fatal("CRA not translated")
		}
	}
	radl := []*rtp.Packet{hevcPacket(11, 600, true, hevcNAL(6, true)...)}
	for _, v := range []*videoContinuity{&fresh, &existing} {
		if !v.beginH265Frame(radl, false) {
			t.Fatal("decodable RADL dropped")
		}
		v.translateH265(radl[0], now, false)
	}
	rasl := []*rtp.Packet{hevcPacket(12, 300, true, hevcNAL(8, true)...)}
	if fresh.beginH265Frame(rasl, false) {
		t.Fatal("fresh viewer received undecodable RASL")
	}
	if !existing.beginH265Frame(rasl, false) {
		t.Fatal("established viewer lost decodable RASL")
	}
	existing.translateH265(rasl[0], now, false)
	trailing := []*rtp.Packet{hevcPacket(13, 1200, true, hevcNAL(1, true)...)}
	if !fresh.beginH265Frame(trailing, false) {
		t.Fatal("trailing picture rejected")
	}
	out, ok := fresh.translateH265(trailing[0], now, false)
	if !ok || out.SequenceNumber != 12 || out.Timestamp != 1200 || fresh.h265DropRASL {
		t.Fatal("intentional RASL drop created sequence gap or shifted media time")
	}
	fresh.beginEpoch()
	if fresh.beginH265Frame(trailing, false) {
		t.Fatal("delta resumed epoch")
	}
}
func TestH265ForwardIdleTrickleAndParameterPrefixBounds(t *testing.T) {
	for _, mode := range []string{"padding idle", "media trickle", "parameter prefix"} {
		t.Run(mode, func(t *testing.T) {
			var f h265ForwardFrame
			now := time.Now()
			first := hevcPacket(1, 90, false, 98, 1, 0x81, 0x80)
			if mode == "parameter prefix" {
				first = hevcPacket(1, 90, true, hevcNAL(32, false)...)
			}
			f.push(first, now)
			rejected := false
			for i := 1; i < 60; i++ {
				p := hevcPacket(uint16(i+1), 90, false, 98, 1, 1, 1)
				if mode == "padding idle" {
					p = recoveryPaddingPacket(7, uint16(i+1), 0)
				}
				if mode == "parameter prefix" {
					p = hevcPacket(uint16(i+1), uint32(90+i), true, hevcNAL(32, false)...)
				}
				_, _, lost := f.push(p, now.Add(time.Duration(i)*100*time.Millisecond))
				if lost {
					rejected = true
					break
				}
			}
			if !rejected {
				t.Fatal("unfinished HEVC media retained indefinitely")
			}
			// An expiry can begin a new independently valid prefix, but cannot keep
			// the old cumulative retention start time or any old parameter packets.
			if len(f.packets) > 1 {
				t.Fatal("expired prefix retained old packets")
			}
		})
	}
}

func TestH265ForwardReorderedFURepair(t *testing.T) {
	var reorder videoReorder
	var f h265ForwardFrame
	now := time.Unix(1, 0)
	var complete []*rtp.Packet
	keys := 0
	emit := func(p *rtp.Packet, _ string) {
		out, key, lost := f.push(p, now)
		if lost {
			t.Fatal("intact repaired unit rejected")
		}
		complete = append(complete, out...)
		if key {
			keys++
		}
	}
	const mime = "video/H265"
	reorder.push(hevcPacket(1, 90, false, hevcAP(hevcNAL(32, false), hevcNAL(33, false), hevcNAL(34, false))...), mime, now, emit)
	reorder.push(hevcPacket(2, 90, false, 98, 1, 0x93, 0x80), mime, now, emit)
	reorder.push(hevcPacket(4, 90, true, 98, 1, 0x53, 3), mime, now, emit)
	if len(complete) != 0 {
		t.Fatal("incomplete IRAP escaped reorder")
	}
	now = now.Add(2400 * time.Millisecond)
	reorder.push(hevcPacket(3, 90, false, 98, 1, 19, 2), mime, now, emit)
	if len(complete) != 4 || keys != 1 {
		t.Fatal("late repaired HEVC IRAP missing")
	}
}
