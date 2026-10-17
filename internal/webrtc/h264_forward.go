package webrtc

import (
	"encoding/binary"
	"time"

	"github.com/pion/rtp"
	"noxa/internal/safecast"
)

// A congestion-paced keyframe can take longer than 500 ms to arrive without
// losing a packet. Keep progress while packets arrive, but bound both idle time
// and total residence. The absolute cap prevents endless trickle retention;
// byte, packet and NAL caps still apply independently. These forwarding limits
// do not relax the separate dimension-policy inspector.
// An unfinished unit can wait behind an upstream sequence gap. Preserve the
// existing assembly allowance in addition to that bounded repair residence.
const h264ForwardIdleWait = videoReorderWait + 500*time.Millisecond
const h264ForwardMaxWait = videoReorderWait + 2*time.Second

type h264RejectReason uint8

type h264DiscontinuityMask uint8

const (
	h264DiscontinuitySSRC h264DiscontinuityMask = 1 << iota
	h264DiscontinuityTimestamp
	h264DiscontinuitySequence
)

const (
	h264RejectMalformed h264RejectReason = iota
	h264RejectCapacity
	h264RejectDiscontinuity
	h264RejectIdle
	h264RejectAge
)

// h264ForwardFrame checks packet boundaries, not decoded dimensions. It is
// deliberately separate from the strict dimension-policy inspector. A fresh
// receiver needs the SPS, PPS and a complete IDR access unit before deltas.
type h264ForwardFrame struct {
	observed               bool
	lastSSRC               uint32
	lastSequence           uint16
	packets                []*rtp.Packet
	bytes                  int
	started                time.Time
	lastPacket             time.Time
	onReject               func(h264RejectReason, h264DiscontinuityMask)
	ssrc, timestamp        uint32
	sequence               uint16
	fragment               byte
	sps, pps, idr          bool
	nals                   int
	configurationBeforeIDR bool
	rejected               bool
	rejectedSSRC           uint32
	rejectedTimestamp      uint32
}

func (f *h264ForwardFrame) clearUnit() {
	*f = h264ForwardFrame{observed: f.observed, lastSSRC: f.lastSSRC, lastSequence: f.lastSequence, onReject: f.onReject,
		rejected: f.rejected, rejectedSSRC: f.rejectedSSRC, rejectedTimestamp: f.rejectedTimestamp}
}

func (f *h264ForwardFrame) reject(reason h264RejectReason) {
	f.rejectObserved(reason, 0)
}

func (f *h264ForwardFrame) rejectObserved(reason h264RejectReason, mask h264DiscontinuityMask) {
	if f.onReject != nil {
		f.onReject(reason, mask)
	}
	f.rejected, f.rejectedSSRC, f.rejectedTimestamp = true, f.ssrc, f.timestamp
	f.clearUnit()
}

