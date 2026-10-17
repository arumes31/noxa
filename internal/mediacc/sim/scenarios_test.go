package sim

import (
	"testing"
	"time"
)

// Steady rates of a 1080p60 screen share from a hardware encoder: typical
// desktop content, and high-motion content.
const (
	demand1080p60       = 8_000_000
	demand1080p60Motion = 12_000_000
)

func constant(rate int) func(time.Duration) int { return func(time.Duration) int { return rate } }

// stepped returns base, except low during [from, to).
func stepped(base, low int, from, to time.Duration) func(time.Duration) int {
	return func(t time.Duration) int {
		if t >= from && t < to {
			return low
		}
		return base
	}
}

// Receivers are ~40 ms RTT from the relay with jitter: small uniform jitter
// plus occasional aggregation/Wi-Fi style delay spikes of 15-35 ms.
func jitteryLink(capacity func(time.Duration) int) linkConfig {
	return linkConfig{
		capacity:     capacity,
		oneWay:       20 * time.Millisecond,
		jitter:       4 * time.Millisecond,
		spikeEvery:   4 * time.Second,
		spikeDelay:   [2]time.Duration{15 * time.Millisecond, 35 * time.Millisecond},
		spikeLength:  [2]time.Duration{50 * time.Millisecond, 150 * time.Millisecond},
		returnJitter: 2 * time.Millisecond,
	}
}

func logReports(t *testing.T, w *world, from time.Duration) map[string]viewerReport {
	t.Helper()
	reports := map[string]viewerReport{}
	for _, v := range w.viewers {
		r := v.report(from)
		reports[v.name] = r
		t.Log(r.String())
	}
	t.Log(w.source.String())
	return reports
}

var seeds = []int64{1, 2, 3}

// A stable link with ample capacity: the estimate must never fall below what
// the stream needs, and the viewer must stay at full quality without
// periodic disturbances.
func TestSimStableJitteryLink(t *testing.T) {
	for _, seed := range seeds {
		w := newWorld(seed)
		newSource(w, 60, constant(demand1080p60))
		v := w.addViewer("A", jitteryLink(constant(30_000_000)))
		w.run(120 * time.Second)
		r := logReports(t, w, 5*time.Second)["A"]
		if r.fullQuality < 0.99 || r.freezes != 0 || r.starvedTime != 0 || r.targetDrops > 12 {
			t.Errorf("seed %d: stable link degraded: %s", seed, r)
		}
		if w.source.keyframesForced != 1 {
			t.Errorf("seed %d: stable viewer forced publisher keyframes: %s", seed, w.source)
		}
		t.Logf("seed %d estimate: %s", seed, v.timeline(0, 60*time.Second))
	}
}

// A link only 50% above the stream's rate, with the same jitter: jitter must
// not be mistaken for congestion often enough to starve the stream.
func TestSimStableTightLink(t *testing.T) {
	for _, seed := range seeds {
		w := newWorld(seed)
		newSource(w, 60, constant(demand1080p60))
		w.addViewer("A", jitteryLink(constant(12_000_000)))
		w.run(120 * time.Second)
		r := logReports(t, w, 5*time.Second)["A"]
		if r.fullQuality < 0.98 || r.starvedTime > time.Second || w.source.keyframesForced > 2 {
			t.Errorf("seed %d: tight link degraded: %s / %s", seed, r, w.source)
		}
	}
}

// Small bursty loss (about 1%, bursts of ~3 packets) must be repaired and must
// not collapse the estimate.
func TestSimBurstyLoss(t *testing.T) {
	for _, seed := range seeds {
		w := newWorld(seed)
		newSource(w, 60, constant(demand1080p60))
		cfg := jitteryLink(constant(30_000_000))
		cfg.lossRate, cfg.lossBurst = 0.01, 3
		w.addViewer("A", cfg)
		w.run(90 * time.Second)
		r := logReports(t, w, 5*time.Second)["A"]
		if r.fullQuality < 0.95 || r.starvedTime != 0 || w.source.keyframesForced > 3 {
			t.Errorf("seed %d: bursty loss collapsed quality: %s / %s", seed, r, w.source)
		}
	}
}

