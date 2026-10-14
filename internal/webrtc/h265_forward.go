package webrtc

import (
	"encoding/binary"
	"time"

	"github.com/pion/rtp"
)

// h265ForwardFrame accepts RFC 7798 SRST, layer 0, no DONL/DOND. Negotiation
// must reject interleaved/multilayer modes. It retains complete RTP packets,
// never depacketizes into an unbounded NAL buffer, and never rewrites payloads.
// Join readiness requires in-band VPS/SPS/PPS before a complete IRAP picture.
// This is packet/picture validation, not a replacement for parameter-set or
// decoded-dimension validation by the policy inspector and receiver decoder.
type h265ForwardFrame struct {
	observed                        bool
	lastSSRC                        uint32
	lastSequence                    uint16
	packets                         []*rtp.Packet
	bytes, nals                     int
	started, lastPacket             time.Time
	ssrc, timestamp                 uint32
	sequence                        uint16
	fragment                        uint16
	vps, sps, pps                   bool
	vcl                             bool
	vclType, vclTID                 byte
	configurationBeforeIRAP         bool
	configurationBoundary           bool
	rejected                        bool
	rejectedSSRC, rejectedTimestamp uint32
}

func (f *h265ForwardFrame) clearUnit() {
	*f = h265ForwardFrame{observed: f.observed, lastSSRC: f.lastSSRC, lastSequence: f.lastSequence, rejected: f.rejected, rejectedSSRC: f.rejectedSSRC, rejectedTimestamp: f.rejectedTimestamp}
}
func (f *h265ForwardFrame) reject() {
	f.rejected, f.rejectedSSRC, f.rejectedTimestamp = true, f.ssrc, f.timestamp
	f.clearUnit()
}

// The two-byte HEVC header has a forbidden bit, six-bit NAL type, six-bit
// layer ID and a nonzero temporal_id_plus1. Main-profile SRST uses layer 0.
func h265Header(data []byte) (kind, tid byte, ok bool) {
	if len(data) < 2 || data[0]&0x81 != 0 || data[1]&0xf8 != 0 || data[1]&7 == 0 {
		return 0, 0, false
	}
	return data[0] >> 1, data[1] & 7, true
}

func (f *h265ForwardFrame) nal(header []byte, firstPayload byte) bool {
	kind, tid, ok := h265Header(header)
	if !ok || f.nals >= h264AccessUnitNALs {
		return false
	}
	f.nals++
	if (kind == 32 || kind == 33 || kind == 36 || kind == 37) && tid != 1 {
		return false
	}
	switch {
	case kind <= 9 || (kind >= 16 && kind <= 21):
		// A picture's first slice must actually be present; merely observing an
		// IRAP type on a later slice is not enough to start a fresh decoder.
		firstSlice := firstPayload&0x80 != 0
		if !f.vcl {
			if !firstSlice {
				return false
			}
			f.vcl, f.vclType, f.vclTID = true, kind, tid
			f.configurationBeforeIRAP = f.vps && f.sps && f.pps
		} else if firstSlice || kind != f.vclType || tid != f.vclTID {
			return false
		}
		if kind >= 16 && tid != 1 {
			return false
		}
	case kind == 32:
		f.vps = true
	case kind == 33:
		f.sps = true
	case kind == 34:
		f.pps = true
	case kind >= 35 && kind <= 40:
	default:
		return false
	}
	return true
}

// payload validates one packet and advances only bounded scalar state. The
// caller discards the whole access unit if any later member fails validation.
func (f *h265ForwardFrame) payload(data []byte) bool {
	kind, tid, ok := h265Header(data)
	if !ok || len(data) < 3 {
		return false
	}
	switch kind {
	case 48:
		if f.fragment != 0 {
			return false
		}
		rest := data[2:]
		count := 0
		minTID := byte(7)
		for len(rest) > 0 {
			if len(rest) < 2 {
				return false
			}
			size := int(binary.BigEndian.Uint16(rest))
			rest = rest[2:]
			if size < 3 || size > len(rest) {
				return false
			}
			_, nalTID, valid := h265Header(rest[:size])
			if !valid || !f.nal(rest[:2], rest[2]) {
				return false
			}
			minTID = min(minTID, nalTID)
			count++
			rest = rest[size:]
		}
		// RFC 7798 APs contain at least two NALs. Its TID is the minimum
		// temporal_id_plus1 of the aggregated NALs (all layer IDs are zero).
		return count >= 2 && tid == minTID
	case 49:
		// Unlike H264, HEVC explicitly forbids empty FU payloads.
		if len(data) < 4 {
			return false
		}
		nalType := data[2] & 63
		start, end := data[2]&0x80 != 0, data[2]&0x40 != 0
		header := [2]byte{nalType << 1, data[1]}
		identity := binary.BigEndian.Uint16(header[:])
		if start {
			if end || f.fragment != 0 || !f.nal(header[:], data[3]) {
				return false
			}
			f.fragment = identity
		} else if f.fragment == 0 || identity != f.fragment {
			return false
		}
		if end {
			f.fragment = 0
		}
		return true
	default:
		return f.fragment == 0 && f.nal(data[:2], data[2])
	}
}

