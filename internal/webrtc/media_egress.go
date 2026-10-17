package webrtc

import (
	"errors"
	"log"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
)

// Retain the whole bounded pacing queue plus recent NACK retransmissions.
// Count and lifetime remain bounded; every write still rechecks authority.
const mediaTicketCount = mediaPacerPackets + 4096
const mediaTicketLifetime = time.Second
const mediaVideoTicketLifetime = mediaPacerLifetime + mediaTicketLifetime

// Admission avoids filling queues with already-denied media. It releases its
// lease before entering Pion: the terminal interceptor checks again after all
// buffering, without acquiring Authority recursively through a track lock.
func (s *pubSlot) enqueueMedia(pkt *rtp.Packet, ticket mediaTicket) (bool, error) {
	// Pion binds senders before ICE/DTLS is ready. Admitting video then both
	// commits an unseen keyframe and fills the pacing queue with stale frames.
	// Return false so the caller rolls continuity back until transport is ready.
	if ticket.router != nil && (ticket.delivery.Slot == SlotCam || ticket.delivery.Slot == SlotScreen) &&
		!s.videoTransportReady(ticket.router, ticket.delivery.RecipientID) {
		return false, nil
	}
	allowed := false
	admit := func() error { allowed = true; return nil }
	switch {
	case ticket.router != nil:
		if err := ticket.router.mediaCommit(ticket.delivery, ticket.guard, ticket.videoRevision)(admit); err != nil {
			return false, err
		}
	case ticket.guard != nil:
		if err := ticket.guard(ticket.delivery, admit); err != nil {
			return false, err
		}
	default:
		allowed = true
	}
	if !allowed {
		return false, nil
	}
	s.startVideoProbe(pkt, ticket)
	packet, ok := s.egress.prepare(pkt, ticket)
	if !ok {
		return false, nil
	}
	err := s.WriteRTP(&packet)
	return err == nil, err
}

// mediaEgressRegistry associates Pion sender bindings with router outputs.
// Ticket IDs are process-local and never reused, including after ReplaceTrack.
type mediaEgressRegistry struct {
	mu      sync.Mutex
	next    uint64
	streams map[string]*mediaEgressStream
}

type mediaTicket struct {
	id            uint64
	expires       time.Time
	delivery      MediaDelivery
	guard         MediaGuard
	router        *Router
	videoRevision uint64
	csrc          []uint32
	videoSource   uint32
	videoRID      string
	videoMedia    bool
	// Diagnostic-only mutable receipt state; protected by videoDiagnostic.mu.
	// All authorization and source fields above remain immutable after prepare.
	repairDiagnostic videoRepairTicketDiagnostic
}

type mediaEgressStream struct {
	mu         sync.RWMutex
	active     bool
	retryAfter time.Time
	failures   atomic.Uint32 // consecutive non-timeout write failures
	// admitted/written count packets accepted for this output and packets
	// that reached the transport; a gap between them localizes a silent pair.
	admitted, written atomic.Uint64
	registry          *mediaEgressRegistry
	// Immutable owner notification; the engine registry is shared by all peers.
	onVideoReady func()
	// mu protects lifecycle and remains held through authorized socket writes.
	// Ticket admission uses its shared side: a slow socket must not block the
	// publisher's sequential fan-out to other recipients. ticketsMu protects
	// the bounded map/ring only, always acquired after mu, never across writes.
	ticketsMu       sync.Mutex
	tickets         map[uint64]*mediaTicket
	order           [mediaTicketCount]uint64
	nextTicket      int
	outputSSRC      atomic.Uint32
	videoDiagnostic videoForwardDiagnostic
	pacer           *mediaPacer // immutable after binding; protected by mu
}

func (r *mediaEgressRegistry) ticketID() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.next == ^uint64(0) {
		return 0
	}
	r.next++
	return r.next
}

