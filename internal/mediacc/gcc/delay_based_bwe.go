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
	ackPipe chan<- []cc.Acknowledgment

	*arrivalGroupAccumulator
	*rateController

	onUpdateCallback func(DelayStats)

	wg sync.WaitGroup

	log logging.LeveledLogger
}

type delayControllerConfig struct {
	nowFn          now
	initialBitrate int
	minBitrate     int
	maxBitrate     int
}

func newDelayController(delayConfig delayControllerConfig, loggerFactory logging.LoggerFactory) *delayController {
	ackPipe := make(chan []cc.Acknowledgment)

	delayController := &delayController{
		ackPipe:                 ackPipe,
		arrivalGroupAccumulator: nil,
		rateController:          nil,
		onUpdateCallback:        nil,
		wg:                      sync.WaitGroup{},
		log:                     loggerFactory.NewLogger("gcc_delay_controller"),
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
		detector := newOveruseDetector(newAdaptiveThreshold(), 10*time.Millisecond, func(ds DelayStats) {
			latest, updated = ds, true
		})
		slopeEstimator = newSlopeEstimator(newTrendline(), detector.onDelayStats)
		arrivalGroupAccumulator = newArrivalGroupAccumulator()
	}
	resetTiming()

	rc := newRateCalculator(500 * time.Millisecond)

	delayController.wg.Add(1)
	go func() {
		defer delayController.wg.Done()
		var lastFeedback time.Time
		for acks := range ackPipe {
			received := false
			for _, ack := range acks {
				if !ack.Arrival.IsZero() {
					received = true
					break
				}
			}
			if !received {
				continue
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
			updated = false
			arrivalGroupAccumulator.add(acks, slopeEstimator.onArrivalGroup)
			if updated {
				rateController.onDelayStats(latest)
			}
		}
	}()

	return delayController
}

func (d *delayController) onUpdate(f func(DelayStats)) {
	d.onUpdateCallback = f
}

func (d *delayController) updateDelayEstimate(acks []cc.Acknowledgment) {
	d.ackPipe <- acks
}

func (d *delayController) Close() error {
	defer d.wg.Wait()

	close(d.ackPipe)

	return nil
}
