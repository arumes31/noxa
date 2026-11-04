package webrtc

import (
	"time"

	"github.com/pion/rtp"
)

// One recipient SSRC survives a publisher's microphone/capture replacement.
// Keep constant offsets within each claimed source so loss, reordering and
// duplicate sequence numbers retain their meaning to SRTP and the decoder.
// The caller serializes translation through enqueueing and rolls it back if
// admission fails. Payloads and source headers are never modified.
type audioContinuity struct {
	seen                       bool
	epoch                      uint64
	ssrc                       uint32
	sequence, sequenceOffset   uint16
	timestamp, timestampOffset uint32
	epochPackets               uint32
	last                       time.Time
}

func (a *audioContinuity) translate(packet *rtp.Packet, epoch uint64, clockRate uint32, now time.Time, preferred uint32, havePreferred bool) (*rtp.Packet, bool) {
	if clockRate == 0 || (a.seen && epoch < a.epoch) {
		return nil, false
	}
	switching := a.seen && (epoch != a.epoch || packet.SSRC != a.ssrc)
	// A live claimed reader has a single SSRC. A stale reader may not reclaim
	// its output merely by arriving after the replacement's first packet.
	if a.seen && epoch != 0 && epoch == a.epoch && packet.SSRC != a.ssrc {
		return nil, false
	}
	first := !a.seen
	if switching {
		a.sequenceOffset = a.sequence + 1 - packet.SequenceNumber
		elapsed := max(time.Duration(0), now.Sub(a.last))
		ticks := uint64(elapsed/time.Second)*uint64(clockRate) + uint64(elapsed%time.Second)*uint64(clockRate)/uint64(time.Second)
		a.timestampOffset = a.timestamp + max(uint32(1), uint32(ticks&0xffffffff)) - packet.Timestamp
		if step := packet.Timestamp + preferred - a.timestamp; havePreferred && step != 0 && step < timingPreserveMaxStep {
			a.timestampOffset = preferred
		}
	}
	out := *packet
	out.SequenceNumber += a.sequenceOffset
	out.Timestamp += a.timestampOffset
	delta := rtpSequenceDelta(out.SequenceNumber, a.sequence)
	if a.seen && !switching && delta < 0 && uint32(-int32(delta)) > a.epochPackets {
		return nil, false
	}
	if first || switching || delta > 0 {
		switch {
		case first:
			a.epochPackets = 32768
		case switching:
			a.epochPackets = 0
		default:
			// This is a forward RTP distance, including uint16 sequence wrap.
			a.epochPackets = min(uint32(32768), a.epochPackets+uint32(out.SequenceNumber-a.sequence))
		}
		a.seen, a.epoch, a.ssrc = true, epoch, packet.SSRC
		a.sequence, a.timestamp, a.last = out.SequenceNumber, out.Timestamp, now
	}
	return &out, true
}
