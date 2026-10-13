package netproto

// VideoUpstreamFeedbackDiagnostics observes publisher-facing TWCC writes for
// one peer lifetime, shared across that peer's audio/video publications. It is
// not per-publication or per-viewer feedback. Nil means unavailable/unknown.
// StartedAt is server Unix milliseconds; SampleMS is peer-observer lifetime.
// Attempts/Reports/ReportedPackets count attempted compound writes/TWCC packets/
// their PacketStatusCount sum, NOT delivered packets or observed packet loss.
// Durations time only the wrapped writer, excluding observation-lock wait.
// Gaps order attempt entry under the short observer mutex; in-flight age starts
// at that entry. Durations and gaps are microseconds. Histogram inclusive upper
// bounds100,500,1000,5000,20000,100000,500000us, then >500000us; completed writes
// only. Attempt gap is null before two attempts, age is null before any attempt.
// Oldest in-flight age is null with no writes OR when bounded-slot overflow
// makes it unknown (InFlightAgeUnknown). Overflow counts all untracked attempts.
// Writer success is not evidence that feedback reached the publisher.
type VideoUpstreamFeedbackDiagnostics struct {
	StartedAt           int64                                 `json:"started_at"`
	SampleMS            int64                                 `json:"sample_ms"`
	Closed              bool                                  `json:"closed"`
	Attempts            uint64                                `json:"attempts"`
	CompletedWrites     uint64                                `json:"completed_writes"`
	Reports             uint64                                `json:"reports"`
	ReportedPackets     uint64                                `json:"reported_packets"`
	WriteErrors         uint64                                `json:"write_errors"`
	DurationTotalUS     uint64                                `json:"duration_total_us"`
	DurationMaxUS       uint64                                `json:"duration_max_us"`
	DurationHistogram   [8]uint64                             `json:"duration_histogram"`
	LastAttemptAgeUS    *int64                                `json:"last_attempt_age_us"`
	LastAttemptGapUS    *int64                                `json:"last_attempt_gap_us"`
	MaxAttemptGapUS     uint64                                `json:"max_attempt_gap_us"`
	InFlight            uint64                                `json:"in_flight"`
	InFlightOldestAgeUS *int64                                `json:"in_flight_oldest_age_us"`
	InFlightAgeUnknown  bool                                  `json:"in_flight_age_unknown"`
	InFlightOverflow    uint64                                `json:"in_flight_overflow"`
	Dispatch            *VideoUpstreamTWCCDispatchDiagnostics `json:"dispatch,omitempty"`
}

// VideoUpstreamTWCCDispatchDiagnostics describes the bounded publisher-facing
// feedback queue, shared by audio and video. It contains no media. Accepted
// means queued, Written means a completed write attempt (including errors),
// neither proves delivery. Drops omit whole original reports without changing
// packet arrival times or inventing packet loss. OldestAgeMS is null when empty.
type VideoUpstreamTWCCDispatchDiagnostics struct {
	Closed           bool   `json:"closed"`
	Accepted         uint64 `json:"accepted"`
	Written          uint64 `json:"written"`
	WriteErrors      uint64 `json:"write_errors"`
	DroppedOverflow  uint64 `json:"dropped_overflow"`
	DroppedExpired   uint64 `json:"dropped_expired"`
	DroppedClosed    uint64 `json:"dropped_closed"`
	RejectedOversize uint64 `json:"rejected_oversize"`
	RejectedInvalid  uint64 `json:"rejected_invalid"`
	QueuedBatches    int    `json:"queued_batches"`
	QueuedBytes      int    `json:"queued_bytes"`
	OldestAgeMS      *int64 `json:"oldest_age_ms"`
}
