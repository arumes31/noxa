// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package gcc

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
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
	if got := c.decrease(); got != 28900 {
		t.Fatalf("genuine congestion decrease blocked: %d", got)
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
			name:           "increasesMultiplicativelyBy8000",
			initialBitrate: 100_000,
			usage:          []usage{usageNormal, usageNormal},
			expected: []DelayStats{{
				Usage:         usageNormal,
				State:         stateIncrease,
				TargetBitrate: 108_000,
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
