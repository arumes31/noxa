package webrtc

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
	return r.publisherGuard == nil || r.publisherGuard(PublisherAccess{
		publisher, subscriber, r.clientChan[publisher], r.clientChan[subscriber],
	})
}

// SetPublisherGuard installs a snapshot and synchronously removes forbidden
// tracks. It intentionally does not invoke renegotiation callbacks: callers
// may hold an authorization lease while preparing an SDP offer or answer.
// The next SDP preparation restores newly allowed channel/whisper tracks.
func (r *Router) SetPublisherGuard(guard PublisherGuard) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.publisherGuard = guard
	r.prunePublishersLocked()
}

func (r *Router) prunePublishersLocked() {
	for sub, publishers := range r.pubTracks {
		for pub := range publishers {
			if !r.publisherAllowedLocked(sub, pub) {
				r.removePublisherLocked(sub, pub)
				delete(r.whisperPairs[pub], sub)
			}
		}
	}
}

// PrepareSubscriber refreshes this subscriber's tracks without recursively
// scheduling another offer. It runs immediately before SDP construction.
func (r *Router) PrepareSubscriber(subscriber string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.prunePublishersLocked()
	channelID := r.clientChan[subscriber]
	if channelID <= 0 {
		return
	}
	for pub := range r.members[channelID] {
		if pub != subscriber || channelID == r.echoChannel {
			r.addPublisherLocked(subscriber, pub)
		}
	}
	for pub, cfg := range r.whispers {
		if cfg.active && r.whisperTargetsLocked(pub, cfg)[subscriber] && r.addPublisherLocked(subscriber, pub) {
			if r.whisperPairs[pub] == nil {
				r.whisperPairs[pub] = make(map[string]bool)
			}
			r.whisperPairs[pub][subscriber] = true
		}
	}
}

func (v *Voice) SetPublisherGuard(guard PublisherGuard) { v.router.SetPublisherGuard(guard) }
