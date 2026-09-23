package webrtc

import "maps"

type whisperScope struct {
	active   bool
	revision uint64
	targets  map[string]bool
}

// Control paths hold Router.mu before this gate. Final writes take only the
// gate, after authorization; they never acquire Router.mu from inside Pion.
func (r *Router) refreshWhisperScopesLocked() {
	r.whisperScopeMu.Lock()
	defer r.whisperScopeMu.Unlock()
	if r.whisperScopes == nil {
		r.whisperScopes = make(map[string]whisperScope)
	}
	for sender := range r.whisperScopes {
		if r.whispers[sender] == nil {
			delete(r.whisperScopes, sender)
		}
	}
	for sender, cfg := range r.whispers {
		targets := map[string]bool{}
		if cfg.active {
			for recipient := range r.whisperTargetsLocked(sender, cfg) {
				if r.clientChan[recipient] > 0 {
					targets[recipient] = true
				}
			}
		}
		previous, found := r.whisperScopes[sender]
		if found && previous.active == cfg.active && maps.Equal(previous.targets, targets) {
			cfg.revision = previous.revision
			continue
		}
		r.whisperScopeEpoch++
		cfg.revision = r.whisperScopeEpoch
		r.whisperScopes[sender] = whisperScope{active: cfg.active, revision: cfg.revision, targets: targets}
	}
}

func (r *Router) withWhisperScope(delivery MediaDelivery, write func() error) error {
	if delivery.Slot != SlotMic {
		return write()
	}
	if !r.whisperScopeMu.TryRLock() {
		return nil
	}
	defer r.whisperScopeMu.RUnlock()
	scope := r.whisperScopes[delivery.SenderID]
	if scope.revision != delivery.WhisperRevision || scope.active != delivery.Whisper {
		return nil
	}
	// A channel recording is not a whisper recipient. Neither a queued tap
	// nor a retransmission may retain access after the recipient set changes.
	if scope.active && (delivery.Tap || !scope.targets[delivery.RecipientID]) {
		return nil
	}
	return write()
}
