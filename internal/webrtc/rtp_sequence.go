package webrtc

// rtpSequenceDelta orders sequence numbers in the signed half of their 16-bit
// wire space. Widen before interpreting the sign to preserve rollover without
// an overflowing uint16-to-int16 conversion.
func rtpSequenceDelta(sequence, previous uint16) int32 {
	delta := int32(sequence - previous)
	if delta >= 1<<15 {
		delta -= 1 << 16
	}
	return delta
}
