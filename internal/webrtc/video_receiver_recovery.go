package webrtc

import (
	"fmt"
	"time"

	pion "github.com/pion/webrtc/v4"
)

const videoReceiverRecoveryInterval = 30 * time.Second
const videoReceiverRecoveryLimit = 8

// ResetStreamReceiver replaces one authorized outgoing video binding. Pion's
// inactive-offer handling permanently stops transceivers, so decoder recovery
// uses a fresh sender through normal PeerConnection APIs, never private API
// state or an inactive/recvonly SDP cycle. Audio and the transport stay intact.
func (r *Router) ResetStreamReceiver(subscriber, publisher, slot string, generation, session, revision uint64) error {
	if (slot != SlotCam && slot != SlotScreen) || generation == 0 || session == 0 || revision == 0 {
		return ErrVideoWatch
	}
	r.mu.RLock()
	peer := r.pubPeers[subscriber]
	r.mu.RUnlock()
	if peer == nil {
		return ErrVideoWatch
	}
	peer.signalMu.Lock()
	changed, hook, err := r.resetStreamReceiverLocked(peer, subscriber, publisher, slot, generation, session, revision)
	peer.signalMu.Unlock()
	if changed && hook != nil {
		hook(subscriber)
	}
	return err
}

// signalMu serializes recovery with offer/answer construction. Router.mu pins
// membership, publication and watch ownership through output replacement.
func (r *Router) resetStreamReceiverLocked(peer *PeerConnectionWrapper, subscriber, publisher, slot string, generation, session, revision uint64) (bool, func(string), error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	channel := r.clientChan[subscriber]
	if r.pubPeers[subscriber] != peer || channel <= 0 || channel != r.clientChan[publisher] || !r.publisherAllowedLocked(subscriber, publisher) {
		return false, nil, ErrVideoWatch
	}
	r.watchMu.RLock()
	watch := r.watches[watchKey{subscriber, publisher, slot}]
	allowed := watch.active && watch.publication == generation && watch.session == session && watch.revision == revision &&
		r.publications[publicationKey{publisher, slot}] == generation && r.watchSessions[subscriber] == session
	r.watchMu.RUnlock()
	if !allowed {
		return false, nil, ErrVideoWatch
	}
	publication := r.pubTracks[subscriber][publisher]
	if publication == nil || publication.video[slot] == nil || peer.pc.SignalingState() != pion.SignalingStateStable {
		return false, nil, ErrVideoWatch
	}
	output := publication.video[slot]
	if output.sender == nil || output.sender.Track() == nil {
		return false, nil, fmt.Errorf("video receiver is no longer recoverable on this connection")
	}
	bound := false
	for _, transceiver := range peer.pc.GetTransceivers() {
		if transceiver.Sender() == output.sender && transceiver.Mid() != "" &&
			(transceiver.Direction() == pion.RTPTransceiverDirectionSendonly || transceiver.Direction() == pion.RTPTransceiverDirectionSendrecv) {
			bound = true
			break
		}
	}
	if !bound {
		return false, nil, fmt.Errorf("video receiver has no active negotiated output")
	}
	// A finite per-peer budget bounds transceiver growth even when clients
	// replace watch/publication generations to evade a per-watch limit.
	now := time.Now()
	if peer.videoRecoveryCount >= videoReceiverRecoveryLimit || (!peer.videoRecoveryLast.IsZero() && now.Sub(peer.videoRecoveryLast) < videoReceiverRecoveryInterval) {
		return false, nil, fmt.Errorf("video receiver recovery limit reached")
	}
	peer.videoRecoveryCount++
	peer.videoRecoveryLast = now
	r.removeSlotLocked(peer, publication, slot)
	if !r.addSlotLocked(peer, subscriber, publisher, publication, slot) {
		// The old sender has already been retired. Negotiate that change even
		// on failure rather than leaving the client with stale send metadata.
		return true, r.onRenegotiate, fmt.Errorf("video receiver recovery could not create output")
	}
	return true, r.onRenegotiate, nil
}

func (v *Voice) ResetStreamReceiver(subscriber, publisher, slot string, generation, session, revision uint64) error {
	return v.router.ResetStreamReceiver(subscriber, publisher, slot, generation, session, revision)
}
