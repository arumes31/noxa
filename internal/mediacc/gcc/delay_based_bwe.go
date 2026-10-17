// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package gcc

import (
	"sync"
	"time"

	"github.com/pion/logging"
	"noxa/internal/mediacc/cc"
)

// DelayStats contains some internal statistics of the delay based congestion
// controller.
type DelayStats struct {
	// generation prevents an in-flight callback from undoing a probe transition.
	generation uint64
	// usageFloor is the throughput just delivered on a path free of delay
	// overuse for a full hold-off (0 otherwise). It bounds this increase from
	// below, and the loss controller adopts it while loss is low.
	usageFloor       int
	Measurement      time.Duration
	Estimate         time.Duration
	Threshold        time.Duration
	LastReceiveDelta time.Duration

	Usage         usage
	State         state
	TargetBitrate int
}

type now func() time.Time

type delayController struct {
	*rateController

	onUpdateCallback func(DelayStats)

	// mu serializes complete feedback reports. Each report is processed
	// synchronously by its caller so rate decisions are deterministic with
	// respect to the feedback that produced them (and to an injected clock).
	mu      sync.Mutex
	closed  bool
	process func([]cc.Acknowledgment)

	log logging.LeveledLogger
}

type delayControllerConfig struct {
	nowFn          now
	initialBitrate int
	minBitrate     int
	maxBitrate     int
}

func newDelayController(delayConfig delayControllerConfig, loggerFactory logging.LoggerFactory) *delayController {
	if delayConfig.nowFn == nil {
		delayConfig.nowFn = time.Now
	}
	delayController := &delayController{
		rateController:   nil,
		onUpdateCallback: nil,
		log:              loggerFactory.NewLogger("gcc_delay_controller"),
	}

	rateController := newRateController(
		delayConfig.nowFn, delayConfig.initialBitrate, delayConfig.minBitrate, delayConfig.maxBitrate,
		func(ds DelayStats) {
			delayController.log.Infof("delaystats: %v", ds)
			if delayController.onUpdateCallback != nil {
				delayController.onUpdateCallback(ds)
			}
		},
	)
	delayController.rateController = rateController
	var latest DelayStats
	var updated bool
	var slopeEstimator *slopeEstimator
	var arrivalGroupAccumulator *arrivalGroupAccumulator
	resetTiming := func() {
		detector := newOveruseDetector(newAdaptiveThreshold(withThresholdClock(delayConfig.nowFn)), 10*time.Millisecond, func(ds DelayStats) {
			latest, updated = ds, true
		})
		slopeEstimator = newSlopeEstimator(newTrendline(), detector.onDelayStats)
		arrivalGroupAccumulator = newArrivalGroupAccumulator()
	}
	resetTiming()

	rc := newRateCalculator(500 * time.Millisecond)

	var lastFeedback time.Time
	delayController.process = func(acks []cc.Acknowledgment) {
		received := false
		for _, ack := range acks {
			if !ack.Arrival.IsZero() {
				received = true

				break
			}
		}
		if !received {
			return
		}
		now := delayConfig.nowFn()
		// Match WebRTC's two-second stream timeout: keep the current
		// bitrate, but do not compare resumed packets with stale timing.
		if !lastFeedback.IsZero() && now.Sub(lastFeedback) > 2*time.Second {
			resetTiming()
		}
		lastFeedback = now
		// Rate and delay must describe the same completed feedback report.
		// A burst of acknowledgements supplies one rate-control decision,
		// not repeated reductions against the previous report's throughput.
		rc.add(acks, rateController.onReceivedRate)
		rateController.onDeliveryRatio(rc.deliveryRatio())
		updated = false
		arrivalGroupAccumulator.add(acks, slopeEstimator.onArrivalGroup)
		if updated {
			rateController.onDelayStats(latest)
		}
	}

	return delayController
}

func (d *delayController) onUpdate(f func(DelayStats)) {
	d.onUpdateCallback = f
}

func (d *delayController) updateDelayEstimate(acks []cc.Acknowledgment) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.closed {
		d.process(acks)
	}
}

func (d *delayController) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.closed = true

	return nil
}
