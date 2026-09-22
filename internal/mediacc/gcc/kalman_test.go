// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package gcc

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestKalmanNoiseAdaptsToFiveMillisecondGroups(t *testing.T) {
	k := newKalman(initMeasurementUncertainty(1))
	k.updateEstimate(2 * time.Millisecond)
	want := 1.0004501913680218
	if math.Abs(k.measurementUncertainty-want) > 1e-12 {
		t.Fatalf("noise variance=%0.16f, want %0.16f; packet period must be measured in seconds", k.measurementUncertainty, want)
	}
}

func TestKalmanGroupPeriodWindow(t *testing.T) {
	k := newKalman(initMeasurementUncertainty(1))
	k.setPeriod(20 * time.Millisecond)
	k.updateEstimate(2 * time.Millisecond)
	want := 1 + 3*(1-math.Pow(1-chi, .6))
	if math.Abs(k.measurementUncertainty-want) > 1e-12 {
		t.Fatal("period scaling is inverted")
	}
	k.setPeriod(5 * time.Millisecond)
	if k.minPeriod() != 5*time.Millisecond {
		t.Fatal("minimum period not retained")
	}
	for range 60 {
		k.setPeriod(20 * time.Millisecond)
	}
	if k.minPeriod() != 20*time.Millisecond {
		t.Fatal("old minimum never expires")
	}
}

func TestKalman(t *testing.T) {
	cases := []struct {
		name         string
		opts         []kalmanOption
		measurements []time.Duration
		expected     []time.Duration
	}{
		{
			name:         "empty",
			opts:         []kalmanOption{},
			measurements: []time.Duration{},
			expected:     []time.Duration{},
		},
		{
			name: "kalmanfilter.netExample",
			opts: []kalmanOption{
				initEstimate(10 * time.Millisecond),
				initEstimateError(100),
				initProcessUncertainty(0.15),
				initMeasurementUncertainty(0.01),
			},
			measurements: []time.Duration{
				time.Duration(50.45 * float64(time.Millisecond)),
				time.Duration(50.967 * float64(time.Millisecond)),
				time.Duration(51.6 * float64(time.Millisecond)),
				time.Duration(52.106 * float64(time.Millisecond)),
				time.Duration(52.492 * float64(time.Millisecond)),
				time.Duration(52.819 * float64(time.Millisecond)),
				time.Duration(53.433 * float64(time.Millisecond)),
				time.Duration(54.007 * float64(time.Millisecond)),
				time.Duration(54.523 * float64(time.Millisecond)),
				time.Duration(54.99 * float64(time.Millisecond)),
			},
			expected: []time.Duration{
				time.Duration(50.449959 * float64(time.Millisecond)),
				time.Duration(50.936547 * float64(time.Millisecond)),
				time.Duration(51.560411 * float64(time.Millisecond)),
				time.Duration(52.07324 * float64(time.Millisecond)),
				time.Duration(52.466566 * float64(time.Millisecond)),
				time.Duration(52.797787 * float64(time.Millisecond)),
				time.Duration(53.395303 * float64(time.Millisecond)),
				time.Duration(53.970236 * float64(time.Millisecond)),
				time.Duration(54.489652 * float64(time.Millisecond)),
				time.Duration(54.960137 * float64(time.Millisecond)),
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			k := newKalman(append(tc.opts, setDisableMeasurementUncertaintyUpdates(true))...)
			estimates := []time.Duration{}
			for _, m := range tc.measurements {
				estimates = append(estimates, k.updateEstimate(m))
			}
			assert.Equal(t, tc.expected, estimates, "%v != %v", tc.expected, estimates)
		})
	}
}