func (s *mediaEgressStream) prepare(pkt *rtp.Packet, ticket mediaTicket) (rtp.Packet, bool) {
	id := s.registry.ticketID()
	if id == 0 {
		return rtp.Packet{}, false
	}
	lifetime := mediaTicketLifetime
	if ticket.delivery.Slot == SlotCam || ticket.delivery.Slot == SlotScreen {
		lifetime = mediaVideoTicketLifetime
	}
	ticket.id, ticket.expires = id, time.Now().Add(lifetime)
	ticket.repairDiagnostic = videoRepairTicketDiagnostic{}
	ticket.csrc = append([]uint32(nil), pkt.CSRC...)
	ticket.videoMedia = !isVideoPadding(pkt) && len(pkt.Payload) > 0
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.active || time.Now().Before(s.retryAfter) {
		return rtp.Packet{}, false
	}
	s.ticketsMu.Lock()
	defer s.ticketsMu.Unlock()
	if s.tickets == nil {
		s.tickets = make(map[uint64]*mediaTicket)
	}
	delete(s.tickets, s.order[s.nextTicket])
	s.order[s.nextTicket] = id
	s.nextTicket = (s.nextTicket + 1) % mediaTicketCount
	s.tickets[id] = &ticket
	s.admitted.Add(1)
	out := *pkt
	// Internal CSRC words survive both GCC and NACK/RTX header copies. The
	// terminal interceptor restores the publisher's CSRCs before SRTP. Never
	// use source-controlled sequence/timestamp values as permission tickets.
	out.CSRC = []uint32{uint32(id >> 32), uint32(id & 0xffffffff)}
	return out, true
}

func (s *mediaEgressStream) stop() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	wasActive := s.active
	s.active = false
	s.ticketsMu.Lock()
	clear(s.tickets)
	clear(s.order[:])
	s.ticketsMu.Unlock()
	return wasActive
}

func (s *mediaEgressStream) write(header *rtp.Header, payload []byte, attrs interceptor.Attributes, writer interceptor.RTPWriter) (int, error) {
	n, _, err := s.writeResult(header, payload, attrs, writer)
	return n, err
}

// The result describes only the branch that actually declined this write.
// Counters are updated by the pacer after every permission/stream lock unwinds.
func (s *mediaEgressStream) writeResult(header *rtp.Header, payload []byte, attrs interceptor.Attributes, writer interceptor.RTPWriter) (int, mediaTerminalDropReason, error) {
	return s.writeResultKind(header, payload, attrs, writer, mediaPacketPrimary)
}

// kind is captured from the negotiated binding by the pacer at admission.
func (s *mediaEgressStream) writeResultKind(header *rtp.Header, payload []byte, attrs interceptor.Attributes, writer interceptor.RTPWriter, kind mediaPacketKind) (int, mediaTerminalDropReason, error) {
	if len(header.CSRC) != 2 {
		return 0, mediaDropInvalidTicket, nil
	}
	id := uint64(header.CSRC[0])<<32 | uint64(header.CSRC[1])
	if id == 0 {
		return 0, mediaDropInvalidTicket, nil
	}
	s.mu.RLock()
	s.ticketsMu.Lock()
	ticket := s.tickets[id]
	s.ticketsMu.Unlock()
	active := s.active
	validTicket := ticket != nil && ticket.id == id
	s.mu.RUnlock()
	if !active {
		return 0, mediaDropInactiveStream, nil
	}
	if !validTicket {
		return 0, mediaDropMissingTicket, nil
	}
	n := 0
	var outputError error
	reason := mediaDropGuardDeclined
	write := func() error {
		reason = mediaDropScopeDeclined
		// Lock order: authority, member movement, video policy, echo, whisper, watch, stream, socket.
		// No router or track lock may be acquired while holding stream.mu.
		if ticket.router != nil && (ticket.delivery.Slot == SlotCam || ticket.delivery.Slot == SlotScreen) {
			if !ticket.router.videoPolicyMu.TryRLock() {
				reason = mediaDropPolicyBusy
				return nil
			}
			defer ticket.router.videoPolicyMu.RUnlock()
			if ticket.videoRevision != ticket.router.videoPolicy.revision {
				reason = mediaDropPolicyRevision
				return nil
			}
		}
		commit := func() error {
			s.mu.RLock()
			defer s.mu.RUnlock()
			if !s.active {
				reason = mediaDropInactiveStream
				return nil
			}
			s.ticketsMu.Lock()
			current := s.tickets[id] == ticket
			s.ticketsMu.Unlock()
			if !current {
				reason = mediaDropMissingTicket
				return nil
			}
			if !time.Now().Before(ticket.expires) {
				reason = mediaDropExpiredTicket
				return nil
			}
			reason = mediaDropUnclassified
			// Lifecycle/permission read leases still linearize revocation with
			// this write. Concurrent admission may evict a cache entry afterward,
			// but cannot change this ticket's immutable authorization fields.
			out := *header
			out.CSRC = ticket.csrc
			if ticket.delivery.sourceLive != nil && !ticket.delivery.sourceLive.Load() {
				reason = mediaDropScopeDeclined
				return nil
			}
			n, outputError = writer.Write(&out, payload, attrs)
			if n > 0 && outputError == nil {
				s.written.Add(1)
			}
			if n > 0 && outputError == nil {
				s.observeVideoKind(&out, out.MarshalSize()+len(payload)+int(out.PaddingSize), ticket, time.Now(), kind)
			}
			return outputError
		}
		if ticket.router != nil {
			return ticket.router.withMediaScope(ticket.delivery, commit)
		}
		return commit()
	}
	var err error
	if ticket.guard != nil {
		err = ticket.guard(ticket.delivery, write)
	} else {
		err = write()
	}
	// Handle failed output only after all authorization/policy/watch and
	// stream read locks unwind. A socket deadline does not invalidate ICE:
	// discard its backlog, then let fresh, reauthorized packets try again.
	// The cooldown bounds repeated attempts against a stalled transport.
	//
	// Any other write error is treated the same way, with a growing cooldown.
	// Retiring the output permanently turned one transient failure (an ICE
	// restart closing the muxed socket under the in-flight packet, ENOBUFS
	// during a burst) into a (subscriber, publisher) pair that stayed silent
	// for the rest of the session while every other pair kept working. A
	// sender that is really gone is stopped by Unbind/RemoveTrack instead.
	if outputError != nil {
		var timeout net.Error
		isTimeout := errors.As(outputError, &timeout) && timeout.Timeout()
		if failures, ok := s.backOff(isTimeout); ok && !isTimeout && failures == 1 {
			log.Printf("webrtc: media output slot=%s write failed: %v; backing off and retrying", ticket.delivery.Slot, outputError)
		}
	} else if n > 0 {
		s.failures.Store(0)
	}
	return n, reason, err
}

