package netproto

// VideoRTXRepairDiagnostics counts eligible successful negotiated RTX writes
// within an established forwarding epoch, not browser receipt or decoding.
// An RTX write cannot establish a new source/publication/watch epoch; until
// a primary write does so, this operator-only diagnostic remains unavailable.
// First means the first successful repair for an exact authorization ticket;
// repeat means another for that ticket in the same forwarding diagnostic epoch.
// Tickets expire after one second and are bounded; a new ticket is a new first,
// even if its RTP sequence repeats. Primary/legacy same-SSRC retransmissions,
// pure padding (including RTX-wrapped source padding), coalesced repairs,
// denied writes, zero writes, and failures do
// not count. Counters reset with source/publication/watch forwarding epochs.
// Nil means unavailable; a present zero struct means supported with no repairs.
type VideoRTXRepairDiagnostics struct {
	FirstRepairs  uint64 `json:"first_repairs"`
	RepeatRepairs uint64 `json:"repeat_repairs"`
	// Upper bounds in microseconds: 5000,20000,50000,100000,200000,1000000,+Inf.
	// Gap measures serialized successful-write observation time, not wire RTT.
	RepeatGapHistogram [7]uint64 `json:"repeat_gap_histogram"`
	MaxRepeatGapUS     uint64    `json:"max_repeat_gap_us"`
}