// A second viewer joins a running stream. The existing viewer must not be
// disturbed (no freeze, no estimate below demand) and the new one must reach
// full quality within two seconds; the only publisher keyframes are the two
// watch starts.
func TestSimViewerJoin(t *testing.T) {
	for _, demand := range []int{demand1080p60, demand1080p60Motion} {
		for _, seed := range seeds {
			w := newWorld(seed)
			newSource(w, 60, constant(demand))
			w.source.keyframeFactor = 10
			a := w.addViewer("A", jitteryLink(constant(30_000_000)))
			var b *viewer
			w.after(20*time.Second, func() { b = w.addViewer("B", jitteryLink(constant(30_000_000))) })
			w.run(60 * time.Second)
			reports := logReports(t, w, 5*time.Second)
			joinRamp := b.recovery(20*time.Second, 3*time.Second)
			t.Logf("demand %.0fM seed %d: B full quality %v after joining", float64(demand)/1e6, seed, joinRamp)
			if ra := reports["A"]; ra.freezes != 0 || ra.starvedTime != 0 || ra.fullQuality < 0.99 {
				t.Errorf("demand %d seed %d: join disturbed the existing viewer: %s", demand, seed, ra)
			}
			if joinRamp < 0 || joinRamp > 2*time.Second || reports["B"].freezes != 0 {
				t.Errorf("demand %d seed %d: new viewer ramp %v: %s", demand, seed, joinRamp, reports["B"])
			}
			if w.source.keyframesForced > 2 {
				t.Errorf("demand %d seed %d: join caused extra publisher keyframes: %s", demand, seed, w.source)
			}
			_ = a
		}
	}
}

// Transient congestion: capacity collapses to 3 Mbit/s for 3 s, then returns.
// Full quality must return within a few seconds, without a keyframe storm.
func TestSimTransientCongestion(t *testing.T) {
	for _, seed := range seeds {
		w := newWorld(seed)
		newSource(w, 60, constant(demand1080p60))
		v := w.addViewer("A", jitteryLink(stepped(30_000_000, 3_000_000, 20*time.Second, 23*time.Second)))
		w.run(60 * time.Second)
		r := logReports(t, w, 5*time.Second)["A"]
		recovery := v.recovery(23*time.Second, 5*time.Second)
		t.Logf("seed %d: full quality %v after congestion cleared; estimate %s", seed, recovery, v.timeline(18*time.Second, 32*time.Second))
		if recovery < 0 || recovery > 4*time.Second {
			t.Errorf("seed %d: slow recovery %v: %s", seed, recovery, r)
		}
		if w.source.keyframesForced > 4 {
			t.Errorf("seed %d: congestion caused a keyframe storm: %s", seed, w.source)
		}
		if r.overhead > 0.05 {
			t.Errorf("seed %d: recovery cost %.1f%% extra traffic", seed, 100*r.overhead)
		}
	}
}

// A slow viewer (5 Mbit/s for an 8 Mbit/s stream) among fast ones must not
// degrade the fast viewers, neither through the relay nor by making the
// shared publisher encode keyframes on a fixed cadence.
func TestSimSlowViewerAmongFast(t *testing.T) {
	for _, seed := range seeds[:2] {
		w := newWorld(seed)
		newSource(w, 60, constant(demand1080p60))
		w.addViewer("A", jitteryLink(constant(30_000_000)))
		w.addViewer("B", jitteryLink(constant(30_000_000)))
		w.after(10*time.Second, func() { w.addViewer("slow", jitteryLink(constant(5_000_000))) })
		w.run(70 * time.Second)
		reports := logReports(t, w, 12*time.Second)
		for _, name := range []string{"A", "B"} {
			if r := reports[name]; r.fullQuality < 0.99 || r.freezes != 0 || r.starvedTime != 0 {
				t.Errorf("seed %d: slow viewer degraded %s: %s", seed, name, r)
			}
		}
		if n := w.source.keyframeByReason["viewer-slow"]; n > 8 {
			t.Errorf("seed %d: slow viewer forced %d publisher keyframes in 60 s", seed, n)
		}
	}
}
