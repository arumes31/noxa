package webrtc

import "time"

// Inline in each already-bounded authorization ticket (4096 entries, one second).
// Only videoForwardDiagnostic.mu accesses these fields after ticket admission.
// No packet identifier, content, address, or per-packet log is exported.
type videoRepairTicketDiagnostic struct {
	epoch uint64
	seen  bool
	last  time.Time
}

// Called once per successful negotiated RTX write under d.mu. It neither
// decides whether a repair is sent nor establishes that a browser received it.
func (d *videoForwardDiagnostic) observeRepair(ticket *videoRepairTicketDiagnostic, now time.Time) {
	if !ticket.seen || ticket.epoch != d.repairEpoch {
		ticket.epoch, ticket.seen, ticket.last = d.repairEpoch, true, now
		d.repairs.FirstRepairs++
		return
	}
	gap := uint64(max(0, now.Sub(ticket.last).Microseconds()))
	ticket.last = now
	d.repairs.RepeatRepairs++
	d.repairs.MaxRepeatGapUS = max(d.repairs.MaxRepeatGapUS, gap)
	bucket := len(d.repairs.RepeatGapHistogram) - 1
	for i, bound := range [...]uint64{5000, 20000, 50000, 100000, 200000, 1000000} {
		if gap <= bound {
			bucket = i
			break
		}
	}
	d.repairs.RepeatGapHistogram[bucket]++
}
