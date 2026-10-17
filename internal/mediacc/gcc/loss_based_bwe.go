// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package gcc

import (
	"math"
	"sync"
	"time"

	"github.com/pion/logging"
	"noxa/internal/mediacc/cc"
)

const (
	// constants from
	// https://datatracker.ietf.org/doc/html/draft-ietf-rmcat-gcc-02#section-6

	increaseLossThreshold = 0.02
	increaseTimeThreshold = 200 * time.Millisecond
	increaseFactor        = 1.05

	decreaseLossThreshold = 0.1
	decreaseTimeThreshold = 200 * time.Millisecond
)

// LossStats contains internal statistics of the loss based controller.
type LossStats struct {
	TargetBitrate int
	AverageLoss   float64
}

type lossBasedBandwidthEstimator struct {
	lock           sync.Mutex
	maxBitrate     int
	minBitrate     int
	bitrate        int
	averageLoss    float64
	lastLossUpdate time.Time
	lastIncrease   time.Time
	lastDecrease   time.Time
	now            now
	log            logging.LeveledLogger
}

func newLossBasedBWE(initialBitrate int, nowFn now, loggerFactory logging.LoggerFactory) *lossBasedBandwidthEstimator {
	if nowFn == nil {
		nowFn = time.Now
	}
	return &lossBasedBandwidthEstimator{
		now:            nowFn,
		lock:           sync.Mutex{},
		maxBitrate:     100_000_000, // 100 mbit
		minBitrate:     100_000,     // 100 kbit
		bitrate:        initialBitrate,
		averageLoss:    0,
		lastLossUpdate: time.Time{},
		lastIncrease:   time.Time{},
		lastDecrease:   time.Time{},
		log:            loggerFactory.NewLogger("gcc_loss_controller"),
	}
}

func (e *lossBasedBandwidthEstimator) getEstimate(wantedRate int) LossStats {
	e.lock.Lock()
	defer e.lock.Unlock()

	if e.bitrate <= 0 {
		e.bitrate = clampInt(wantedRate, e.minBitrate, e.maxBitrate)
	}
	e.bitrate = min(wantedRate, e.bitrate)

	return LossStats{
		TargetBitrate: e.bitrate,
		AverageLoss:   e.averageLoss,
	}
}

// raiseToDeliveredRate lets throughput that was just delivered without delay
// overuse bound the loss estimate from below while loss is low. Its 5% per
// 200 ms ramp otherwise lags far behind after a transient congestion episode.
func (e *lossBasedBandwidthEstimator) raiseToDeliveredRate(rate int) {
	e.lock.Lock()
	defer e.lock.Unlock()
	if e.averageLoss < increaseLossThreshold && rate > e.bitrate {
		e.bitrate = clampInt(rate, e.minBitrate, e.maxBitrate)
	}
}

func (e *lossBasedBandwidthEstimator) updateLossEstimate(results []cc.Acknowledgment) {
	if len(results) == 0 {
		return
	}

	packetsLost := 0
	for _, p := range results {
		if p.Arrival.IsZero() {
			packetsLost++
		}
	}

	e.lock.Lock()
	defer e.lock.Unlock()

	lossRatio := float64(packetsLost) / float64(len(results))
	now := e.now()
	e.averageLoss = e.average(now.Sub(e.lastLossUpdate), e.averageLoss, lossRatio)
	e.lastLossUpdate = now

	increaseLoss := math.Max(e.averageLoss, lossRatio)
	decreaseLoss := math.Min(e.averageLoss, lossRatio)

	if increaseLoss < increaseLossThreshold && now.Sub(e.lastIncrease) > increaseTimeThreshold {
		e.log.Infof(
			"loss controller increasing; averageLoss: %v, decreaseLoss: %v, increaseLoss: %v",
			e.averageLoss, decreaseLoss, increaseLoss,
		)
		e.lastIncrease = now
		e.bitrate = clampInt(int(increaseFactor*float64(e.bitrate)), e.minBitrate, e.maxBitrate)
	} else if decreaseLoss > decreaseLossThreshold && now.Sub(e.lastDecrease) > decreaseTimeThreshold {
		e.log.Infof(
			"loss controller decreasing; averageLoss: %v, decreaseLoss: %v, increaseLoss: %v",
			e.averageLoss, decreaseLoss, increaseLoss,
		)
		e.lastDecrease = now
		e.bitrate = clampInt(int(float64(e.bitrate)*(1-0.5*decreaseLoss)), e.minBitrate, e.maxBitrate)
	}
}

func (e *lossBasedBandwidthEstimator) average(delta time.Duration, prev, sample float64) float64 {
	return sample + math.Exp(-float64(delta.Milliseconds())/200.0)*(prev-sample)
}