func (f *h264ForwardFrame) expired(now time.Time) (h264RejectReason, bool) {
	if now.Sub(f.started) > h264ForwardMaxWait {
		return h264RejectAge, true
	}
	if now.Sub(f.lastPacket) > h264ForwardIdleWait {
		return h264RejectIdle, true
	}
	return 0, false
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
	// The reorder window has already repaired or abandoned gaps. Remember
	// sequence continuity across completed access units too: losing a whole
	// frame leaves no unfinished unit, but dependent frames still need a PLI.
	gap := f.observed && f.lastSSRC == p.SSRC && p.SequenceNumber != f.lastSequence+1
	defer func() {
		// Unit resets below must not erase inter-frame loss detection. Padding
		// participates in the same sequence space and can expose the gap first.
		f.observed, f.lastSSRC, f.lastSequence = true, p.SSRC, p.SequenceNumber
		lost = lost || gap
	}()
	if len(f.packets) > 0 {
		if reason, expired := f.expired(now); expired {
			f.reject(reason)
			lost = true
		}
	}
	if isVideoPadding(p) {
		// Padding shares the RTP sequence space and may be interleaved with a
		// fragmented frame. Forward it in order too: consuming it only here
		// would manufacture missing packets and downstream NACKs. Keep padding
		// behind an unfinished unit until that unit can be released atomically.
		if len(f.packets) == 0 {
			return []*rtp.Packet{p.Clone()}, false, lost
		}
		if len(f.packets) > 0 && p.SSRC == f.ssrc && p.SequenceNumber == f.sequence+1 {
			if p.MarshalSize() > h264AccessUnitBytes-f.bytes || len(f.packets) >= h264AccessUnitPackets {
				f.reject(h264RejectCapacity)
				return nil, false, true
			}
			f.sequence = p.SequenceNumber
			f.bytes += p.MarshalSize()
			f.packets = append(f.packets, p.Clone())
		}
		// Padding must not keep an unfinished media unit alive indefinitely.
		return nil, false, lost
	}
	if len(f.packets) > 0 && (p.SSRC != f.ssrc || p.Timestamp != f.timestamp || p.SequenceNumber != f.sequence+1) {
		// Observe the existing rejection predicate without storing packet
		// identifiers. Simultaneous causes occupy one fixed combination bucket.
		var mask h264DiscontinuityMask
		if p.SSRC != f.ssrc {
			mask |= h264DiscontinuitySSRC
		}
		if p.Timestamp != f.timestamp {
			mask |= h264DiscontinuityTimestamp
		}
		if p.SequenceNumber != f.sequence+1 {
			mask |= h264DiscontinuitySequence
		}
		f.rejectObserved(h264RejectDiscontinuity, mask)
		lost = true
	}
	// A later slice cannot make a rejected access unit complete. Keep this
	// bounded identity until a different media timestamp/source arrives;
	// padding above preserves sequence continuity without reopening the unit.
	if f.rejected && p.SSRC == f.rejectedSSRC && p.Timestamp == f.rejectedTimestamp {
		return nil, false, lost
	}
	f.rejected = false
	if len(f.packets) == 0 {
		f.ssrc, f.timestamp, f.started = p.SSRC, p.Timestamp, now
	}
	reject := func() ([]*rtp.Packet, bool, bool) { f.reject(h264RejectMalformed); return nil, false, true }
	if p.MarshalSize() > h264AccessUnitBytes-f.bytes || len(f.packets) >= h264AccessUnitPackets {
		f.reject(h264RejectCapacity)
		return nil, false, true
	}
	if len(p.Payload) < 2 || p.Payload[0]&0x80 != 0 {
		return reject()
	}
	f.lastPacket = now
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
		// RFC 6184 section 5.8 permits empty FU payloads and requires the
		// receiver to ignore the reserved R bit. Both header octets exist.
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
	f.clearUnit()
	return packets, keyframe, lost
}

// H.264 payload bytes must never enter the VP8 descriptor rewriter. Only RTP
// sequence/timestamp continuity changes; frameStart marks an assembled access
// unit with parameter sets before its IDR for newly watching receivers.
func (v *videoContinuity) translateH264(pkt *rtp.Packet, now time.Time, frameStart bool) (*rtp.Packet, bool) {
	if isVideoPadding(pkt) {
		// The shared RTP padding path does not parse or rewrite VP8 payloads.
		// It preserves sequence offsets without advancing media timestamps or
		// permitting padding to start a new watch/source epoch.
		return v.translate(pkt, now)
	}
	switching := v.seen && (v.ssrc != pkt.SSRC || v.pendingEpoch)
	if (!v.seen || switching || v.pendingEpoch) && !frameStart {
		return nil, false
	}
	first := !v.seen
	if switching {
		v.sequenceOffset = v.sequence + 1 - pkt.SequenceNumber
		v.timestampOffset = v.switchTimestampOffset(pkt, now)
	}
	out := *pkt
	out.SequenceNumber += v.sequenceOffset
	out.Timestamp += v.timestampOffset
	delta := rtpSequenceDelta(out.SequenceNumber, v.sequence)
	if v.seen && !switching && delta < 0 {
		backtrack, err := safecast.Int32ToUint32(-delta)
		if err != nil || backtrack > v.epochPackets {
			return nil, false
		}
	}
	v.pendingEpoch = false
	if first || switching || delta > 0 {
		if first || switching {
			v.epochPackets = 0
		} else {
			deltaPackets, err := safecast.Int32ToUint32(delta)
			if err != nil {
				return nil, false
			}
			v.epochPackets = min(32768, v.epochPackets+deltaPackets)
		}
		if first || switching || out.Timestamp != v.timestamp {
			v.last = now
		}
		v.seen, v.ssrc, v.sequence, v.timestamp = true, pkt.SSRC, out.SequenceNumber, out.Timestamp
	}
	return &out, true
}
