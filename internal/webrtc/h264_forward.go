package webrtc

import (
	"encoding/binary"
	"time"

	"github.com/pion/rtp"
)

// h264ForwardFrame checks packet boundaries, not decoded dimensions. It is
// deliberately separate from the strict dimension-policy inspector. A fresh
// receiver needs the SPS, PPS and a complete IDR access unit before deltas.
type h264ForwardFrame struct {
	packets                []*rtp.Packet
	bytes                  int
	started                time.Time
	ssrc, timestamp        uint32
	sequence               uint16
	fragment               byte
	sps, pps, idr          bool
	nals                   int
	configurationBeforeIDR bool
}

func (f *h264ForwardFrame) nal(header byte) bool {
	if header&0x80 != 0 || f.nals >= h264AccessUnitNALs {
		return false
	}
	f.nals++
	switch header & 31 {
	case 1, 6, 9, 12:
	case 5:
		if !f.idr {
			f.configurationBeforeIDR = f.sps && f.pps
		}
		f.idr = true
	case 7:
		f.sps = true
	case 8:
		f.pps = true
	default:
		return false
	}
	return true
}

// push owns buffered packets because ReadRTP may reuse the source memory.
// lost is also true when a new valid unit replaces an incomplete old one.
func (f *h264ForwardFrame) push(p *rtp.Packet, now time.Time) (packets []*rtp.Packet, keyframe, lost bool) {
	if isVideoPadding(p) {
		// Padding shares the RTP sequence space and may be interleaved with a
		// fragmented frame. Consume contiguous padding without treating it as
		// media or allowing it to hide an actual missing packet.
		if len(f.packets) > 0 && p.SSRC == f.ssrc && p.SequenceNumber == f.sequence+1 {
			f.sequence = p.SequenceNumber
		}
		return nil, false, false
	}
	if len(f.packets) > 0 && (p.SSRC != f.ssrc || p.Timestamp != f.timestamp || p.SequenceNumber != f.sequence+1 || now.Sub(f.started) > h264AccessUnitWait) {
		*f = h264ForwardFrame{}
		lost = true
	}
	reject := func() ([]*rtp.Packet, bool, bool) { *f = h264ForwardFrame{}; return nil, false, true }
	if len(p.Payload) < 2 || p.Payload[0]&0x80 != 0 || p.MarshalSize() > h264AccessUnitBytes-f.bytes || len(f.packets) >= h264AccessUnitPackets {
		return reject()
	}
	if len(f.packets) == 0 {
		f.ssrc, f.timestamp, f.started = p.SSRC, p.Timestamp, now
	}
	f.sequence = p.SequenceNumber
	f.bytes += p.MarshalSize()
	switch p.Payload[0] & 31 {
	case 24:
		if f.fragment != 0 {
			return reject()
		}
		data := p.Payload[1:]
		for len(data) > 0 {
			if len(data) < 2 {
				return reject()
			}
			size := int(binary.BigEndian.Uint16(data))
			data = data[2:]
			if size < 2 || size > len(data) || !f.nal(data[0]) {
				return reject()
			}
			data = data[size:]
		}
	case 28:
		if len(p.Payload) < 3 || p.Payload[1]&0x20 != 0 {
			return reject()
		}
		header := p.Payload[0]&0xe0 | p.Payload[1]&31
		start, end := p.Payload[1]&0x80 != 0, p.Payload[1]&0x40 != 0
		if start {
			if end || f.fragment != 0 || !f.nal(header) {
				return reject()
			}
			f.fragment = header
		} else if f.fragment == 0 || f.fragment != header {
			return reject()
		}
		if end {
			f.fragment = 0
		}
	default:
		if f.fragment != 0 || !f.nal(p.Payload[0]) {
			return reject()
		}
	}
	f.packets = append(f.packets, p.Clone())
	if !p.Marker {
		return nil, false, lost
	}
	if f.fragment != 0 {
		return reject()
	}
	packets, keyframe = f.packets, f.configurationBeforeIDR && f.idr
	*f = h264ForwardFrame{}
	return packets, keyframe, lost
}

// H.264 payload bytes must never enter the VP8 descriptor rewriter. Only RTP
// sequence/timestamp continuity changes; frameStart marks an assembled access
// unit with parameter sets before its IDR for newly watching receivers.
func (v *videoContinuity) translateH264(pkt *rtp.Packet, now time.Time, frameStart bool) (*rtp.Packet, bool) {
	switching := v.seen && (v.ssrc != pkt.SSRC || v.pendingEpoch)
	if (!v.seen || switching || v.pendingEpoch) && !frameStart {
		return nil, false
	}
	first := !v.seen
	if switching {
		v.sequenceOffset = v.sequence + 1 - pkt.SequenceNumber
		elapsed := max(int64(1), now.Sub(v.last).Nanoseconds()*90000/int64(time.Second))
		v.timestampOffset = v.timestamp + uint32(elapsed&0xffffffff) - pkt.Timestamp
	}
	out := *pkt
	out.SequenceNumber += v.sequenceOffset
	out.Timestamp += v.timestampOffset
	delta := rtpSequenceDelta(out.SequenceNumber, v.sequence)
	if v.seen && !switching && delta < 0 && uint32(-int32(delta)) > v.epochPackets {
		return nil, false
	}
	v.pendingEpoch = false
	if first || switching || delta > 0 {
		if first || switching {
			v.epochPackets = 0
		} else {
			v.epochPackets = min(32768, v.epochPackets+uint32(delta))
		}
		if first || switching || out.Timestamp != v.timestamp {
			v.last = now
		}
		v.seen, v.ssrc, v.sequence, v.timestamp = true, pkt.SSRC, out.SequenceNumber, out.Timestamp
	}
	return &out, true
}
