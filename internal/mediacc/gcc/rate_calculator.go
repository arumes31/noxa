// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package gcc

import (
	"time"

	"noxa/internal/mediacc/cc"
)

type rateCalculator struct {
	window      time.Duration
	history     []cc.Acknowledgment
	initialized bool
	sum         int
}

func newRateCalculator(window time.Duration) *rateCalculator {
	return &rateCalculator{
		window: window,
	}
}

func (c *rateCalculator) run(in <-chan []cc.Acknowledgment, onRateUpdate func(int)) {
	for acks := range in {
		c.add(acks, onRateUpdate)
	}
}

func (c *rateCalculator) add(acks []cc.Acknowledgment, onRateUpdate func(int)) {
	for _, next := range acks {
		if next.Arrival.IsZero() {
			continue
		}
		var previousArrival time.Time
		if len(c.history) > 0 {
			previousArrival = c.history[len(c.history)-1].Arrival
			// Match delay grouping: old feedback must not move the measured
			// window or its idle-gap anchor backwards.
			if next.Arrival.Before(previousArrival) {
				continue
			}
		}
		c.history = append(c.history, next)
		c.sum += next.Size
		if !c.initialized {
			c.initialized = true
			onRateUpdate(next.Size * 8)
			continue
		}
		del := 0
		for _, ack := range c.history {
			if !ack.Arrival.Before(next.Arrival.Add(-c.window)) {
				break
			}
			del++
			c.sum -= ack.Size
		}
		c.history = c.history[del:]
		dt := next.Arrival.Sub(c.history[0].Arrival)
		// If the entire window expired, the gap since its last packet is
		// still a measurable interval. Sparse traffic must not retain an old
		// high rate forever. Equal timestamps still provide no elapsed time.
		if len(c.history) == 1 {
			dt = next.Arrival.Sub(previousArrival)
		}
		if dt <= 0 {
			continue
		}
		onRateUpdate(int(float64(8*c.sum) / dt.Seconds()))
	}
}

// deliveryRatio compares how long the path took to deliver the packets in the
// current receive window with how long the sender took to send them (send
// span / receive span). About 1 means the path kept up with the sending rate;
// clearly below 1 means a queue grew somewhere. It returns 0 when the window
// is too short to judge.
func (c *rateCalculator) deliveryRatio() float64 {
	if len(c.history) < 2 {
		return 0
	}
	first, last := c.history[0], c.history[len(c.history)-1]
	minDeparture, maxDeparture := first.Departure, first.Departure
	for _, ack := range c.history[1:] {
		if ack.Departure.Before(minDeparture) {
			minDeparture = ack.Departure
		}
		if ack.Departure.After(maxDeparture) {
			maxDeparture = ack.Departure
		}
	}
	receive := last.Arrival.Sub(first.Arrival)
	send := maxDeparture.Sub(minDeparture)
	if receive < c.window/4 || send <= 0 {
		return 0
	}

	return send.Seconds() / receive.Seconds()
}
