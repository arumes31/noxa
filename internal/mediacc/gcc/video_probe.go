package gcc

import (
	"time"

	"noxa/internal/mediacc/cc"
)

const videoProbeLifetime = time.Second

type videoProbeAckKey struct {
	departure int64
	ssrc      uint32
	sequence  uint16
}

// Protected by SendSideBWE.lock. At most one one-second probe and one bounded
// deduplication set are retained for the estimator's lifetime.
type videoStartupProbe struct {
	started                       time.Time
	base, rate                    int
	timer                         *time.Timer
	seen                          map[videoProbeAckKey]struct{}
	bytes, firstSize              int
	firstDeparture, lastDeparture time.Time
	firstArrival, lastArrival     time.Time
	lastFeedback                  time.Time
}

// StartVideoProbe temporarily paces a cold video recipient at measured source
// demand. The caller must have an authorized media packet and negotiated TWCC.
// Unknown demand consumes the single attempt without increasing bandwidth.
func (e *SendSideBWE) StartVideoProbe(demandBPS int) bool {
	e.closeLock.RLock()
	defer e.closeLock.RUnlock()
	if e.isClosed() {
		return false
	}
	e.lock.Lock()
	defer e.lock.Unlock()
	if e.videoProbeUsed {
		return false
	}
	e.videoProbeUsed = true
	rate := min(demandBPS, e.maxBitrate)
	if rate <= e.latestBitrate {
		return false
	}
	probe := &videoStartupProbe{started: time.Now(), base: e.latestBitrate, rate: rate, seen: make(map[videoProbeAckKey]struct{})}
	e.videoProbe = probe
	e.seedVideoProbeLocked(rate)
	probe.timer = time.AfterFunc(videoProbeLifetime, func() {
		e.closeLock.RLock()
		defer e.closeLock.RUnlock()
		if !e.isClosed() {
			e.finishVideoProbe(time.Now())
		}
	})
	return true
}

// Called after loss estimation, before handing a complete report to delay GCC.
// ACK clocks are independent: only spans are compared, never one-way delays.
func (e *SendSideBWE) observeVideoProbe(acks []cc.Acknowledgment, now time.Time) {
	e.lock.Lock()
	defer e.lock.Unlock()
	probe := e.videoProbe
	if probe == nil {
		return
	}
	e.lossController.lock.Lock()
	loss := e.lossController.averageLoss
	e.lossController.lock.Unlock()
	if loss > decreaseLossThreshold {
		e.endVideoProbeLocked(min(probe.base, e.latestBitrate))
		return
	}
	if !now.Before(probe.started.Add(videoProbeLifetime)) {
		e.endVideoProbeLocked(probe.confirmedRate(now))
		return
	}
	for _, ack := range acks {
		if ack.Arrival.IsZero() || ack.Departure.Before(probe.started) || ack.Departure.After(now) || ack.Size <= 0 || ack.Size > 65535 {
			continue
		}
		key := videoProbeAckKey{departure: ack.Departure.UnixNano(), ssrc: ack.SSRC, sequence: ack.SequenceNumber}
		if _, duplicate := probe.seen[key]; duplicate || len(probe.seen) == cc.FeedbackHistorySize {
			continue
		}
		probe.seen[key] = struct{}{}
		probe.bytes += ack.Size
		probe.lastFeedback = now
		if probe.firstDeparture.IsZero() || ack.Departure.Before(probe.firstDeparture) {
			probe.firstDeparture, probe.firstSize = ack.Departure, ack.Size
		}
		if ack.Departure.After(probe.lastDeparture) {
			probe.lastDeparture = ack.Departure
		}
		if probe.firstArrival.IsZero() || ack.Arrival.Before(probe.firstArrival) {
			probe.firstArrival = ack.Arrival
		}
		if ack.Arrival.After(probe.lastArrival) {
			probe.lastArrival = ack.Arrival
		}
	}
}

func (p *videoStartupProbe) confirmedRate(now time.Time) int {
	sendSpan, receiveSpan := p.lastDeparture.Sub(p.firstDeparture), p.lastArrival.Sub(p.firstArrival)
	if sendSpan < 200*time.Millisecond || receiveSpan < 200*time.Millisecond ||
		now.Sub(p.lastFeedback) > 500*time.Millisecond || now.Sub(p.lastDeparture) > 500*time.Millisecond {
		return p.base
	}
	// Exclude the first packet's bytes because it begins, rather than occupies,
	// the measurement window. ACK compression cannot shorten the send window.
	rate := float64(p.bytes-p.firstSize) * 8 / max(sendSpan, receiveSpan).Seconds()
	return max(p.base, int(min(float64(p.rate), rate)))
}

func (e *SendSideBWE) finishVideoProbe(now time.Time) {
	e.lock.Lock()
	defer e.lock.Unlock()
	if probe := e.videoProbe; probe != nil && !now.Before(probe.started.Add(videoProbeLifetime)) {
		e.endVideoProbeLocked(probe.confirmedRate(now))
	}
}

func (e *SendSideBWE) endVideoProbeLocked(rate int) {
	if probe := e.videoProbe; probe != nil {
		probe.timer.Stop()
		e.videoProbe = nil
	}
	e.seedVideoProbeLocked(rate)
}

func (e *SendSideBWE) seedVideoProbeLocked(rate int) {
	rate = clampInt(rate, e.minBitrate, e.maxBitrate)
	e.controllerGeneration = e.delayController.seedVideoProbe(rate)
	e.lossController.lock.Lock()
	e.lossController.bitrate = rate
	e.lossController.lock.Unlock()
	e.delayController.setPacedBitrate(rate)
	if rate != e.latestBitrate {
		e.latestBitrate = rate
		e.pacer.SetTargetBitrate(rate)
		if e.onTargetBitrateChange != nil {
			go e.onTargetBitrateChange(rate)
		}
	}
}