// push owns buffered bytes because upstream RTP reads may reuse their memory.
// It is called after bounded RTP reordering. lost includes whole-unit gaps,
// abandoned partial units and invalid packetization, requiring keyframe repair.
func (f *h265ForwardFrame) push(p *rtp.Packet, now time.Time) (packets []*rtp.Packet, keyframe, lost bool) {
	gap := f.observed && f.lastSSRC == p.SSRC && p.SequenceNumber != f.lastSequence+1
	defer func() { f.observed, f.lastSSRC, f.lastSequence = true, p.SSRC, p.SequenceNumber; lost = lost || gap }()
	if len(f.packets) > 0 && (now.Sub(f.started) > h264ForwardMaxWait || now.Sub(f.lastPacket) > h264ForwardIdleWait) {
		f.reject()
		lost = true
	}
	if isVideoPadding(p) {
		if len(f.packets) == 0 {
			return []*rtp.Packet{p.Clone()}, false, lost
		}
		// Never conceal a gap observed first in a padding packet. Its timestamp
		// has no media meaning, but its source/sequence must remain contiguous.
		if p.SSRC != f.ssrc || p.SequenceNumber != f.sequence+1 {
			f.reject()
			return nil, false, true
		}
		if p.MarshalSize() > h264AccessUnitBytes-f.bytes || len(f.packets) >= h264AccessUnitPackets {
			f.reject()
			return nil, false, true
		}
		f.sequence = p.SequenceNumber
		f.bytes += p.MarshalSize()
		f.packets = append(f.packets, p.Clone())
		return nil, false, lost // padding cannot extend the media idle deadline
	}
	if len(f.packets) > 0 && (p.SSRC != f.ssrc || (p.Timestamp != f.timestamp && !f.configurationBoundary) || p.SequenceNumber != f.sequence+1) {
		f.reject()
		lost = true
	}
	if f.rejected && p.SSRC == f.rejectedSSRC && p.Timestamp == f.rejectedTimestamp {
		return nil, false, lost
	}
	f.rejected = false
	if len(f.packets) == 0 {
		f.ssrc, f.timestamp, f.started = p.SSRC, p.Timestamp, now
	}
	f.timestamp = p.Timestamp
	f.configurationBoundary = false
	if p.MarshalSize() > h264AccessUnitBytes-f.bytes || len(f.packets) >= h264AccessUnitPackets || !f.payload(p.Payload) {
		f.reject()
		return nil, false, true
	}
	f.lastPacket, f.sequence = now, p.SequenceNumber
	f.bytes += p.MarshalSize()
	f.packets = append(f.packets, p.Clone())
	if !p.Marker {
		return nil, false, lost
	}
	if f.fragment != 0 {
		f.reject()
		return nil, false, true
	}
	// Parameter-only units may use their own RTP timestamps and markers.
	// Retain them in original sequence order until the picture arrives; this
	// avoids replaying cached sequence numbers or fabricating payloads. The
	// same cumulative byte/packet/NAL/age bounds cover the entire prefix.
	if !f.vcl {
		f.configurationBoundary = true
		return nil, false, lost
	}
	packets, keyframe = f.packets, f.vcl && f.vclType >= 16 && f.vclType <= 21 && f.configurationBeforeIRAP
	f.clearUnit()
	return packets, keyframe, lost
}

// translateH265 shares only the RTP-header continuity path with H264. HEVC
// payload bytes never enter the VP8 payload-descriptor rewriter. frameStart is
// granted solely for the first packet of an assembled, configured IRAP unit.
func (v *videoContinuity) translateH265(pkt *rtp.Packet, now time.Time, frameStart bool) (*rtp.Packet, bool) {
	return v.translateH264(pkt, now, frameStart)
}

// h265UnitVCLType inspects only units already validated by h265ForwardFrame.
// It does not decode NAL payloads or allocate a depacketized buffer.
func h265UnitVCLType(packets []*rtp.Packet) (byte, bool) {
	for _, p := range packets {
		if isVideoPadding(p) {
			continue
		}
		kind, _, ok := h265Header(p.Payload)
		if !ok {
			return 0, false
		}
		if kind < 32 {
			return kind, true
		}
		if kind == 49 && len(p.Payload) >= 4 && p.Payload[2]&63 < 32 {
			return p.Payload[2] & 63, true
		}
		if kind == 48 {
			rest := p.Payload[2:]
			for len(rest) >= 2 {
				size := int(binary.BigEndian.Uint16(rest))
				rest = rest[2:]
				if size < 3 || size > len(rest) {
					return 0, false
				}
				kind, _, ok = h265Header(rest[:size])
				if !ok {
					return 0, false
				}
				if kind < 32 {
					return kind, true
				}
				rest = rest[size:]
			}
		}
	}
	return 0, false
}

// beginH265Frame runs once per viewer under its continuity lock, before
// translating any packet of this complete unit. RASL pictures are undecodable
// after random access to their associated IRAP (RFC 7798 section 1.1.2).
// Existing viewers keep those pictures; newly joined/resumed viewers discard
// only RASL, preserving independently decodable RADL leading pictures.
func (v *videoContinuity) beginH265Frame(packets []*rtp.Packet, keyframe bool) bool {
	if len(packets) == 0 {
		return false
	}
	kind, hasVCL := h265UnitVCLType(packets)
	fresh := !v.seen || v.pendingEpoch || v.ssrc != packets[0].SSRC
	if fresh {
		if !keyframe || !hasVCL || kind < 16 || kind > 21 {
			return false
		}
		v.h265DropRASL = true
		return true
	}
	if !hasVCL {
		return true
	} // includes stand-alone RTP padding
	if kind == 8 || kind == 9 {
		if v.h265DropRASL {
			// Every packet is contiguous and owned by the same validated unit.
			// Close the intentional sequence gap without advancing media time or
			// treating discarded source packets as successful decoder input.
			v.sequenceOffset -= uint16(len(packets))
			return false
		}
	} else if kind <= 5 || kind >= 16 {
		v.h265DropRASL = false
	}
	return true
}
