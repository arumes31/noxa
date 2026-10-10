package webrtc

import "fmt"

// SetStreamVideoQuality changes one active watch, never another publication or
// the legacy default. Its storage shares the watch's bounded lifecycle.
func (r *Router) SetStreamVideoQuality(subscriber, publisher, slot string, generation, session uint64, quality string) error {
	rid, ok := qualityToRID[quality]
	if !ok && quality != "default" {
		return fmt.Errorf("invalid video quality %q (want high, mid, low, or default)", quality)
	}
	if (slot != SlotCam && slot != SlotScreen) || generation == 0 || session == 0 {
		return ErrVideoWatch
	}
	r.mu.RLock()
	channel := r.clientChan[subscriber]
	if channel <= 0 || channel != r.clientChan[publisher] || !r.publisherAllowedLocked(subscriber, publisher) {
		r.mu.RUnlock()
		return ErrVideoWatch
	}
	r.watchMu.Lock()
	key := watchKey{subscriber, publisher, slot}
	watch := r.watches[key]
	if !watch.active || watch.publication != generation || watch.session != session ||
		r.publications[publicationKey{publisher, slot}] != generation || r.watchSessions[subscriber] != session {
		r.watchMu.Unlock()
		r.mu.RUnlock()
		return ErrVideoWatch
	}
	if r.publicationModes[publicationKey{publisher, slot}] == "source" && (quality == "low" || quality == "mid") {
		r.watchMu.Unlock()
		r.mu.RUnlock()
		return fmt.Errorf("video quality is selected by the publisher")
	}
	changed := watch.qualityRID != rid
	watch.qualityRID = rid
	r.watches[key] = watch
	if rid == "" {
		rid = r.layerPrefLocked(subscriber)
	}
	r.watchMu.Unlock()
	r.mu.RUnlock()
	if changed {
		r.RequestKeyframe(publisher, slot, rid)
	}
	return nil
}

// streamLayerPrefLocked requires Router.mu. Watch lifetimes are protected by
// watchMu; missing, inactive and legacy watches use the subscriber's default.
func (r *Router) streamLayerPrefLocked(subscriber, publisher, slot string) string {
	r.watchMu.RLock()
	watch := r.watches[watchKey{subscriber, publisher, slot}]
	rid := ""
	if watch.active && watch.session == r.watchSessions[subscriber] &&
		watch.publication == r.publications[publicationKey{publisher, slot}] {
		rid = watch.qualityRID
	}
	r.watchMu.RUnlock()
	if rid != "" {
		return rid
	}
	return r.layerPrefLocked(subscriber)
}

func (v *Voice) SetStreamVideoQuality(subscriber, publisher, slot string, generation, session uint64, quality string) error {
	return v.router.SetStreamVideoQuality(subscriber, publisher, slot, generation, session, quality)
}
