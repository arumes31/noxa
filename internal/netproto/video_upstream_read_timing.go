package netproto

// VideoUpstreamReadTimingDiagnostics is publisher-peer lifetime, covering all
// incoming RTP (audio, video and RTX), not one publication or SSRC. It observes
// every 64th read invocation, including invocations that return errors. This
// systematic sample is not a random/unbiased sample and can miss isolated stalls. Absent
// means unknown. Zero completed samples is not evidence of zero latency.
//
// Completed durations start immediately after the underlying decrypted SRTP
// reader returns and end when the outermost default interceptor returns. They
// include intervening interceptor work, TWCC's synchronous handoff, scheduling
// and observer overhead; they exclude the underlying blocking Read itself,
// kernel arrival, SRTP queue residence and application work between reads.
// Histograms cover completed samples only: an unfinished call is not measured.
// Duration bucket inclusive upper bounds in microseconds are 100,500,1000,5000,
// 20000,100000,500000,+Inf. No packet identifiers, timestamps or contents appear.
type VideoUpstreamReadTimingDiagnostics struct {
	StartedAt         int64     `json:"started_at"`
	SampleMS          int64     `json:"sample_ms"`
	Closed            bool      `json:"closed"`
	SampleEvery       uint64    `json:"sample_every"`
	ReadCalls         uint64    `json:"read_calls"`
	SampledCalls      uint64    `json:"sampled_calls"`
	CompletedSamples  uint64    `json:"completed_samples"`
	ReadErrors        uint64    `json:"read_errors"`
	MissingInner      uint64    `json:"missing_inner"`
	InvalidToken      uint64    `json:"invalid_token"`
	DurationTotalUS   uint64    `json:"duration_total_us"`
	DurationMaxUS     uint64    `json:"duration_max_us"`
	DurationHistogram [8]uint64 `json:"duration_histogram"`
}
