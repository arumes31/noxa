// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package gcc

import (
	"time"
)

type threshold interface {
	compare(estimate time.Duration, delta time.Duration) (usage, time.Duration, time.Duration)
}

type overuseDetector struct {
	threshold   threshold
	overuseTime time.Duration

	dsWriter func(DelayStats)

	lastEstimate       time.Duration
	increasingDuration time.Duration
	increasingCounter  int
	hypothesis         usage
}

func newOveruseDetector(thresh threshold, overuseTime time.Duration, dsw func(DelayStats)) *overuseDetector {
	return &overuseDetector{
		threshold:          thresh,
		overuseTime:        overuseTime,
		dsWriter:           dsw,
		lastEstimate:       0,
		increasingDuration: -1,
		increasingCounter:  0,
		hypothesis:         usageNormal,
	}
}

func (d *overuseDetector) onDelayStats(ds DelayStats) {
	// Measurement is arrival delta minus departure delta. Use the latter
	// for persistence so batching or scheduling feedback cannot change it.
	delta := max(ds.LastReceiveDelta-ds.Measurement, 0)

	thresholdUse, estimate, currentThreshold := d.threshold.compare(ds.Estimate, ds.LastReceiveDelta)

	use := d.hypothesis
	if thresholdUse == usageOver { //nolint:nestif
		if d.increasingDuration < 0 {
			d.increasingDuration = delta / 2
		} else {
			d.increasingDuration += delta
		}

		d.increasingCounter++

		if (d.overuseTime == 0 && d.increasingCounter > 1) ||
			(d.increasingDuration > d.overuseTime && d.increasingCounter > 1) {
			// Threshold scaling grows during startup even when the underlying
			// delay is falling. Compare the raw offsets to detect its trend.
			if ds.Estimate >= d.lastEstimate {
				use = usageOver
				d.increasingDuration = 0
				d.increasingCounter = 0
			}
		}
	}

	if thresholdUse == usageUnder {
		d.increasingCounter = 0
		d.increasingDuration = -1
		use = usageUnder
	}

	if thresholdUse == usageNormal {
		d.increasingDuration = -1
		d.increasingCounter = 0
		use = usageNormal
	}

	d.lastEstimate = ds.Estimate
	d.hypothesis = use

	d.dsWriter(DelayStats{
		Measurement:      ds.Measurement,
		Estimate:         estimate,
		Threshold:        currentThreshold,
		LastReceiveDelta: ds.LastReceiveDelta,
		Usage:            use,
		State:            0,
		TargetBitrate:    0,
	})
}
