package gcc

import (
	"github.com/pion/logging"
	"noxa/internal/mediacc/cc"
	"sync/atomic"
	"testing"
	"time"
)

func TestDelayFeedbackUsesCurrentBatchOnce(t *testing.T) {
	base := time.Unix(100, 0)
	controller := newDelayController(delayControllerConfig{nowFn: func() time.Time { return base }, initialBitrate: 1_500_000, minBitrate: 5000, maxBitrate: 50_000_000}, logging.NewDefaultLoggerFactory())
	var updates []DelayStats
	var decisionRate int
	controller.onUpdate(func(ds DelayStats) {
		updates = append(updates, ds)
		decisionRate = controller.latestReceivedRate
	})
	acks := make([]cc.Acknowledgment, 60)
	for i := range acks {
		acks[i] = cc.Acknowledgment{Size: 1200, Departure: base.Add(time.Duration(i) * 10 * time.Millisecond), Arrival: base.Add(time.Second + time.Duration(i)*15*time.Millisecond)}
	}
	controller.updateDelayEstimate(acks[:3])
	controller.updateDelayEstimate(acks[3:])
	if err := controller.Close(); err != nil {
		t.Fatal(err)
	}
	if len(updates) != 1 {
		t.Fatalf("two reports (initialization then congestion) produced %d bitrate decisions", len(updates))
	}
	// The receive window contains 34 packets over 495ms. Congestion must use
	// that completed report, not the previous report or an uninitialized rate.
	received := float64(34*1200*8) / .495
	if decisionRate != int(received) {
		t.Fatalf("decision used throughput=%d want current batch %d", decisionRate, int(received))
	}
}

func TestDelayFeedbackResetsTimingAfterTimeout(t *testing.T) {
	base := time.Unix(100, 0)
	var elapsed atomic.Int64
	controller := newDelayController(delayControllerConfig{nowFn: func() time.Time { return base.Add(time.Duration(elapsed.Load())) }, initialBitrate: 1_500_000, minBitrate: 5000, maxBitrate: 50_000_000}, logging.NewDefaultLoggerFactory())
	var updates []DelayStats
	controller.onUpdate(func(ds DelayStats) { updates = append(updates, ds) })
	batch := func(first, count int, delay time.Duration) []cc.Acknowledgment {
		acks := make([]cc.Acknowledgment, count)
		for i := range acks {
			sent := base.Add(time.Duration(first+i) * 20 * time.Millisecond)
			acks[i] = cc.Acknowledgment{Size: 1200, Departure: sent, Arrival: sent.Add(delay)}
		}
		return acks
	}
	controller.updateDelayEstimate(batch(0, 60, 50*time.Millisecond))
	controller.updateDelayEstimate(nil) // Wait until the preceding report is processed.
	elapsed.Store(int64(5 * time.Second))
	controller.updateDelayEstimate([]cc.Acknowledgment{{Departure: base, Size: 1200}})
	controller.updateDelayEstimate(nil) // Loss-only feedback must not refresh the timeout.
	elapsed.Store(int64(6 * time.Second))
	controller.updateDelayEstimate(batch(60, 5, 5*time.Second))
	controller.updateDelayEstimate(nil)
	elapsed.Store(int64(6100 * time.Millisecond))
	congestion := batch(65, 60, 5*time.Second)
	for i := range congestion {
		congestion[i].Arrival = congestion[i].Arrival.Add(time.Duration(i) * 5 * time.Millisecond)
	}
	controller.updateDelayEstimate(congestion)
	if err := controller.Close(); err != nil {
		t.Fatal(err)
	}
	if len(updates) != 2 {
		t.Fatalf("resumed and congested reports produced %d decisions, want 2", len(updates))
	}
	if ds := updates[0]; ds.Estimate != 0 || ds.Usage != usageNormal || ds.TargetBitrate < 1_500_000 {
		t.Fatalf("timed-out history was interpreted as queue growth: %+v", ds)
	}
	if ds := updates[1]; ds.Usage != usageOver || ds.TargetBitrate >= 1_500_000 {
		t.Fatalf("timeout reset suppressed renewed real congestion: %+v", ds)
	}
}
