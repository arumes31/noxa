package webrtc

import (
	"strings"

	"go.uber.org/zap"
)

// PublisherAccess describes the identity and channel metadata carried by a
// subscriber's SDP. It does not grant permission to deliver media packets.
type PublisherAccess struct {
	PublisherID, SubscriberID      string
	ChannelID, SubscriberChannelID int64
}

// PublisherGuard runs under Router.mu. It must use only immutable data and
// must not acquire server locks or call the router. Nil preserves legacy mode.
type PublisherGuard func(PublisherAccess) bool

func (r *Router) publisherAllowedLocked(subscriber, publisher string) bool {
	return r.echoPublisherAllowedLocked(subscriber, publisher) && (r.publisherGuard == nil || r.publisherGuard(PublisherAccess{
		publisher, subscriber, r.clientChan[publisher], r.clientChan[subscriber],
	}))
}

// SetPublisherGuard installs a snapshot, synchronously removes forbidden
// tracks and restores pairs the snapshot allows again, then schedules
// renegotiation for every subscriber whose track set changed.
//
// Pruning without renegotiating left the subscriber's SDP announcing a track
// whose sender had been removed: that listener heard nothing from that one
// publisher until some unrelated membership change happened to renegotiate
// it. Concurrent refreshes (one per renegotiation offer, so N-1 at once on
// every join) made this likely: an older snapshot that did not yet know the
// newest member could be installed last and silently cut that member off.
// The renegotiation hook only arms a timer, so it is safe to call while the
// caller holds an authorization lease; offers are built later under the
// offer guard.
func (r *Router) SetPublisherGuard(guard PublisherGuard) {
	r.mu.Lock()
	r.publisherGuard = guard
	renegotiate := r.prunePublishersLocked()
	for sub := range r.restorePublishersLocked() {
		renegotiate[sub] = true
	}
	hook := r.onRenegotiate
	r.mu.Unlock()
	if hook != nil {
		for sub := range renegotiate {
			hook(sub)
		}
	}
}

// restorePublishersLocked recreates channel (and active whisper) pairs that
// are allowed but missing, for every subscriber with a peer connection. It
// returns the subscribers whose track set changed. Callers must hold mu.
func (r *Router) restorePublishersLocked() map[string]bool {
	changed := make(map[string]bool)
	for sub := range r.pubPeers {
		if r.restoreSubscriberLocked(sub) {
			changed[sub] = true
		}
	}
	return changed
}

// restoreSubscriberLocked adds every allowed but missing pair for one
// subscriber. Callers must hold mu.
func (r *Router) restoreSubscriberLocked(subscriber string) bool {
	changed := false
	channelID := r.clientChan[subscriber]
	if channelID <= 0 {
		return false
	}
	for pub := range r.members[channelID] {
		if (pub != subscriber || channelID == r.echoChannel) && r.addPublisherLocked(subscriber, pub) {
			changed = true
		}
	}
	for pub, cfg := range r.whispers {
		if cfg.active && r.whisperTargetsLocked(pub, cfg)[subscriber] && r.addPublisherLocked(subscriber, pub) {
			if r.whisperPairs[pub] == nil {
				r.whisperPairs[pub] = make(map[string]bool)
			}
			r.whisperPairs[pub][subscriber] = true
			changed = true
		}
	}
	return changed
}

// repairSubscriberLocked rebuilds audio outputs of this subscriber whose
// egress stream was stopped while the pair itself is still wanted. A stopped
// output would otherwise stay announced in SDP and silent forever. Video
// slots are left to the video watch machinery. Callers must hold mu.
func (r *Router) repairSubscriberLocked(subscriber string) bool {
	pc := r.pubPeers[subscriber]
	if pc == nil {
		return false
	}
	changed := false
	for pub, t := range r.pubTracks[subscriber] {
		for slot, s := range t.audio {
			if s.egress == nil || s.egress.Active() {
				continue
			}
			r.logger.Warn("router: rebuilding stopped audio output",
				zap.String("subscriber_id", subscriber), zap.String("publisher_id", pub), zap.String("slot", slot))
			r.removeSlotLocked(pc, t, slot)
			if r.addSlotLocked(pc, subscriber, pub, t, slot) {
				changed = true
			}
		}
	}
	return changed
}