// mediaEgressRetryMax caps the cooldown after repeated non-timeout failures.
const mediaEgressRetryMax = 2 * time.Second

// backOff discards the backlog queued against a failed transport and pauses
// admission. Timeouts use the socket deadline; other errors back off
// exponentially from it up to mediaEgressRetryMax. It reports the number of
// consecutive non-timeout failures and whether the stream is still active.
func (s *mediaEgressStream) backOff(isTimeout bool) (uint32, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.active {
		return 0, false
	}
	s.ticketsMu.Lock()
	clear(s.tickets)
	clear(s.order[:])
	s.ticketsMu.Unlock()
	delay := mediaSocketWriteTimeout
	var failures uint32
	if !isTimeout {
		failures = s.failures.Add(1)
		for i := uint32(1); i < failures && delay < mediaEgressRetryMax; i++ {
			delay *= 2
		}
		delay = min(delay, mediaEgressRetryMax)
	}
	s.retryAfter = time.Now().Add(delay)
	return failures, true
}

// Active reports whether the output still admits media (diagnostics/tests).
func (s *mediaEgressStream) Active() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.active
}

type guardedLocalTrack struct {
	*webrtc.TrackLocalStaticRTP
	egress *mediaEgressStream
}

func (t *guardedLocalTrack) Bind(ctx webrtc.TrackLocalContext) (webrtc.RTPCodecParameters, error) {
	codec, err := t.TrackLocalStaticRTP.Bind(ctx)
	if err != nil {
		return codec, err
	}
	t.egress.outputSSRC.Store(uint32(ctx.SSRC()))
	r := t.egress.registry
	r.mu.Lock()
	if r.streams == nil {
		r.streams = make(map[string]*mediaEgressStream)
	}
	previous := r.streams[ctx.ID()]
	if previous != nil && previous != t.egress {
		r.mu.Unlock()
		_ = t.TrackLocalStaticRTP.Unbind(ctx)
		return codec, errors.New("webrtc: media sender binding already in use")
	}
	r.streams[ctx.ID()] = t.egress
	r.mu.Unlock()
	return codec, nil
}

func (t *guardedLocalTrack) Unbind(ctx webrtc.TrackLocalContext) error {
	t.egress.stop()
	r := t.egress.registry
	r.mu.Lock()
	if r.streams[ctx.ID()] == t.egress {
		delete(r.streams, ctx.ID())
	}
	r.mu.Unlock()
	return t.TrackLocalStaticRTP.Unbind(ctx)
}
