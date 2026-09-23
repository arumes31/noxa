package webrtc

import "github.com/pion/rtp"

// MediaDelivery identifies the routing decision for one packet and recipient.
// The guard must validate both channel IDs against current membership. Tap
// recipients are recorder identities, not registered client sessions.
type MediaDelivery struct {
	SenderID, RecipientID         string
	ChannelID, RecipientChannelID int64
	Slot                          string
	Whisper, Tap                  bool
	SourceEpoch                   uint64
	WhisperRevision               uint64
	Codec                         string
	Publication, WatchEpoch       uint64
}

// MediaCommit rechecks the original delivery policy at the final write.
// Buffered consumers must call it after dequeueing, never retain its write
// callback, and bound the time spent in that callback.
type MediaCommit func(write func() error) error

// ScopedTrackWriter preserves source identity across an asynchronous tap.
// WriteMedia must copy any packet data it retains and return without blocking
// on process startup or storage. WriteRTP is unused for a scoped tap.
type ScopedTrackWriter interface {
	TrackWriter
	WriteMedia(*rtp.Packet, MediaDelivery, MediaCommit) error
}

func (r *Router) mediaCommit(delivery MediaDelivery, guard MediaGuard, videoRevision uint64) MediaCommit {
	return func(write func() error) error {
		commit := func() error {
			if delivery.Slot == SlotCam || delivery.Slot == SlotScreen {
				if !r.videoPolicyMu.TryRLock() {
					return nil
				}
				defer r.videoPolicyMu.RUnlock()
				if r.videoPolicy.revision != videoRevision {
					return nil
				}
			}
			return r.withWhisperScope(delivery, func() error { return r.withWatch(delivery, write) })
		}
		if guard != nil {
			return guard(delivery, commit)
		}
		return commit()
	}
}

// MediaGuard must synchronously authorize and execute write under the same
// policy revision. It runs outside the router lock, may decline to call write,
// and must never retain write or start asynchronous work with it.
type MediaGuard func(MediaDelivery, func() error) error

func (r *Router) SetMediaGuard(guard MediaGuard) {
	r.mu.Lock()
	r.mediaGuard = guard
	r.mu.Unlock()
}

func (v *Voice) SetMediaGuard(guard MediaGuard) { v.router.SetMediaGuard(guard) }

type mediaOutput struct {
	delivery MediaDelivery
	writer   TrackWriter
}

// mediaOutputLocked captures scope with routing, rather than guessing the scope
// later from a client that may have moved in between selection and delivery.
func (r *Router) mediaOutputLocked(sender, recipient, slot string, tap bool, writer TrackWriter) mediaOutput {
	whisper := slot == SlotMic && r.whispers[sender] != nil && r.whispers[sender].active
	delivery := MediaDelivery{SenderID: sender, RecipientID: recipient, ChannelID: r.clientChan[sender], RecipientChannelID: r.clientChan[recipient], Slot: slot, Whisper: whisper, Tap: tap, SourceEpoch: r.slotClaims[sender][slot].token, Codec: "audio/opus"}
	r.captureWatch(&delivery)
	if slot == SlotMic && r.whispers[sender] != nil {
		delivery.WhisperRevision = r.whispers[sender].revision
	}
	return mediaOutput{delivery, writer}
}
