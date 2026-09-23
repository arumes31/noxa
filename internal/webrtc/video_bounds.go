package webrtc

import (
	"encoding/binary"
	"fmt"

	"github.com/pion/rtp"
	"github.com/pion/rtp/codecs"
	"go.uber.org/zap"
)

// VideoBounds limits encoded frame dimensions. Both zero means unlimited.
// Enabled bounds require VP8 negotiation and packet inspection.
type VideoBounds struct{ Width, Height int }

func (b VideoBounds) validate() error {
	if b == (VideoBounds{}) {
		return nil
	}
	if b.Width < 1 || b.Height < 1 || b.Width > 16383 || b.Height > 16383 {
		return fmt.Errorf("video dimensions must both be zero or between 1 and 16383")
	}
	return nil
}

// NewRouterWithVideoBounds constructs a router with initial dimension limits.
func NewRouterWithVideoBounds(logger *zap.Logger, bounds VideoBounds) (*Router, error) {
	if err := bounds.validate(); err != nil {
		return nil, err
	}
	r := NewRouter(logger)
	if err := r.SetVideoLimits(0, bounds); err != nil {
		return nil, err
	}
	return r, nil
}

// A separate inspector belongs to each inbound track/layer. Unknown references,
// malformed descriptors, lost frame starts and sequence gaps require a fresh
// in-bounds keyframe. No packet buffer or decoded image is allocated.
type vp8BoundsInspector struct {
	bounds             VideoBounds
	known, frame, seen bool
	timestamp          uint32
	sequence           uint16
}

func (v *vp8BoundsInspector) stale(pkt *rtp.Packet) bool {
	return v.seen && int16(pkt.SequenceNumber-v.sequence) <= 0
}

func (v *vp8BoundsInspector) accept(pkt *rtp.Packet) bool {
	if v.stale(pkt) {
		return false
	}
	if v.seen && pkt.SequenceNumber != v.sequence+1 {
		v.known, v.frame = false, false
	}
	v.sequence, v.seen = pkt.SequenceNumber, true
	if isVideoPadding(pkt) {
		// Padding consumes a sequence number, but carries no dimensions or
		// frame boundary. A real sequence gap above still invalidates state.
		return true
	}
	var descriptor codecs.VP8Packet
	data, err := descriptor.Unmarshal(pkt.Payload)
	if err != nil || len(data) == 0 {
		v.known, v.frame = false, false
		return false
	}
	if descriptor.S == 1 && descriptor.PID == 0 {
		v.frame, v.timestamp = false, pkt.Timestamp
		if len(data) < 3 || (data[0]>>1)&7 > 3 {
			v.known = false
			return false
		}
		if data[0]&1 == 0 {
			v.known = false
			// RFC 6386 section 9.1: keyframe sync code and 14-bit dimensions.
			if len(data) < 10 || data[3] != 0x9d || data[4] != 1 || data[5] != 0x2a {
				return false
			}
			width := int(binary.LittleEndian.Uint16(data[6:8]))
			height := int(binary.LittleEndian.Uint16(data[8:10]))
			// Reject nonzero scaling fields as well as oversized dimensions.
			if width < 1 || height < 1 || width > v.bounds.Width || height > v.bounds.Height {
				return false
			}
			v.known = true
		}
		v.frame = v.known
	} else if !v.frame || v.timestamp != pkt.Timestamp {
		v.known, v.frame = false, false
		return false
	}
	allowed := v.frame && v.known
	if pkt.Marker {
		v.frame = false
	}
	return allowed
}
