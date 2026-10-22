package netproto

import "noxa/internal/traffic"

// Aggregate metadata only. No per-client identities, addresses or channel names.
type ServerOverview struct {
	ObservedAt     int64            `json:"observed_at"`
	Clients        int              `json:"clients"`
	LatencyMS      *float64         `json:"latency_ms"`
	LatencySamples int              `json:"latency_samples"`
	LossPercent    *float64         `json:"loss_percent"`
	LossSamples    int              `json:"loss_samples"`
	JitterMS       *float64         `json:"jitter_ms"`
	JitterSamples  int              `json:"jitter_samples"`
	Traffic        *traffic.Summary `json:"traffic,omitempty"`
}
