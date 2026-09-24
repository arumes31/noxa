package webrtc

import (
	"errors"
	"log"
	"sync"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
)

const mediaTicketCount = 512
const mediaTicketLifetime = time.Second

// Admission avoids filling queues with already-denied media. It releases its
// lease before entering Pion: the terminal interceptor checks again after all
// buffering, without acquiring Authority recursively through a track lock.
func (s *pubSlot) enqueueMedia(pkt *rtp.Packet, ticket mediaTicket) (bool, error) {
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
}

type mediaEgressStream struct {
	mu         sync.RWMutex
	active     bool
	registry   *mediaEgressRegistry
	tickets    map[uint64]*mediaTicket
	order      [mediaTicketCount]uint64
	nextTicket int
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
	ticket.id, ticket.expires = id, time.Now().Add(mediaTicketLifetime)
	ticket.csrc = append([]uint32(nil), pkt.CSRC...)
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.active {
		return rtp.Packet{}, false
	}
	if s.tickets == nil {
		s.tickets = make(map[uint64]*mediaTicket)
	}
	delete(s.tickets, s.order[s.nextTicket])
	s.order[s.nextTicket] = id
	s.nextTicket = (s.nextTicket + 1) % mediaTicketCount
	s.tickets[id] = &ticket
	out := *pkt
	// Internal CSRC words survive both GCC and NACK/RTX header copies. The
	// terminal interceptor restores the publisher's CSRCs before SRTP. Never
	// use source-controlled sequence/timestamp values as permission tickets.
	out.CSRC = []uint32{uint32(id >> 32), uint32(id)}
	return out, true
}

func (s *mediaEgressStream) stop() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	wasActive := s.active
	s.active = false
	clear(s.tickets)
	clear(s.order[:])
	return wasActive
}

func (s *mediaEgressStream) write(header *rtp.Header, payload []byte, attrs interceptor.Attributes, writer interceptor.RTPWriter) (int, error) {
	if len(header.CSRC) != 2 {
		return 0, nil
	}
	id := uint64(header.CSRC[0])<<32 | uint64(header.CSRC[1])
	s.mu.RLock()
	ticket := s.tickets[id]
	valid := s.active && ticket != nil && ticket.id == id
	s.mu.RUnlock()
	if !valid {
		return 0, nil
	}
	n := 0
	var outputError error
	write := func() error {
		// Lock order: authority, member movement, video policy, watch, stream, socket.
		// No router or track lock may be acquired while holding stream.mu.
		if ticket.router != nil && (ticket.delivery.Slot == SlotCam || ticket.delivery.Slot == SlotScreen) {
			if !ticket.router.videoPolicyMu.TryRLock() {
				return nil
			}
			defer ticket.router.videoPolicyMu.RUnlock()
			if ticket.videoRevision != ticket.router.videoPolicy.revision {
				return nil
			}
		}
		commit := func() error {
			s.mu.RLock()
			defer s.mu.RUnlock()
			if !s.active || s.tickets[id] != ticket || !time.Now().Before(ticket.expires) {
				return nil
			}
			out := *header
			out.CSRC = ticket.csrc
			n, outputError = writer.Write(&out, payload, attrs)
			return outputError
		}
		if ticket.router != nil {
			return ticket.router.withWhisperScope(ticket.delivery, func() error { return ticket.router.withWatch(ticket.delivery, commit) })
		}
		return commit()
	}
	var err error
	if ticket.guard != nil {
		err = ticket.guard(ticket.delivery, write)
	} else {
		err = write()
	}
	// Retire failed terminal output only after all authorization/policy/watch
	// and stream read locks unwind. Guard denials do not poison a healthy
	// transport. Queued packets and NACKs lose their tickets together.
	if outputError != nil && s.stop() {
		log.Printf("webrtc: retiring failed media output slot=%s: %v; track rebuild required", ticket.delivery.Slot, outputError)
	}
	return n, err
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
