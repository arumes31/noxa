package webrtc

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/webrtc/v4"
)

// Voice must never wait for Router.mu. Control paths hold it exclusively while
// they add or remove Pion tracks (AddTrack/RemoveTrack can wait for an
// in-progress SetRemoteDescription on the same connection), and Go's RWMutex
// also blocks new readers while such a writer is merely queued. Every
// microphone packet used to take Router.mu up to five times, so one
// renegotiation stalled voice for everyone in the process for 10-80 ms.
//
// The voice hot path therefore reads its callbacks from atomics, checks slot
// ownership through the claim's live flag, and, when Router.mu is busy,
// reuses the fan-out it computed at most voiceFanoutReuse ago. Reuse is safe:
// every delivery still crosses the media guard (permissions/membership), the
// echo/whisper/watch final-write gates (which compare the epochs captured in
// the delivery), and the per-output egress lifecycle. A stale entry can only
// be dropped by those gates, never misdelivered.
const voiceFanoutReuse = 250 * time.Millisecond

type voiceHandlers struct {
	canTalk    func(clientID string) bool
	onSpeaking func(clientID string, speaking bool)
	onForward  func(media string, n int)
}

type voiceFanout struct {
	at      time.Time
	guard   MediaGuard
	writers []mediaOutput // read-only once published
}

type routerVoicePath struct {
	handlers atomic.Pointer[voiceHandlers]
	fanouts  sync.Map // sender + slotSep + slot -> *voiceFanout
}

// publishVoiceHandlersLocked mirrors the handler fields. Callers hold r.mu.
func (r *Router) publishVoiceHandlersLocked() {
	r.voicePath.handlers.Store(&voiceHandlers{canTalk: r.canTalk, onSpeaking: r.onSpeaking, onForward: r.onForward})
}

func (r *Router) voiceHandlers() voiceHandlers {
	if h := r.voicePath.handlers.Load(); h != nil {
		return *h
	}
	return voiceHandlers{}
}

// voiceFanoutFor returns the guard and outputs for one voice packet.
func (r *Router) voiceFanoutFor(senderID, slot string) (MediaGuard, []mediaOutput) {
	key := senderID + slotSep + slot
	if !r.mu.TryRLock() {
		if cached, ok := r.voicePath.fanouts.Load(key); ok {
			if f := cached.(*voiceFanout); time.Since(f.at) < voiceFanoutReuse {
				return f.guard, f.writers
			}
		}
		r.mu.RLock()
	}
	guard, writers := r.voiceFanoutLocked(senderID, slot)
	r.mu.RUnlock()
	r.voicePath.fanouts.Store(key, &voiceFanout{at: time.Now(), guard: guard, writers: writers})
	return guard, writers
}

// forgetVoiceFanoutLocked drops cached routes from a detached sender so its
// outputs are not retained. Callers hold r.mu exclusively.
func (r *Router) forgetVoiceFanoutLocked(senderID string) {
	for slot, kind := range slotKinds {
		if kind == webrtc.RTPCodecTypeAudio {
			r.voicePath.fanouts.Delete(senderID + slotSep + slot)
		}
	}
}

// slotLive returns the live flag of the claim identified by token, or nil.
func (r *Router) slotLive(clientID, key string, token uint64) *atomic.Bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if claim := r.slotClaims[clientID][key]; claim.token == token {
		return claim.live
	}
	return nil
}
