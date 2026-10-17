// Package keyframe holds the relay's policy for receiver-initiated keyframe
// requests (PLI/FIR) on a single shared publisher encoder.
package keyframe

import (
	"sync"
	"time"
)

const (
	// BacklogLimit is the queued-media delay above which a receiver cannot
	// receive a new keyframe promptly: it would wait behind that backlog.
	BacklogLimit = 250 * time.Millisecond
	// While its backlog persists, one receiver may force a publisher keyframe
	// at most this often, doubling up to MaxBackloggedInterval.
	MinBackloggedInterval = 2 * time.Second
	MaxBackloggedInterval = 16 * time.Second
)

// FeedbackGate decides whether one receiver's own keyframe request may reach
// the publisher. Every forwarded request makes the single upload encode an
// IDR for all receivers; a receiver whose path cannot currently carry the
// stream would otherwise force one every coalescing window (1 s), degrading
// everyone's quality on a fixed cadence while it cannot use the keyframe
// either. Such requests are deferred while that receiver's relay backlog
// persists (with an exponentially growing allowance so it still recovers a
// picture), and forwarded at once when it can receive the keyframe. Browsers
// repeat keyframe requests while they wait, so a deferred request is retried
// by the receiver itself. The zero value is ready to use.
type FeedbackGate struct {
	mu       sync.Mutex
	last     time.Time
	interval time.Duration
	deferred uint64
}

// Allow reports whether a receiver request arriving at now, with the given
// relay backlog for that receiver, may be forwarded to the publisher.
func (g *FeedbackGate) Allow(now time.Time, backlog time.Duration) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if backlog <= BacklogLimit {
		g.interval = 0
		g.last = now

		return true
	}
	if g.interval == 0 {
		g.interval = MinBackloggedInterval
		if g.last.IsZero() || now.Sub(g.last) >= g.interval {
			g.last = now

			return true
		}
	} else if now.Sub(g.last) >= g.interval {
		g.interval = min(2*g.interval, MaxBackloggedInterval)
		g.last = now

		return true
	}
	g.deferred++

	return false
}

// Deferred returns how many requests were held back.
func (g *FeedbackGate) Deferred() uint64 {
	g.mu.Lock()
	defer g.mu.Unlock()

	return g.deferred
}
