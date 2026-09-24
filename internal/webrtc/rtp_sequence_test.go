package webrtc

import "testing"

func TestRTPSequenceDelta(t *testing.T) {
	// Sweep a full sequence space with origins on both sides of rollover.
	for _, origin := range []uint16{0, 1, 32767, 32768, 65535} {
		for offset := int32(-32768); offset <= 32767; offset++ {
			sequence := uint16((int32(origin) + offset) & 0xffff)
			if got := rtpSequenceDelta(sequence, origin); got != offset {
				t.Fatalf("sequence %d after %d: got %d, want %d", sequence, origin, got, offset)
			}
		}
	}
}
