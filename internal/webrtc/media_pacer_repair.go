package webrtc

import (
	"encoding/binary"

	"github.com/pion/rtp"
)

// Only queued repairs are coalesced. The original media packet is never
// suppressed, and a later NACK is allowed after a repair leaves the queue.
// The internal authorization ticket survives Pion's RTX header copy and is
// indispensable: equal RTP identifiers may belong to a different source or
// watch generation. Final permission checks still run when the queue drains.
type mediaRepairKey struct {
	stream    *pacedStream
	ticket    uint64
	ssrc      uint32
	timestamp uint32
	sequence  uint16
}

func queuedRepairKey(stream *pacedStream, header *rtp.Header, payload []byte) mediaRepairKey {
	if stream.audio || stream.rtxSSRC == 0 || header.SSRC != stream.rtxSSRC || len(payload) < 2 || len(header.CSRC) != 2 {
		return mediaRepairKey{}
	}
	ticket := uint64(header.CSRC[0])<<32 | uint64(header.CSRC[1])
	if ticket == 0 {
		return mediaRepairKey{}
	}
	return mediaRepairKey{stream: stream, ticket: ticket, ssrc: header.SSRC,
		timestamp: header.Timestamp, sequence: binary.BigEndian.Uint16(payload[:2])}
}
