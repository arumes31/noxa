package webrtc

import (
	"errors"
	"net"
	"sort"
	"time"

	"github.com/pion/rtp"
)

const videoReorderWait = 200 * time.Millisecond

// A 50 Mbps source needs about 1.25 MB and 1,042 ordinary RTP packets for
// this repair window. Leave bounded headroom for keyframe bursts; a tiny
// packet cap otherwise abandons gaps before the first NACK can return.
const videoReorderPackets = 2048
const videoReorderBytes = 2 * 1024 * 1024

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
	expires time.Time
	dirty   bool
}

func (q *videoReorder) deadline() time.Time {
	// Reads behind the same missing packet must not scan the entire repair
	// window. Recompute only after its oldest entry has actually been removed.
	if q.dirty {
		q.expires = time.Time{}
		for _, entry := range q.pending {
			expires := entry.at.Add(videoReorderWait)
			if q.expires.IsZero() || expires.Before(q.expires) {
				q.expires = expires
			}
		}
		q.dirty = false
	}
	return q.expires
}

func (q *videoReorder) push(packet *rtp.Packet, codec string, now time.Time, emit func(*rtp.Packet, string)) {
	if !q.seen {
		q.seen, q.next = true, packet.SequenceNumber
	}
	if rtpSequenceDelta(packet.SequenceNumber, q.next) < 0 {
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
		if rtpSequenceDelta(packet.SequenceNumber, q.next) < 0 {
			return
		}
	}
	if q.pending == nil {
		q.pending = make(map[uint16]orderedVideoPacket)
	}
	expires := now.Add(videoReorderWait)
	if len(q.pending) == 0 {
		q.expires, q.dirty = expires, false
	} else if !q.dirty && expires.Before(q.expires) {
		q.expires = expires
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
			q.flushGaps(now, force, emit)
			return
		}
		delete(q.pending, q.next)
		if entry.at.Add(videoReorderWait).Equal(q.expires) {
			q.dirty = true
		}
		q.bytes -= entry.packet.MarshalSize()
		q.next++
		emit(entry.packet, entry.codec)
	}
	q.expires, q.dirty = time.Time{}, false
}

// Expiration/pressure can expose many gaps at once. Sort once and retain each
// suffix's earliest deadline instead of repeatedly scanning the entire map.
// The ordinary contiguous path above does not allocate this work list.
func (q *videoReorder) flushGaps(now time.Time, force bool, emit func(*rtp.Packet, string)) {
	type pendingDeadline struct {
		sequence uint16
		expires  time.Time
	}
	ordered := make([]pendingDeadline, 0, len(q.pending))
	for sequence, entry := range q.pending {
		ordered = append(ordered, pendingDeadline{sequence, entry.at.Add(videoReorderWait)})
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].sequence-q.next < ordered[j].sequence-q.next })
	for index := len(ordered) - 2; index >= 0; index-- {
		if ordered[index+1].expires.Before(ordered[index].expires) {
			ordered[index].expires = ordered[index+1].expires
		}
	}
	for _, pending := range ordered {
		if pending.sequence != q.next && !force && now.Before(pending.expires) {
			q.expires, q.dirty = pending.expires, false
			return
		}
		entry := q.pending[pending.sequence]
		delete(q.pending, pending.sequence)
		q.bytes -= entry.packet.MarshalSize()
		q.next = pending.sequence + 1
		emit(entry.packet, entry.codec)
	}
	q.expires, q.dirty = time.Time{}, false
}

// Pion's read deadline lets an idle screen release its final queued frame
// without a helper goroutine. Fakes without deadlines flush at EOF instead.
func readOrderedVideo(track VideoTrackReader, current func() bool, codec func() string, emit func(*rtp.Packet, string), observe func(*rtp.Packet)) {
	push := func(queue *videoReorder, packet *rtp.Packet) {
		if observe != nil {
			observe(packet)
		}
		queue.push(packet, codec(), time.Now(), emit)
	}
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
						push(&queue, repaired)
					}
				}
				queue.flush(time.Now(), false, emit)
				continue
			}
			queue.flush(time.Now(), true, emit)
			return
		}
		if packet != nil {
			push(&queue, packet)
		}
	}
}
