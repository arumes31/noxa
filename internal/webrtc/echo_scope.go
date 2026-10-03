package webrtc

// Echo membership has a separate final-write gate: buffered media and
// retransmissions cannot acquire Router.mu while writing through Pion.
// Control changes hold Router.mu before echoScopeMu; final writes acquire
// echoScopeMu before the whisper, watch and egress gates.
func (r *Router) refreshEchoScopeLocked() {
	r.echoScopeMu.Lock()
	defer r.echoScopeMu.Unlock()
	if r.echoMembers == nil {
		r.echoMembers = make(map[string]uint64)
	}
	for client := range r.echoMembers {
		if !r.inEchoLocked(client) {
			delete(r.echoMembers, client)
		}
	}
	if r.echoChannel == 0 {
		return
	}
	for client := range r.members[r.echoChannel] {
		if r.echoMembers[client] == 0 {
			r.echoScopeEpoch++
			r.echoMembers[client] = r.echoScopeEpoch
		}
	}
}

func (r *Router) inEchoLocked(client string) bool {
	return r.echoChannel != 0 && r.clientChan[client] == r.echoChannel
}

func (r *Router) echoPublisherAllowedLocked(subscriber, publisher string) bool {
	return subscriber == publisher || (!r.inEchoLocked(subscriber) && !r.inEchoLocked(publisher))
}

func (r *Router) whisperActiveLocked(sender string, cfg *whisperConfig) bool {
	return cfg != nil && cfg.active && !r.inEchoLocked(sender)
}

func (r *Router) withEchoScope(delivery MediaDelivery, write func() error) error {
	if !r.echoScopeMu.TryRLock() {
		return nil
	}
	defer r.echoScopeMu.RUnlock()
	senderEpoch := r.echoMembers[delivery.SenderID]
	if delivery.EchoEpoch != senderEpoch {
		return nil
	}
	if senderEpoch != 0 || r.echoMembers[delivery.RecipientID] != 0 {
		if delivery.Tap || delivery.SenderID != delivery.RecipientID {
			return nil
		}
	}
	return write()
}

func (r *Router) withMediaScope(delivery MediaDelivery, write func() error) error {
	return r.withEchoScope(delivery, func() error {
		return r.withWhisperScope(delivery, func() error { return r.withWatch(delivery, write) })
	})
}
