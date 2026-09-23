package webrtc

import (
	"time"

	"github.com/pion/rtp"
	"github.com/pion/rtp/codecs"
)

// A subscriber has one RTP stream per publisher slot. Simulcast sources have
// independent sequence and VP8 reference spaces, so switching SSRC requires a
// keyframe and a new offset in each space. The caller serializes translation
// through enqueueing; source packets remain immutable for other subscribers.
type videoContinuity struct {
	seen                           bool
	pendingEpoch                   bool
	ssrc                           uint32
	sequence, sequenceOffset       uint16
	mediaSequence                  uint16
	timestamp, timestampOffset     uint32
	picture, pictureInput          uint16
	tl0, tl0Offset, key, keyOffset byte
	last                           time.Time
	epochPackets                   uint32
}

// beginEpoch waits for a decodable frame without discarding the receiver's
// sequence and reference spaces, including when resuming the same source.
func (v *videoContinuity) beginEpoch() { v.pendingEpoch = true }

func isVideoPadding(pkt *rtp.Packet) bool {
	return pkt.Padding && len(pkt.Payload) == 0 && (pkt.Header.PaddingSize != 0 || pkt.PaddingSize != 0)
}

func (v *videoContinuity) translate(pkt *rtp.Packet, now time.Time) (*rtp.Packet, bool) {
	if isVideoPadding(pkt) {
		// Preserve sequence continuity for the current source without letting
		// probes start a stream, complete a switch, or advance frame references.
		if !v.seen || v.pendingEpoch || v.ssrc != pkt.SSRC {
			return nil, false
		}
		out := *pkt
		out.SequenceNumber += v.sequenceOffset
		out.Timestamp += v.timestampOffset
		delta := int16(out.SequenceNumber - v.sequence)
		if delta < 0 && uint32(-int32(delta)) > v.epochPackets {
			return nil, false
		}
		if delta > 0 {
			v.sequence = out.SequenceNumber
			v.epochPackets = min(32768, v.epochPackets+uint32(delta))
		}
		return &out, true
	}
	var descriptor codecs.VP8Packet
	data, err := descriptor.Unmarshal(pkt.Payload)
	if err != nil || len(data) == 0 {
		return nil, false
	}
	switching := v.seen && (v.ssrc != pkt.SSRC || v.pendingEpoch)
	if (switching || v.pendingEpoch) && (descriptor.S != 1 || descriptor.PID != 0 || len(data) < 10 || data[0]&1 != 0 || data[3] != 0x9d || data[4] != 1 || data[5] != 0x2a) {
		return nil, false
	}
	v.pendingEpoch = false
	first := !v.seen
	if switching {
		v.sequenceOffset = v.sequence + 1 - pkt.SequenceNumber
		elapsed := max(int64(1), now.Sub(v.last).Nanoseconds()*90000/int64(time.Second))
		v.timestampOffset = v.timestamp + uint32(elapsed) - pkt.Timestamp
		v.tl0Offset = v.tl0 + 1 - descriptor.TL0PICIDX
		v.keyOffset = v.key + 1 - descriptor.KEYIDX
	}
	out := *pkt
	out.SequenceNumber += v.sequenceOffset
	out.Timestamp += v.timestampOffset
	sequenceDelta := int16(out.SequenceNumber - v.sequence)
	mediaDelta := int16(out.SequenceNumber - v.mediaSequence)
	if v.seen && !switching && sequenceDelta < 0 && uint32(-int32(sequenceDelta)) > v.epochPackets {
		return nil, false
	}
	picture := descriptor.PictureID
	if switching {
		picture = (v.picture + 1) & 0x7fff
	} else if v.seen && descriptor.I != 0 {
		mask := uint16(0x7fff)
		if pkt.Payload[2]&0x80 == 0 {
			mask = 0x7f
		}
		if mediaDelta >= 0 {
			picture = (v.picture + ((descriptor.PictureID - v.pictureInput) & mask)) & 0x7fff
		} else {
			picture = (v.picture - ((v.pictureInput - descriptor.PictureID) & mask)) & 0x7fff
		}
	}
	tl0, key := descriptor.TL0PICIDX+v.tl0Offset, (descriptor.KEYIDX+v.keyOffset)&31
	if err == nil && (descriptor.I != 0 || v.tl0Offset != 0 || v.keyOffset != 0) {
		// Rebuild only the small optional descriptor. Always use a 15-bit
		// PictureID after a switch, including when the input uses seven bits.
		header := append([]byte(nil), pkt.Payload[:len(pkt.Payload)-len(data)]...)
		pos := 2
		if descriptor.I != 0 {
			width := 1
			if header[pos]&0x80 != 0 {
				width = 2
			}
			rest := append([]byte(nil), header[pos+width:]...)
			header = append(header[:pos], 0x80|byte(picture>>8), byte(picture))
			header = append(header, rest...)
			pos += 2
		}
		if descriptor.L != 0 {
			header[pos] = tl0
			pos++
		}
		if descriptor.K != 0 {
			header[pos] = header[pos]&0xe0 | key
		}
		header = append(header, data...)
		out.Payload = header
	}
	if first || switching || int16(out.SequenceNumber-v.sequence) > 0 {
		if first || switching {
			v.epochPackets = 0
		} else {
			v.epochPackets = min(32768, v.epochPackets+uint32(sequenceDelta))
		}
		v.sequence = out.SequenceNumber
	}
	if first || switching || mediaDelta > 0 {
		if first || switching || out.Timestamp != v.timestamp {
			v.last = now
		}
		v.seen, v.ssrc, v.mediaSequence = true, pkt.SSRC, out.SequenceNumber
		v.timestamp, v.picture, v.tl0, v.key = out.Timestamp, picture, tl0, key
		v.pictureInput = descriptor.PictureID
	}
	return &out, true
}
