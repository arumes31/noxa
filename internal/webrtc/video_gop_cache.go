package webrtc

import (
	"strings"
	"sync"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
)

// A source's GOP cache holds its latest complete keyframe access unit and
// every forwarded unit since, so a newly watching viewer, or a single viewer
// that lost its picture, can start from the server instead of forcing a new
// keyframe on the publisher and thereby on every other viewer.
//
// Bounds: a keyframe older than gopCacheMaxAge is not served and its units are
// released (screen encoders may keep one GOP for minutes); bytes and packets
// are capped independently. Any loss or format change after the keyframe
// invalidates the cache until the next complete keyframe.
const (
	gopCacheMaxAge     = 2 * time.Second
	gopCacheMaxBytes   = 8 << 20
	gopCacheMaxPackets = 8192
	// A source idle for longer may be paused screen capture: a cached frame
	// would be the stale picture, and the publisher must be woken instead.
	gopCacheLiveness = 250 * time.Millisecond
	// One viewer's refills are spaced like upstream keyframe requests.
	gopRefillInterval = keyframeRequestWindow
	// A refill not served by the next live unit falls back upstream.
	gopRefillTimeout = 500 * time.Millisecond
	// The cached burst drains through the viewer's own pacer at 1.5x its
	// congestion target, so it catches up to live without exceeding the BWE.
	// Live packets queue behind it: bound that wait well below the pacer's
	// packet lifetime (mediaPacerLifetime, 3 s) so nothing behind it expires.
	// A viewer that cannot absorb the burst asks the publisher instead.
	gopCacheMaxDrain = 1500 * time.Millisecond
)

type gopUnit struct {
	packets  []*rtp.Packet // owned by the cache; never mutated after insert
	bytes    int
	keyframe bool
}

type gopCache struct {
	mu      sync.Mutex
	mime    string
	ssrc    uint32
	units   []gopUnit
	packets int
	bytes   int
	keyAt   time.Time
	lastAt  time.Time
	nextSeq uint16
}

func gopCacheable(mime string) bool {
	return strings.EqualFold(mime, webrtc.MimeTypeH264) || strings.EqualFold(mime, webrtc.MimeTypeH265)
}

// observe runs on the source's dispatcher after a unit was forwarded. lost
// reports a dependency lost before this unit (including dispatch loss).
func (c *gopCache) observe(mime string, packets []*rtp.Packet, keyframe, lost bool, now time.Time) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(packets) == 0 {
		if lost {
			c.resetLocked() // a rejected unit breaks the cached dependency chain
		}
		return
	}
	if !gopCacheable(mime) {
		c.resetLocked()
		return
	}
	first, last := packets[0], packets[len(packets)-1]
	if keyframe {
		c.resetLocked()
		c.mime, c.ssrc, c.keyAt = mime, first.SSRC, now
	} else if len(c.units) == 0 || lost || !strings.EqualFold(c.mime, mime) || c.ssrc != first.SSRC || first.SequenceNumber != c.nextSeq {
		c.resetLocked()
		return
	}
	if now.Sub(c.keyAt) > gopCacheMaxAge {
		c.resetLocked()
		return
	}
	size := 0
	for _, packet := range packets {
		size += packet.MarshalSize()
	}
	if c.packets+len(packets) > gopCacheMaxPackets || c.bytes+size > gopCacheMaxBytes {
		c.resetLocked()
		return
	}
	c.units = append(c.units, gopUnit{packets: packets, bytes: size, keyframe: keyframe})
	c.packets += len(packets)
	c.bytes += size
	c.lastAt = now
	c.nextSeq = last.SequenceNumber + 1
}

func (c *gopCache) invalidate() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.resetLocked()
	c.mu.Unlock()
}

func (c *gopCache) resetLocked() {
	clear(c.units)
	c.units = c.units[:0]
	if cap(c.units) > 1024 {
		c.units = nil
	}
	c.packets, c.bytes = 0, 0
	c.keyAt, c.lastAt = time.Time{}, time.Time{}
	c.mime, c.ssrc = "", 0
}

// servableLocked reports whether the cache can start a viewer at once.
func (c *gopCache) servableLocked(now time.Time, mime string) bool {
	return len(c.units) > 0 && c.units[0].keyframe && strings.EqualFold(c.mime, mime) &&
		now.Sub(c.keyAt) <= gopCacheMaxAge && now.Sub(c.lastAt) <= gopCacheLiveness
}

func (c *gopCache) servable(now time.Time, mime string) bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.servableLocked(now, mime)
}

// snapshotBefore returns the cached units when next is the first packet of
// the following live unit, so cached and live media join without overlap.
// The returned slice is a copy; the packets are immutable.
func (c *gopCache) snapshotBefore(now time.Time, next *rtp.Packet, mime string) []gopUnit {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.servableLocked(now, mime) || next.SSRC != c.ssrc || next.SequenceNumber != c.nextSeq {
		return nil
	}
	return append([]gopUnit(nil), c.units...)
}

func (c *gopCache) size() (units, packets, bytes int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.units), c.packets, c.bytes
}

// gopCacheLocked resolves a live source's cache. Caller holds Router.mu.
func (r *Router) gopCacheLocked(publisher, slot, rid string) *gopCache {
	return r.gopCaches[videoIngressKey{publisher, slot, rid}]
}

// gopServable reports whether a recipient output can be started from the
// cache of the publisher's current source layer.
func (r *Router) gopServable(publisher, slot, rid string, output *pubSlot, now time.Time) bool {
	if output == nil || output.egress == nil {
		return false
	}
	r.mu.RLock()
	cache := r.gopCacheLocked(publisher, slot, rid)
	r.mu.RUnlock()
	return cache.servable(now, output.track.Codec().MimeType)
}

