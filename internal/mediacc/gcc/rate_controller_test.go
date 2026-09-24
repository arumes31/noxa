// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package gcc

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"noxa/internal/mediacc/cc"
)

func TestAdditiveIncreaseCannotLowerTargetForSparseMedia(t *testing.T) {
	now := time.Now()
	c := newRateController(func() time.Time { return now }, 130000, 5000, 50000000, func(DelayStats) {})
	c.latestReceivedRate = 34000
	c.latestDecreaseRate = &exponentialMovingAverage{average: 34000, stdDeviation: 1000}
	c.lastUpdate = now.Add(-time.Second)
	if got := c.increase(now); got != c.target {
		t.Fatalf("sparse media target %d -> %d", c.target, got)
	}
	c.latestReceivedRate = 88000
	c.latestDecreaseRate = &exponentialMovingAverage{average: 88000, stdDeviation: 1000}
	c.lastUpdate = now.Add(-time.Second)
	if got := c.increase(now); got != 132000 {
		t.Fatalf("throughput increase cap ignored: %d", got)
	}
	c.latestReceivedRate = 34000
	if got := c.decrease(); got != 110500 {
		t.Fatalf("genuine congestion decrease blocked: %d", got)
	}
}

func TestDelayControllerRetainsDecreaseStateAndUsesItsClock(t *testing.T) {
	now := time.Unix(100, 0)
	c := newRateController(func() time.Time { return now }, 100000, 5000, 50000000, func(DelayStats) {})
	c.onReceivedRate(100000)
	c.onDelayStats(DelayStats{Usage: usageNormal})
	now = now.Add(time.Second)
	c.onDelayStats(DelayStats{Usage: usageOver})
	decreased := c.target
	now = now.Add(time.Second)
	c.onDelayStats(DelayStats{Usage: usageNormal})
	if c.delayStats.State != stateHold || c.target != decreased {
		t.Fatalf("decrease must settle before increasing: state=%v target=%d", c.delayStats.State, c.target)
	}
	now = now.Add(time.Second)
	c.onDelayStats(DelayStats{Usage: usageNormal})
	if c.delayStats.State != stateIncrease || !c.lastUpdate.Equal(now) {
		t.Fatalf("increase used a different clock: %v != %v", c.lastUpdate, now)
	}
}

func TestOveruseCannotIncreaseTarget(t *testing.T) {
	c := newRateController(time.Now, 100000, 5000, 50000000, func(DelayStats) {})
	c.onReceivedRate(200000)
	if got := c.decrease(); got > c.target {
		t.Fatalf("overuse raised target: %d -> %d", c.target, got)
	}
}

func TestMultiplicativeIncreaseHoldsUnmeasuredCapacity(t *testing.T) {
	now := time.Unix(100, 0)
	c := newRateController(func() time.Time { return now }, 1_500_000, 5000, 50_000_000, func(DelayStats) {})
	c.onReceivedRate(50_000)
	c.lastUpdate = now.Add(-time.Second)
	if got := c.increase(now); got != 1_500_000 {
		t.Fatalf("sparse traffic inflated unmeasured capacity to %d", got)
	}
	c.onReceivedRate(1_100_000)
	if got := c.increase(now.Add(time.Second)); got != 1_620_000 {
		t.Fatalf("measured throughput did not permit growth: %d", got)
	}
}

func TestBackoffWaitsForHighRTTAndUsesEffectivePacedRate(t *testing.T) {
	now := time.Unix(100, 0)
	c := newRateController(func() time.Time { return now }, 1_500_000, 5000, 50_000_000, func(DelayStats) {})
	c.setPacedBitrate(200_000)
	c.updateRTT(800 * time.Millisecond)
	c.onReceivedRate(50_000)
	c.onDelayStats(DelayStats{Usage: usageNormal})
	c.onDelayStats(DelayStats{Usage: usageOver})
	if c.target != 170_000 {
		t.Fatalf("backoff ignored actual loss-limited pacing rate: %d", c.target)
	}
	now = now.Add(799 * time.Millisecond)
	c.onDelayStats(DelayStats{Usage: usageOver})
	if c.target != 170_000 {
		t.Fatal("reduced again before the prior rate could be observed")
	}
	now = now.Add(time.Millisecond)
	c.onDelayStats(DelayStats{Usage: usageOver})
	if c.target != 144_500 {
		t.Fatal("continued overuse failed to reduce after one RTT")
	}
	for range 100 {
		now = now.Add(time.Second)
		c.onReceivedRate(c.target / 2)
		c.onDelayStats(DelayStats{Usage: usageOver})
	}
	if c.target != 5000 {
		t.Fatalf("persistent congestion did not reach configured minimum: %d", c.target)
	}
}

