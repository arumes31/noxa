package webrtc

import (
	"time"

	"github.com/pion/rtp"
	pion "github.com/pion/webrtc/v4"
)

// The cold recipient probes actual current demand once. Merely binding dormant
// tracks, receiving audio or admitting a padding packet never consumes it.
func (s *pubSlot) startVideoProbe(packet *rtp.Packet, ticket mediaTicket) {
	if ticket.router == nil || ticket.videoSource == 0 || len(packet.Payload) == 0 || isVideoPadding(packet) {
		return
	}
	s.egress.mu.RLock()
	pacer := s.egress.pacer
	active := s.egress.active
	ssrc := s.egress.outputSSRC.Load()
	s.egress.mu.RUnlock()
	if pacer == nil || !active || pacer.videoStarted.Load() {
		return
	}
	// Binding precedes ICE/DTLS completion. Do not spend the feedback window
	// while the transport cannot yet send or acknowledge the queued packets.
	if !ticket.router.videoTransportConnected(ticket.delivery.RecipientID) {
		return
	}
	demand, authorized := ticket.router.videoStartupDemand(ticket.delivery, time.Now())
	if authorized {
		pacer.startVideo(ssrc, demand)
	}
}

func (r *Router) videoTransportConnected(recipient string) bool {
	r.mu.RLock()
	peer := r.pubPeers[recipient]
	connected := peer != nil && peer.pc.ConnectionState() == pion.PeerConnectionStateConnected
	r.mu.RUnlock()
	return connected
}

func (s *pubSlot) videoTransportReady(router *Router, recipient string) bool {
	s.egress.mu.RLock()
	pacer, active := s.egress.pacer, s.egress.active
	ssrc := s.egress.outputSSRC.Load()
	s.egress.mu.RUnlock()
	if !active || pacer == nil || ssrc == 0 || !router.videoTransportConnected(recipient) {
		return false
	}
	// An established voice transport can still have a new video sender waiting
	// for negotiation. An unbound TrackLocalStaticRTP reports success without
	// emitting anything, so verify the actual video binding before admission.
	pacer.mu.Lock()
	stream := pacer.streams[ssrc]
	ready := !pacer.closed && stream != nil && stream.active && !stream.audio
	pacer.mu.Unlock()
	return ready
}

func (p *mediaPacer) startVideo(ssrc uint32, demand int) bool {
	p.mu.Lock()
	stream := p.streams[ssrc]
	if p.closed || stream == nil || !stream.active || stream.audio || stream.twccID == 0 || p.startVideoProbe == nil || !p.videoStarted.CompareAndSwap(false, true) {
		p.mu.Unlock()
		return false
	}
	start := p.startVideoProbe
	p.mu.Unlock()
	return start(demand)
}

// Demand is the measured incoming rate of the sources this recipient currently
// watches. Unknown or idle sources contribute no guessed resolution budget.
// Lock ordering matches publication/watch admission, and no snapshot escapes.
func (r *Router) videoStartupDemand(delivery MediaDelivery, now time.Time) (int, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	r.watchMu.RLock()
	defer r.watchMu.RUnlock()
	if delivery.Tap || !r.watchAllowedLocked(delivery) || r.clientChan[delivery.RecipientID] != delivery.RecipientChannelID ||
		r.clientChan[delivery.SenderID] != delivery.ChannelID || !r.publisherAllowedLocked(delivery.RecipientID, delivery.SenderID) {
		return 0, false
	}
	var demand float64
	for key, watch := range r.watches {
		if key.subscriber != delivery.RecipientID || !watch.active || watch.session != r.watchSessions[key.subscriber] ||
			watch.publication != r.publications[publicationKey{key.publisher, key.slot}] ||
			r.clientChan[key.publisher] != delivery.RecipientChannelID || !r.publisherAllowedLocked(key.subscriber, key.publisher) {
			continue
		}
		pref := watch.qualityRID
		if pref == "" {
			pref = r.layerPrefLocked(key.subscriber)
		}
		candidates := layerFallback(pref)
		if r.videoSources[key.publisher][key.slot][""] != 0 {
			candidates = []string{""}
		}
		for _, rid := range candidates {
			input := r.videoIngress[videoIngressKey{key.publisher, key.slot, rid}]
			if input == nil || input.ssrc != r.videoSources[key.publisher][key.slot][rid] {
				continue
			}
			input.mu.Lock()
			sample := input.stage.snapshot(now)
			input.mu.Unlock()
			if !sample.Stale && sample.BitrateBPS != nil {
				demand += *sample.BitrateBPS
				break
			}
		}
	}
	// Bound integer conversion only. The estimator's configured ceiling decides
	// the probe rate; this is not a new media bandwidth policy.
	return int(min(demand, 1_000_000_000)), true
}