// requestGOPRefill asks the dispatcher to restart one recipient from the
// cache on the next live unit. It returns false when the caller must ask the
// publisher instead: no servable cache, or an earlier refill went unserved.
func (s *pubSlot) requestGOPRefill(now time.Time) (accepted bool) {
	requested := s.gopRefillAt.Load()
	switch {
	case requested != 0 && now.Sub(time.Unix(0, requested)) > gopRefillTimeout:
		s.gopRefillAt.Store(0)
		return false
	case requested != 0:
		return true // already pending
	}
	if last := s.gopRefilledAt.Load(); last != 0 && now.Sub(time.Unix(0, last)) < gopRefillInterval {
		return true // a fresh start from the cache was just delivered
	}
	s.gopRefillAt.Store(now.UnixNano())
	return true
}

// injectGOP restarts one recipient from cached units. The caller holds the
// output's videoMu, has authorized the delivery's watch, and holds no router,
// watch or egress lock. A refill of a playing recipient begins a new epoch so
// sequence numbers and timestamps continue from what it already received.
//
// The keyframe unit is admitted synchronously; started is false, and the
// caller falls back to an upstream request, if it was not. advanced reports
// that sequence numbers were handed out and must not be rolled back (a
// partially admitted keyframe leaves the recipient waiting for the next one).
// The rest of the GOP goes to the recipient's backfill queue, drained by its
// own worker: a 2 s GOP at 15 Mbit/s is ~3,000 packets, and admitting them on
// the source's fan-out path would delay every other viewer by tens of ms.
func (r *Router) injectGOP(output *pubSlot, ticket mediaTicket, mime string, units []gopUnit, now time.Time) (started, advanced bool) {
	bytes := 0
	for _, unit := range units {
		bytes += unit.bytes
	}
	if drain, ok := output.egress.videoDrain(bytes); !ok || drain > gopCacheMaxDrain {
		return false, false
	}
	saved := output.videoContinuity
	continuity := &output.videoContinuity
	if continuity.seen && !continuity.pendingEpoch {
		continuity.beginEpoch()
	}
	hevc := strings.EqualFold(mime, webrtc.MimeTypeH265)
	for u, unit := range units {
		if hevc && !continuity.beginH265Frame(unit.packets, unit.keyframe) {
			if u == 0 {
				output.videoContinuity = saved
				return false, false
			}
			continue // RASL after random access, as for a live start
		}
		for i, packet := range unit.packets {
			var out *rtp.Packet
			var ok bool
			frameStart := unit.keyframe && i == 0
			if hevc {
				out, ok = continuity.translateH265(packet, now, frameStart)
			} else {
				out, ok = continuity.translateH264(packet, now, frameStart)
			}
			if !ok {
				if u == 0 && i == 0 {
					output.videoContinuity = saved
					return false, false
				}
				continue
			}
			ticket.videoSource = packet.SSRC
			if u > 0 {
				output.backfillLocked(out, ticket)
				continue
			}
			if written, err := output.enqueueVideoLocked(out, ticket); !written || err != nil {
				if i == 0 {
					output.videoContinuity = saved
					return false, false
				}
				// Part of the keyframe is already queued under these sequence
				// numbers: keep them, and wait for a live keyframe.
				continuity.beginEpoch()
				return false, true
			}
		}
	}
	continuity.h265UnitFirst = nil
	output.videoSource.Store(units[0].packets[0].SSRC)
	output.gopRefillAt.Store(0)
	output.gopRefilledAt.Store(now.UnixNano())
	return true, true
}

// gopBackfillMax bounds one recipient's backfill: a full cache plus live
// media queued behind it while a control update delays admission.
const gopBackfillMax = 2 * gopCacheMaxPackets

type gopBackfillPacket struct {
	packet *rtp.Packet
	ticket mediaTicket
}

// enqueueVideoLocked admits one translated packet for this recipient, behind
// any cache backfill still being drained so its order is preserved. The
// caller holds videoMu.
func (s *pubSlot) enqueueVideoLocked(pkt *rtp.Packet, ticket mediaTicket) (bool, error) {
	if len(s.backfill) == 0 {
		return s.enqueueMedia(pkt, ticket)
	}
	if len(s.backfill) >= gopBackfillMax {
		return false, nil
	}
	s.backfill = append(s.backfill, gopBackfillPacket{pkt, ticket})
	return true, nil
}

// backfillLocked queues a packet for the recipient's backfill worker,
// starting it if needed. The caller holds videoMu.
func (s *pubSlot) backfillLocked(pkt *rtp.Packet, ticket mediaTicket) {
	if len(s.backfill) >= gopBackfillMax {
		return
	}
	s.backfill = append(s.backfill, gopBackfillPacket{pkt, ticket})
	if !s.backfilling {
		s.backfilling = true
		go s.drainBackfill()
	}
}

// drainBackfill admits queued packets one at a time under videoMu, so live
// packets for this recipient wait at most one admission. It exits as soon
// as the queue is empty; a retired output fails admission immediately.
func (s *pubSlot) drainBackfill() {
	for {
		s.videoMu.Lock()
		if len(s.backfill) == 0 {
			s.backfill, s.backfilling = nil, false
			s.videoMu.Unlock()
			return
		}
		next := s.backfill[0]
		s.backfill[0] = gopBackfillPacket{}
		s.backfill = s.backfill[1:]
		_, _ = s.enqueueMedia(next.packet, next.ticket)
		s.videoMu.Unlock()
	}
}
