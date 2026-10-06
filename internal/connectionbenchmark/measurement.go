package connectionbenchmark

import (
	"math"
	"slices"
	"sync"
	"time"
)

// Timing reports milliseconds, measured with the client's monotonic clock.
type Timing struct {
	P50 float64 `json:"p50_ms"`
	P95 float64 `json:"p95_ms"`
	Max float64 `json:"max_ms"`
}

// Result contains only bounded numeric measurements, never RTP payloads or addresses.
type Result struct {
	DurationSeconds int     `json:"duration_seconds"`
	IntervalMS      int     `json:"interval_ms"`
	DrainSeconds    int     `json:"drain_seconds"`
	Sent            int     `json:"sent"`
	Returned        int     `json:"returned"`
	Unreturned      int     `json:"unreturned"`
	Duplicates      int     `json:"duplicates"`
	Reordered       int     `json:"reordered"`
	RoundTrip       *Timing `json:"round_trip"`
	ArrivalGap      *Timing `json:"arrival_gap"`
	SendLateness    *Timing `json:"send_lateness"`
	Protocol        string  `json:"protocol"`
	CandidateType   string  `json:"candidate_type"`
	ServerVersion   string  `json:"server_version"`
}

type packetKey struct {
	sequence  uint16
	timestamp uint32
}
type packetSent struct {
	at       time.Time
	ordinal  int
	returned bool
}
type measurement struct {
	mu                                    sync.Mutex
	sent                                  map[packetKey]packetSent
	rtt, gaps, lateness                   []float64
	lastReceived                          time.Time
	highestOrdinal, duplicates, reordered int
}

func newMeasurement() *measurement {
	return &measurement{sent: make(map[packetKey]packetSent), highestOrdinal: -1}
}
func (m *measurement) sentPacket(sequence uint16, timestamp uint32, at time.Time, late time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.sent) >= 1000 {
		return
	}
	m.sent[packetKey{sequence, timestamp}] = packetSent{at: at, ordinal: len(m.sent)}
	m.lateness = append(m.lateness, float64(max(0, late))/float64(time.Millisecond))
}
func (m *measurement) receivedPacket(sequence uint16, timestamp uint32, at time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := packetKey{sequence, timestamp}
	p, ok := m.sent[key]
	if !ok || at.Before(p.at) {
		return
	}
	if p.returned {
		m.duplicates++
		return
	}
	p.returned = true
	m.sent[key] = p
	if p.ordinal < m.highestOrdinal {
		m.reordered++
	}
	m.highestOrdinal = max(m.highestOrdinal, p.ordinal)
	m.rtt = append(m.rtt, float64(at.Sub(p.at))/float64(time.Millisecond))
	if !m.lastReceived.IsZero() {
		m.gaps = append(m.gaps, float64(max(0, at.Sub(m.lastReceived)))/float64(time.Millisecond))
	}
	m.lastReceived = at
}
func timing(samples []float64) *Timing {
	if len(samples) == 0 {
		return nil
	}
	values := slices.Clone(samples)
	slices.Sort(values)
	percentile := func(f float64) float64 {
		return math.Round(values[max(0, int(math.Ceil(float64(len(values))*f))-1)]*100) / 100
	}
	return &Timing{P50: percentile(.5), P95: percentile(.95), Max: percentile(1)}
}
func (m *measurement) summary() Result {
	m.mu.Lock()
	defer m.mu.Unlock()
	return Result{DurationSeconds: 20, IntervalMS: 20, DrainSeconds: 2, Sent: len(m.sent), Returned: len(m.rtt),
		Unreturned: len(m.sent) - len(m.rtt), Duplicates: m.duplicates, Reordered: m.reordered,
		RoundTrip: timing(m.rtt), ArrivalGap: timing(m.gaps), SendLateness: timing(m.lateness)}
}
