package webrtc

import (
	"errors"
	"net"
	"time"

	"github.com/pion/rtp"
)

const videoReorderWait = 200 * time.Millisecond
const videoReorderPackets = 128
const videoReorderBytes = 256 * 1024

type orderedVideoPacket struct {
	packet *rtp.Packet
	codec  string
	at     time.Time
}

// Reordering precedes dimension inspection. A repaired gap must not destroy
// the inspector's known reference frame. Only packets held behind a gap are
// cloned; the ordinary contiguous path still forwards without a payload copy.
type videoReorder struct {
	seen    bool
	next    uint16
	bytes   int
	pending map[uint16]orderedVideoPacket
}

func (q *videoReorder) deadline() time.Time {
	var oldest time.Time
	for _, entry := range q.pending {
		if oldest.IsZero() || entry.at.Before(oldest) {
			oldest = entry.at
		}
	}
	if oldest.IsZero() {
		return time.Time{}
	}
	return oldest.Add(videoReorderWait)
}

func (q *videoReorder) push(packet *rtp.Packet, codec string, now time.Time, emit func(*rtp.Packet, string)) {
	if !q.seen {
		q.seen, q.next = true, packet.SequenceNumber
	}
	if int16(packet.SequenceNumber-q.next) < 0 {
		return // already delivered or abandoned after the bounded repair window
	}
	if packet.SequenceNumber == q.next {
		q.next++
		emit(packet, codec)
		q.flush(time.Time{}, false, emit)
		return
	}
	if _, duplicate := q.pending[packet.SequenceNumber]; duplicate {
		return
	}
	size := packet.MarshalSize()
	if size > 65535 {
		return
	}
	if len(q.pending) >= videoReorderPackets || q.bytes+size > videoReorderBytes {
		// Exhaustion declares the oldest gap lost. The inspector still checks
		// every released packet and rejects unknown/oversized references.
		q.flush(now, true, emit)
		if int16(packet.SequenceNumber-q.next) < 0 {
			return
		}
	}
	if q.pending == nil {
		q.pending = make(map[uint16]orderedVideoPacket)
	}
	q.pending[packet.SequenceNumber] = orderedVideoPacket{packet.Clone(), codec, now}
	q.bytes += size
	q.flush(time.Time{}, false, emit)
}

func (q *videoReorder) flush(now time.Time, force bool, emit func(*rtp.Packet, string)) {
	for len(q.pending) > 0 {
		entry, found := q.pending[q.next]
		if !found {
			if !force && now.Before(q.deadline()) {
				return
			}
			nearest := uint16(32767)
			for sequence := range q.pending {
				if distance := sequence - q.next; distance < nearest {
					nearest = distance
				}
			}
			q.next += nearest
			entry = q.pending[q.next]
		}
		delete(q.pending, q.next)
		q.bytes -= entry.packet.MarshalSize()
		q.next++
		emit(entry.packet, entry.codec)
	}
}

// Pion's read deadline lets an idle screen release its final queued frame
// without a helper goroutine. Fakes without deadlines flush at EOF instead.
func readOrderedVideo(track VideoTrackReader, current func() bool, codec func() string, emit func(*rtp.Packet, string)) {
	var queue videoReorder
	deadline, hasDeadline := track.(interface{ SetReadDeadline(time.Time) error })
	for current() {
		if hasDeadline {
			if err := deadline.SetReadDeadline(queue.deadline()); err != nil {
				return
			}
		}
		packet, _, err := track.ReadRTP()
		if !current() {
			return
		}
		if err != nil {
			var timeout net.Error
			if hasDeadline && errors.As(err, &timeout) && timeout.Timeout() {
				if queue.deadline().IsZero() {
					return
				}
				// Pion polls its separate RTX queue before blocking on primary
				// RTP. A repair arriving during that blocked read cannot wake it.
				// Keep the primary deadline expired while draining queued repairs
				// before declaring the gap lost; bound work under adversarial input.
				for range videoReorderPackets {
					repaired, _, repairErr := track.ReadRTP()
					if !current() {
						return
					}
					if repairErr != nil {
						if errors.As(repairErr, &timeout) && timeout.Timeout() {
							break
						}
						queue.flush(time.Now(), true, emit)
						return
					}
					if repaired != nil {
						queue.push(repaired, codec(), time.Now(), emit)
					}
				}
				queue.flush(time.Now(), false, emit)
				continue
			}
			queue.flush(time.Now(), true, emit)
			return
		}
		if packet != nil {
			queue.push(packet, codec(), time.Now(), emit)
		}
	}
}
