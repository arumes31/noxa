// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package gcc

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

type staticThreshold time.Duration

func (t staticThreshold) compare(estimate, _ time.Duration) (usage, time.Duration, time.Duration) {
	if estimate > time.Duration(t) {
		return usageOver, estimate, time.Duration(t)
	}
	if estimate < -time.Duration(t) {
		return usageUnder, estimate, time.Duration(t)
	}

	return usageNormal, estimate, time.Duration(t)
}

func TestOveruseDetectorUsesWireTimeForBatchedFeedback(t *testing.T) {
	for _, tc := range []struct {
		name        string
		measurement time.Duration
		want        usage
	}{
		{"constant delay", 0, usageOver},
		{"growing delay with short send interval", 18 * time.Millisecond, usageNormal},
		{"shrinking delay with long send interval", -10 * time.Millisecond, usageOver},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got []usage
			detector := newOveruseDetector(staticThreshold(time.Millisecond), 10*time.Millisecond,
				func(ds DelayStats) { got = append(got, ds.Usage) })
			// A single feedback packet delivers both groups together; their
			// departure spacing, not the callback spacing, determines persistence.
			for _, estimate := range []time.Duration{2 * time.Millisecond, 3 * time.Millisecond} {
				detector.onDelayStats(DelayStats{Estimate: estimate, LastReceiveDelta: 20 * time.Millisecond, Measurement: tc.measurement})
			}
			assert.Equal(t, []usage{usageNormal, tc.want}, got)
		})
	}
}

func TestOveruseDetectorComparesUnscaledDelayTrend(t *testing.T) {
	for _, tc := range []struct {
		name      string
		estimates []time.Duration
		want      usage
	}{
		{"declining startup delay", []time.Duration{10 * time.Millisecond, 9 * time.Millisecond, 8 * time.Millisecond}, usageNormal},
		{"persistent constant delay", []time.Duration{10 * time.Millisecond, 10 * time.Millisecond, 10 * time.Millisecond}, usageOver},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got usage
			detector := newOveruseDetector(newAdaptiveThreshold(), 10*time.Millisecond,
				func(ds DelayStats) { got = ds.Usage })
			for _, estimate := range tc.estimates {
				detector.onDelayStats(DelayStats{Estimate: estimate, LastReceiveDelta: 20 * time.Millisecond})
			}
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestOverusePersistsAcrossFeedbackBatchPhases(t *testing.T) {
	for _, batchSize := range []int{7, 10, 13} {
		for phase := range batchSize {
			filter := newTrendline()
			var got usage
			detector := newOveruseDetector(newAdaptiveThreshold(), 10*time.Millisecond, func(ds DelayStats) { got = ds.Usage })
			for i := range 200 {
				filter.setPeriod(10 * time.Millisecond)
				delta := time.Duration(4+i%2) * time.Millisecond
				detector.onDelayStats(DelayStats{Measurement: delta, Estimate: filter.updateEstimate(delta), LastReceiveDelta: 10*time.Millisecond + delta})
				if i > 100 && (i+phase)%batchSize == 0 && got != usageOver {
					t.Fatalf("batch size %d phase %d hid continuously growing queue at sample %d", batchSize, phase, i)
				}
			}
		}
	}
}

func TestOveruseDetectorWithoutDelay(t *testing.T) {
	cases := []struct {
		name      string
		estimates []DelayStats
		expected  []usage
		thresh    threshold
		delay     time.Duration
	}{
		{
			name:      "noEstimateNoUsage",
			estimates: []DelayStats{},
			expected:  []usage{},
			thresh:    staticThreshold(time.Millisecond),
			delay:     0,
		},
		{
			name: "overuse",
			estimates: []DelayStats{
				{},
				{Estimate: 2 * time.Millisecond},
				{Estimate: 3 * time.Millisecond},
			},
			expected: []usage{usageNormal, usageNormal, usageOver},
			thresh:   staticThreshold(time.Millisecond),
			delay:    13 * time.Millisecond,
		},
		{
			name:      "normaluse",
			estimates: []DelayStats{{Estimate: 0}},
			expected:  []usage{usageNormal},
			thresh:    staticThreshold(time.Millisecond),
			delay:     0,
		},
		{
			name:      "underuse",
			estimates: []DelayStats{{Estimate: -2 * time.Millisecond}},
			expected:  []usage{usageUnder},
			thresh:    staticThreshold(time.Millisecond),
			delay:     0,
		},
		{
			name: "noOverUseBeforeDelay",
			estimates: []DelayStats{
				{},
				{Estimate: 3 * time.Millisecond},
				{Estimate: 5 * time.Millisecond},
			},
			expected: []usage{usageNormal, usageNormal, usageOver},
			thresh:   staticThreshold(1 * time.Millisecond),
			delay:    10 * time.Millisecond,
		},
		{
			name: "overusePersistsUntilEstimateReentersThreshold",
			estimates: []DelayStats{
				{},
				{Estimate: 4 * time.Millisecond},
				{Estimate: 5 * time.Millisecond},
				{Estimate: 3 * time.Millisecond},
				{Estimate: 0},
			},
			expected: []usage{usageNormal, usageNormal, usageOver, usageOver, usageNormal},
			thresh:   staticThreshold(1 * time.Millisecond),
			delay:    0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := make(chan DelayStats)
			dsw := func(ds DelayStats) {
				out <- ds
			}
			od := newOveruseDetector(tc.thresh, tc.delay, dsw)
			go func() {
				defer close(out)
				for _, e := range tc.estimates {
					e.LastReceiveDelta = tc.delay
					od.onDelayStats(e)
				}
			}()
			received := []usage{}
			for s := range out {
				received = append(received, s.Usage)
			}
			assert.Equal(t, tc.expected, received, "%v != %v", tc.expected, received)
		})
	}
}