func (r *Router) prunePublishersLocked() map[string]bool {
	renegotiate := make(map[string]bool)
	// Most refreshes leave every active watch authorized. A needless writer
	// would wait for in-flight packet writes while holding Router.mu, blocking
	// unrelated ingress readers and making new final packet TryRLocks fail.
	needsRevocation := false
	r.watchMu.RLock()
	for key, watch := range r.watches {
		if watch.active && !r.publisherAllowedLocked(key.subscriber, key.publisher) {
			needsRevocation = true
			break
		}
	}
	r.watchMu.RUnlock()
	if needsRevocation {
		// Router.mu excludes new watches and permission/membership changes.
		// RevokeVideo may still delete entries, so rescan rather than restoring
		// entries captured above. Actual revocation still drains packet writes.
		r.watchMu.Lock()
		for key, watch := range r.watches {
			if watch.active && !r.publisherAllowedLocked(key.subscriber, key.publisher) {
				watch.active = false
				r.watches[key] = watch
			}
		}
		r.watchMu.Unlock()
	}
	for sub, publishers := range r.pubTracks {
		for pub := range publishers {
			if !r.publisherAllowedLocked(sub, pub) {
				if r.removePublisherLocked(sub, pub) {
					renegotiate[sub] = true
				}
				delete(r.whisperPairs[pub], sub)
			}
		}
	}
	return renegotiate
}

// PrepareSubscriber refreshes this subscriber's tracks without recursively
// scheduling another offer. It runs immediately before SDP construction.
// unannouncedOutputs counts this subscriber's output tracks whose MSID does
// not appear in the given local SDP, i.e. tracks the client cannot receive
// until another server offer is negotiated.
func (r *Router) unannouncedOutputs(subscriber, localSDP string) int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	missing := 0
	for _, t := range r.pubTracks[subscriber] {
		for _, set := range []map[string]*pubSlot{t.audio, t.video} {
			for _, s := range set {
				if s.track == nil {
					continue
				}
				if !strings.Contains(localSDP, "a=msid:"+s.track.StreamID()+" "+s.track.ID()+"\r\n") &&
					!strings.Contains(localSDP, "a=msid:"+s.track.StreamID()+" "+s.track.ID()+"\n") {
					missing++
				}
			}
		}
	}
	return missing
}

// sanitizeOffer prepares an earlier server offer for redelivery: MSID
// attributes of publisher tracks that are no longer this subscriber's outputs
// (the publisher left, or was hidden from it since) are removed, so a
// redelivered offer never discloses a revoked publisher. The m-line itself
// must stay, since Pion's pending offer still contains it; without an MSID
// the frontend binds nothing to it. It reports whether anything was removed.
func (r *Router) sanitizeOffer(subscriber, offerSDP string) (string, bool) {
	r.mu.RLock()
	current := make(map[string]bool)
	for _, t := range r.pubTracks[subscriber] {
		for _, set := range []map[string]*pubSlot{t.audio, t.video} {
			for _, s := range set {
				if s.track != nil {
					current[s.track.StreamID()+" "+s.track.ID()] = true
				}
			}
		}
	}
	r.mu.RUnlock()
	// Work per media section: a stale section loses every attribute that
	// names its source (a=msid and the a=ssrc msid/mslabel/label lines).
	var sections [][]string
	for _, raw := range strings.SplitAfter(offerSDP, "\n") {
		if strings.HasPrefix(raw, "m=") || len(sections) == 0 {
			sections = append(sections, nil)
		}
		sections[len(sections)-1] = append(sections[len(sections)-1], raw)
	}
	var out strings.Builder
	removed := false
	for i, section := range sections {
		stale := false
		for _, raw := range section {
			line := strings.TrimRight(raw, "\r\n")
			value, ok := strings.CutPrefix(line, "a=msid:")
			if !ok && strings.HasPrefix(line, "a=ssrc:") {
				if _, attr, found := strings.Cut(line, " "); found {
					value, ok = strings.CutPrefix(attr, "msid:")
				}
			}
			if i > 0 && ok && strings.HasPrefix(value, "noxa-") && !current[value] {
				stale = true
			}
		}
		for _, raw := range section {
			if stale && (strings.HasPrefix(raw, "a=msid:") || strings.HasPrefix(raw, "a=ssrc:") || strings.HasPrefix(raw, "a=ssrc-group:")) {
				removed = true
				continue
			}
			out.WriteString(raw)
		}
	}
	return out.String(), removed
}

// It reports whether this subscriber's output tracks changed, so a caller
// answering a client offer knows a server offer must follow.
func (r *Router) PrepareSubscriber(subscriber string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	changed := r.prunePublishersLocked()[subscriber]
	if r.repairSubscriberLocked(subscriber) {
		changed = true
	}
	if r.restoreSubscriberLocked(subscriber) {
		changed = true
	}
	return changed
}

func (v *Voice) SetPublisherGuard(guard PublisherGuard) { v.router.SetPublisherGuard(guard) }