func TestLossLimitedFeedbackDoesNotEraseRecoveryBetweenLossUpdates(t *testing.T) {
	start := time.Unix(100, 0)
	now := start
	paced := 100_000
	var c *rateController
	c = newRateController(func() time.Time { return now }, paced, 5000, 50_000_000, func(ds DelayStats) {
		// Loss control permits growth every 200ms, while delay feedback can
		// arrive more frequently. Its held estimate must not reset AIMD.
		if now.Sub(start)%(200*time.Millisecond) == 0 {
			paced = int(float64(paced) * 1.05)
		}
		paced = min(paced, ds.TargetBitrate)
		c.setPacedBitrate(paced)
	})
	c.lastUpdate = now
	c.onReceivedRate(200_000)
	c.onDelayStats(DelayStats{Usage: usageNormal})
	for range 100 {
		now = now.Add(10 * time.Millisecond)
		c.onDelayStats(DelayStats{Usage: usageNormal})
	}
	if paced < 107_000 || paced > 108_000 {
		t.Fatalf("loss update cadence erased the elapsed-time increase: paced=%d, want about 108000", paced)
	}
}

func TestDelayBackoffDoesNotPinAnAlreadyLossLimitedPacer(t *testing.T) {
	now := time.Unix(100, 0)
	c := newRateController(func() time.Time { return now }, 600_000, 5000, 50_000_000, func(DelayStats) {})
	c.setPacedBitrate(100_000)
	c.onReceivedRate(180_000)
	c.onDelayStats(DelayStats{Usage: usageNormal})
	c.onDelayStats(DelayStats{Usage: usageOver})
	if c.target != 600_000 {
		t.Fatalf("delayed feedback pinned recovery to an already lower loss rate: %d", c.target)
	}
	// Once delivery falls below the paced rate, continuing queue growth
	// must still trigger a decrease against that effective rate.
	now = now.Add(time.Second)
	c.onReceivedRate(50_000)
	c.onDelayStats(DelayStats{Usage: usageOver})
	if c.target != 85_000 {
		t.Fatalf("genuine congestion did not lower the actual paced rate: %d", c.target)
	}
}

func TestPacingGainDoesNotMaskGenuineQueueGrowth(t *testing.T) {
	c := newRateController(time.Now, 100_000, 5000, 50_000_000, func(DelayStats) {})
	// The pacer can send at 1.5x nominal bitrate. Receiving more than
	// nominal alone therefore does not disprove a growing network queue.
	c.onReceivedRate(130_000)
	c.onDelayStats(DelayStats{Usage: usageNormal})
	c.onDelayStats(DelayStats{Usage: usageOver})
	if c.target != 85_000 {
		t.Fatalf("pacing gain masked genuine overuse: %d", c.target)
	}
}

func TestLossLimitedOveruseCannotSuppressBackoffIndefinitely(t *testing.T) {
	now := time.Unix(100, 0)
	c := newRateController(func() time.Time { return now }, 600_000, 5000, 50_000_000, func(DelayStats) {})
	c.setPacedBitrate(100_000)
	c.updateRTT(500 * time.Millisecond)
	c.onReceivedRate(125_000)
	c.onDelayStats(DelayStats{Usage: usageNormal})
	c.onDelayStats(DelayStats{Usage: usageOver})
	// A 1.5x pacer can still overload this path while moderate loss holds
	// the loss estimate fixed. Fresh, persistent overuse must win.
	now = now.Add(500 * time.Millisecond)
	c.onDelayStats(DelayStats{Usage: usageOver})
	if c.target != 85_000 {
		t.Fatalf("held loss estimate masked continuing queue growth: %d", c.target)
	}
}

