package webrtc

// Echo membership has a separate final-write gate: buffered media and
// retransmissions cannot acquire Router.mu while writing through Pion.
// Control changes hold Router.mu before echoScopeMu; final writes acquire
// echoScopeMu before the whisper, watch and egress gates.
func (r *Router) refreshEchoScopeLocked() {
	// Every join and leave refreshes this scope, usually without an echo
	// channel. A pending writer makes every final media write's TryRLock fail,
	// so take the gate only when membership actually changes. echoMembers is
	// mutated only here, under Router.mu, which the caller holds.
	if !r.echoScopeChangeLocked() {
		return
	}
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

func (r *Router) echoScopeChangeLocked() bool {
	if r.echoMembers == nil {
		return true
	}
	for client := range r.echoMembers {
		if !r.inEchoLocked(client) {
			return true
		}
	}
	if r.echoChannel == 0 {
		return false
	}
	for client := range r.members[r.echoChannel] {
		if r.echoMembers[client] == 0 {
			return true
		}
	}
	return false
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
	if !r.echoAllowedLocked(delivery) {
		return nil
	}
	return write()
}

// echoAllowedLocked requires echoScopeMu (shared or exclusive).
func (r *Router) echoAllowedLocked(delivery MediaDelivery) bool {
	senderEpoch := r.echoMembers[delivery.SenderID]
	if delivery.EchoEpoch != senderEpoch {
		return false
	}
	if senderEpoch != 0 || r.echoMembers[delivery.RecipientID] != 0 {
		if delivery.Tap || delivery.SenderID != delivery.RecipientID {
			return false
		}
	}
	return true
}

// mediaScopeMode selects how a final-write scope gate treats a pending
// control update. Realtime workers that can retry must not wait: a control
// writer may be joining them. The ingress dispatcher, which cannot retry a
// skipped packet without leaving an unrecoverable sequence hole, waits.
type mediaScopeMode uint8

const (
	mediaScopeTry mediaScopeMode = iota
	mediaScopeWait
)

func (m mediaScopeMode) acquire(mu *contextRWMutex) bool {
	if m == mediaScopeWait {
		mu.RLock()
		return true
	}
	return mu.TryRLock()
}

// gatedMediaScope runs write when echo, whisper and watch scopes still allow
// the delivery. busy reports that a gate was contended (mediaScopeTry only),
// which is not a denial: the caller may retry the identical delivery.
func (r *Router) gatedMediaScope(delivery MediaDelivery, mode mediaScopeMode, write func() error) (busy bool, err error) {
	if !mode.acquire(&r.echoScopeMu) {
		return true, nil
	}
	defer r.echoScopeMu.RUnlock()
	if !r.echoAllowedLocked(delivery) {
		return false, nil
	}
	if delivery.Slot == SlotMic {
		if !mode.acquire(&r.whisperScopeMu) {
			return true, nil
		}
		defer r.whisperScopeMu.RUnlock()
		if !r.whisperAllowedLocked(delivery) {
			return false, nil
		}
	}
	if !delivery.Tap && watchSlot(delivery.Slot) != "" {
		if !mode.acquire(&r.watchMu) {
			return true, nil
		}
		defer r.watchMu.RUnlock()
		if !r.watchAllowedLocked(delivery) {
			return false, nil
		}
	}
	if delivery.sourceLive != nil && !delivery.sourceLive.Load() {
		return false, nil
	}
	return false, write()
}

func (r *Router) withMediaScope(delivery MediaDelivery, write func() error) error {
	return r.withEchoScope(delivery, func() error {
		return r.withWhisperScope(delivery, func() error {
			return r.withWatch(delivery, func() error {
				// This is an admission check, not a drain barrier: a write already entered
				// before retirement remains owned by its existing callback/egress worker.
				if delivery.sourceLive != nil && !delivery.sourceLive.Load() {
					return nil
				}
				return write()
			})
		})
	})
}
