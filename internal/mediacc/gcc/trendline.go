package gcc

import "time"

// trendline estimates queue growth against arrival time, using WebRTC's
// 20-group linear fit, 0.9 smoothing, and threshold gain of 4:
// https://webrtc.googlesource.com/src/+/8c371f2a9baf8fef9bf3c327a93a709bb8c1e000/modules/congestion_controller/goog_cc/trendline_estimator.cc
// Samples are relative durations, keeping the fit independent of clock epochs.
type trendline struct {
	points                         [20]struct{ arrival, delay, raw float64 }
	count, next                    int
	period                         time.Duration
	arrival, accumulated, smoothed float64
	estimate                       time.Duration
}

func newTrendline() *trendline { return &trendline{} }

func (t *trendline) setPeriod(period time.Duration) { t.period = period }

func (t *trendline) updateEstimate(measurement time.Duration) time.Duration {
	t.arrival += float64(t.period+measurement) / float64(time.Millisecond)
	t.accumulated += float64(measurement) / float64(time.Millisecond)
	t.smoothed = .9*t.smoothed + .1*t.accumulated
	t.points[t.next].arrival = t.arrival
	t.points[t.next].delay = t.smoothed
	t.points[t.next].raw = t.accumulated
	t.next = (t.next + 1) % len(t.points)
	t.count = min(t.count+1, len(t.points))
	if t.count < len(t.points) {
		return 0
	}
	var x, y float64
	for _, p := range t.points {
		x += p.arrival
		y += p.delay
	}
	x /= float64(len(t.points))
	y /= float64(len(t.points))
	var covariance, variance float64
	for _, p := range t.points {
		dx := p.arrival - x
		covariance += dx * (p.delay - y)
		variance += dx * dx
	}
	if variance > 0 {
		slope := covariance / variance
		// Bound a positive smoothed trend by raw delay minima at both ends
		// of the window (WebRTC's slope cap). A drained jitter spike must
		// not keep cutting bitrate while the smoother catches up.
		early, late := t.points[t.next], t.points[(t.next+len(t.points)-4)%len(t.points)]
		for i := 1; i < 4; i++ {
			if p := t.points[(t.next+i)%len(t.points)]; p.raw < early.raw {
				early = p
			}
			if p := t.points[(t.next+len(t.points)-4+i)%len(t.points)]; p.raw < late.raw {
				late = p
			}
		}
		if slope > 0 && late.arrival-early.arrival >= 1 {
			slope = min(slope, (late.raw-early.raw)/(late.arrival-early.arrival))
		}
		// The detector's historical threshold uses milliseconds; the trend
		// itself is dimensionless, scaled by the reference filter's gain.
		t.estimate = time.Duration(4 * slope * float64(time.Millisecond))
	}
	return t.estimate
}