func TestSparseFeedbackCannotKeepLossRecoveryGuardAlive(t *testing.T) {
	now := time.Unix(100, 0)
	c := newRateController(func() time.Time { return now }, 600_000, 5000, 50_000_000, func(DelayStats) {})
	c.setPacedBitrate(100_000)
	rates := newRateCalculator(500 * time.Millisecond)
	rates.add([]cc.Acknowledgment{{Size: 1200, Arrival: now}, {Size: 1200, Arrival: now.Add(50 * time.Millisecond)}}, c.onReceivedRate)
	c.onDelayStats(DelayStats{Usage: usageNormal})
	for range 3 {
		now = now.Add(time.Second)
		rates.add([]cc.Acknowledgment{{Size: 1200, Arrival: now}}, c.onReceivedRate)
		c.onDelayStats(DelayStats{Usage: usageOver})
	}
	if c.target >= 100_000 {
		t.Fatalf("stale throughput suppressed real overuse: target=%d received=%d", c.target, c.latestReceivedRate)
	}
}

func TestRateControllerRun(t *testing.T) {
	cases := []struct {
		name           string
		initialBitrate int
		usage          []usage
		expected       []DelayStats
	}{
		{
			name:           "empty",
			initialBitrate: 100_000,
			usage:          []usage{},
			expected:       []DelayStats{},
		},
		{
			name:           "increasesForElapsedMockTime",
			initialBitrate: 100_000,
			usage:          []usage{usageNormal, usageNormal},
			expected: []DelayStats{{
				Usage:         usageNormal,
				State:         stateIncrease,
				TargetBitrate: 101_551, // 100000 * 1.08^0.2, bounded by the supplied clock.
				Estimate:      0,
				Threshold:     0,
			}},
		},
	}

	t0 := time.Time{}
	mockNoFn := func() time.Time {
		t0 = t0.Add(100 * time.Millisecond)

		return t0
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := make(chan DelayStats)
			dc := newRateController(mockNoFn, 100_000, 1_000, 50_000_000, func(ds DelayStats) {
				out <- ds
			})
			in := make(chan DelayStats)
			dc.onReceivedRate(100_000)
			dc.updateRTT(300 * time.Millisecond)
			go func() {
				defer close(out)
				for _, state := range tc.usage {
					dc.onDelayStats(DelayStats{
						Measurement:   0,
						Estimate:      0,
						Threshold:     0,
						Usage:         state,
						State:         0,
						TargetBitrate: 0,
					})
				}
				close(in)
			}()
			received := []DelayStats{}
			for ds := range out {
				received = append(received, ds)
			}
			if len(tc.expected) > 0 {
				assert.Equal(t, tc.expected[0], received[0])
			}
		})
	}
}
func TestCongestionBackoffIsBoundedAndWaitsForFeedback(t *testing.T) {
	now := time.Unix(100, 0)
	c := newRateController(func() time.Time { return now }, 1_500_000, 5000, 50_000_000, func(DelayStats) {})
	c.onReceivedRate(50_000)
	c.updateRTT(100 * time.Millisecond)
	c.onDelayStats(DelayStats{Usage: usageNormal})
	c.onDelayStats(DelayStats{Usage: usageOver})
	if c.target != 1_275_000 {
		t.Fatalf("one sparse jitter sample collapsed target to %d", c.target)
	}
	for range 30 {
		c.onDelayStats(DelayStats{Usage: usageOver})
	}
	if c.target != 1_275_000 {
		t.Fatal("same feedback batch repeatedly reduced target")
	}
	// Persistent congestion must still lower the rate below the content rate;
	// no fixed video bitrate floor may mask a constrained network.
	for range 30 {
		now = now.Add(200 * time.Millisecond)
		c.onDelayStats(DelayStats{Usage: usageOver})
	}
	if c.target >= 50_000 {
		t.Fatalf("persistent congestion did not back off: %d", c.target)
	}
}
