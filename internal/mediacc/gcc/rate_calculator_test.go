// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package gcc

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"noxa/internal/mediacc/cc"
)

func TestRateCalculator(t *testing.T) {
	t0 := time.Now()
	cases := []struct {
		name     string
		acks     []cc.Acknowledgment
		expected []int
	}{
		{
			name: "sameArrivalRetainsEstimateUntilTimeAdvances",
			acks: []cc.Acknowledgment{
				{Size: 125, Arrival: t0},
				{Size: 125, Arrival: t0},
				{Size: 125, Arrival: t0.Add(100 * time.Millisecond)},
			},
			expected: []int{1000, 30_000},
		},
		{
			name: "idleWindowUsesTheMeasuredPacketGap",
			acks: []cc.Acknowledgment{
				{Size: 125, Arrival: t0},
				{Size: 125, Arrival: t0.Add(time.Second)},
				{Size: 125, Arrival: t0.Add(1100 * time.Millisecond)},
			},
			expected: []int{1000, 1000, 20_000},
		},
		{
			name: "reorderedArrivalCannotMoveIdleGapBackward",
			acks: []cc.Acknowledgment{
				{Size: 1200, Arrival: t0},
				{Size: 1200, Arrival: t0.Add(-time.Second)},
				{Size: 1200, Arrival: t0.Add(600 * time.Millisecond)},
			},
			expected: []int{9600, 16_000},
		},
		{
			name:     "emptyCreatesNoRate",
			acks:     []cc.Acknowledgment{},
			expected: []int{},
		},
		{
			name: "ignoresZeroArrivalTimes",
			acks: []cc.Acknowledgment{{
				SequenceNumber: 0,
				Size:           0,
				Departure:      time.Time{},
				Arrival:        time.Time{},
			}},
			expected: []int{},
		},
		{
			name: "singleAckCreatesRate",
			acks: []cc.Acknowledgment{{
				SequenceNumber: 0,
				Size:           1000,
				Departure:      time.Time{},
				Arrival:        t0,
			}},
			expected: []int{8000},
		},
		{
			name: "twoAcksCalculateCorrectRates",
			acks: []cc.Acknowledgment{{
				SequenceNumber: 0,
				Size:           125,
				Departure:      time.Time{},
				Arrival:        t0,
			}, {
				SequenceNumber: 0,
				Size:           125,
				Departure:      time.Time{},
				Arrival:        t0.Add(100 * time.Millisecond),
			}},
			expected: []int{1000, 20_000},
		},
		{
			name: "steadyACKsCalculateCorrectRates",
			acks: getACKStream(10, 1200, 100*time.Millisecond),
			expected: []int{
				9_600,
				192_000,
				144_000,
				128_000,
				120_000,
				115_200,
				115_200,
				115_200,
				115_200,
				115_200,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rc := newRateCalculator(500 * time.Millisecond)
			in := make(chan []cc.Acknowledgment)
			out := make(chan int)
			onRateUpdate := func(rate int) {
				out <- rate
			}
			go func() {
				defer close(out)
				rc.run(in, onRateUpdate)
			}()
			go func() {
				in <- tc.acks
				close(in)
			}()

			received := []int{}
			for r := range out {
				received = append(received, r)
			}
			assert.Equal(t, tc.expected, received)
		})
	}
}

func getACKStream(length int, size int, interval time.Duration) []cc.Acknowledgment {
	res := []cc.Acknowledgment{}
	t0 := time.Now()
	for range length {
		res = append(res, cc.Acknowledgment{
			Size:    size,
			Arrival: t0,
		})
		t0 = t0.Add(interval)
	}

	return res
}
