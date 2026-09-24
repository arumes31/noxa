package gcc

import (
	"math"
	"testing"
	"time"
)

func TestTrendlineSeparatesJitterFromQueueGrowth(t *testing.T) {
	for _, growth := range []time.Duration{0, 5 * time.Millisecond, -5 * time.Millisecond} {
		e := newTrendline()
		var estimate time.Duration
		for i := range 200 {
			e.setPeriod(20 * time.Millisecond)
			jitter := 8 * time.Millisecond
			if i%6 >= 3 {
				jitter = -jitter
			}
			estimate = e.updateEstimate(growth + jitter)
		}
		// A 5ms queue change per 20ms sent has slope 5/25 (or -5/15)
		// against arrival time. Bounded jitter must converge near zero.
		want := 4 * float64(growth) / float64(20*time.Millisecond+growth)
		got := float64(estimate) / float64(time.Millisecond)
		// Endpoint minima deliberately bound positive trends below the mean;
		// allow the bounded jitter's contribution across the fit window.
		if math.Abs(got-want) > .1 {
			t.Fatalf("growth=%v trend=%v want %v", growth, got, want)
		}
	}
}

func TestTrendlineConstantQueueGrowth(t *testing.T) {
	e := newTrendline()
	e.setPeriod(20 * time.Millisecond)
	var estimate time.Duration
	for range 200 {
		estimate = e.updateEstimate(5 * time.Millisecond)
	}
	if got := float64(estimate) / float64(time.Millisecond); math.Abs(got-.8) > .001 {
		t.Fatalf("constant queue slope=%v want .8", got)
	}
}

func TestTrendlineNeedsWindowAndHandlesEqualArrivalTimes(t *testing.T) {
	e := newTrendline()
	for range 19 {
		e.setPeriod(20 * time.Millisecond)
		if got := e.updateEstimate(5 * time.Millisecond); got != 0 {
			t.Fatal("trend used an incomplete window")
		}
	}
	e = newTrendline()
	for range 100 {
		e.setPeriod(time.Millisecond)
		if got := e.updateEstimate(-time.Millisecond); got != 0 {
			t.Fatal("equal arrival timestamps produced a slope")
		}
	}
}

func TestTrendlineDoesNotKeepGrowingAfterDelaySpikeDrains(t *testing.T) {
	e := newTrendline()
	e.setPeriod(20 * time.Millisecond)
	for range 30 {
		e.updateEstimate(0)
	}
	e.updateEstimate(100 * time.Millisecond)
	var estimate time.Duration
	for range 5 {
		estimate = e.updateEstimate(-20 * time.Millisecond)
	}
	if estimate > 0 {
		t.Fatalf("smoothed trend kept reporting queue growth after raw delay returned to baseline: %v", estimate)
	}
}
