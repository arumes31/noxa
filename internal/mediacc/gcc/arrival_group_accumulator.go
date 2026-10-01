// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package gcc

import (
	"time"

	"noxa/internal/mediacc/cc"
)

type arrivalGroupAccumulator struct {
	initialized                      bool
	group                            arrivalGroup
	interDepartureThreshold          time.Duration
	interArrivalThreshold            time.Duration
	interGroupDelayVariationTreshold time.Duration
}

func newArrivalGroupAccumulator() *arrivalGroupAccumulator {
	return &arrivalGroupAccumulator{
		interDepartureThreshold:          5 * time.Millisecond,
		interArrivalThreshold:            5 * time.Millisecond,
		interGroupDelayVariationTreshold: 0,
	}
}

func (a *arrivalGroupAccumulator) run(in <-chan []cc.Acknowledgment, agWriter func(arrivalGroup)) {
	for acks := range in {
		a.add(acks, agWriter)
	}
}

func (a *arrivalGroupAccumulator) add(acks []cc.Acknowledgment, agWriter func(arrivalGroup)) {
	for _, next := range acks {
		if next.Arrival.IsZero() {
			continue // Loss belongs to the loss controller, not delay timing.
		}
		if !a.initialized {
			a.group = newArrivalGroup(next)
			a.initialized = true

			continue
		}
		if next.Arrival.Before(a.group.arrival) {
			// ignore out of order arrivals
			continue
		}
		if !next.Departure.Before(a.group.packets[0].Departure) {
			// A sequence of packets which are sent within a burst_time interval
			// constitute a group.
			if interDepartureTimePkt(a.group, next) <= a.interDepartureThreshold {
				a.group.add(next)

				continue
			}

			// A Packet which has an inter-arrival time less than burst_time and
			// an inter-group delay variation d(i) less than 0 is considered
			// being part of the current group of packets.
			if interArrivalTimePkt(a.group, next) <= a.interArrivalThreshold &&
				interGroupDelayVariationPkt(a.group, next) < a.interGroupDelayVariationTreshold &&
				next.Arrival.Sub(a.group.packets[0].Arrival) < 100*time.Millisecond {
				a.group.add(next)

				continue
			}

			agWriter(a.group)
			a.group = newArrivalGroup(next)
		}
	}
}

func interArrivalTimePkt(group arrivalGroup, ack cc.Acknowledgment) time.Duration {
	return ack.Arrival.Sub(group.arrival)
}

func interDepartureTimePkt(group arrivalGroup, ack cc.Acknowledgment) time.Duration {
	if len(group.packets) == 0 {
		return 0
	}

	// Group membership uses the first send; delay deltas use the latest send.
	return ack.Departure.Sub(group.packets[0].Departure)
}

func interGroupDelayVariationPkt(group arrivalGroup, ack cc.Acknowledgment) time.Duration {
	return ack.Arrival.Sub(group.arrival) - ack.Departure.Sub(group.departure)
}
