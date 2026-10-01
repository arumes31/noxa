package webrtc

import (
	"errors"
	"sort"
)

var ErrVideoPublication = errors.New("video publication is no longer available")
var ErrVideoWatch = errors.New("video watch request is stale or unavailable")

type publicationKey struct{ publisher, slot string }
type watchKey struct{ subscriber, publisher, slot string }
type videoWatch struct {
	publication, revision, epoch, session uint64
	active                                bool
}

// VideoPublication is a current, explicitly announced camera or screen source.
// Listing a source does not authorize its media or preview delivery.
type VideoPublication struct {
	PublisherID   string `json:"publisher_id"`
	Slot          string `json:"slot"`
	Generation    uint64 `json:"generation"`
	PreviewAt     int64  `json:"preview_at"`
	WatchRevision uint64 `json:"watch_revision"`
}

func watchSlot(slot string) string {
	if slot == SlotScreenAudio {
		return SlotScreen
	}
	if slot == SlotScreen || slot == SlotCam {
		return slot
	}
	return ""
}

// PublishVideo is called under the server's publication authorization lease.
// Stops must identify the exact publication; retries cannot stop a replacement.
func (r *Router) PublishVideo(publisher, slot string, generation uint64, active bool) (uint64, error) {
	if slot != SlotCam && slot != SlotScreen {
		return 0, ErrVideoPublication
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	r.watchMu.Lock()
	defer r.watchMu.Unlock()
	key := publicationKey{publisher, slot}
	current := r.publications[key]
	if !active {
		if generation == 0 || current != generation {
			return 0, ErrVideoPublication
		}
		r.removePublicationLocked(key)
		return generation, nil
	}
	if r.clientChan[publisher] <= 0 {
		return 0, ErrVideoPublication
	}
	if current != 0 {
		if generation == current {
			return current, nil
		}
		return 0, ErrVideoPublication
	}
	if generation != 0 || r.watchEpoch == ^uint64(0) {
		return 0, ErrVideoPublication
	}
	r.watchEpoch++
	if r.publications == nil {
		r.publications = make(map[publicationKey]uint64)
	}
	r.publications[key] = r.watchEpoch
	return r.watchEpoch, nil
}

// WatchVideo records explicit recipient intent. Revisions order requests within
// a publication; a new watch epoch invalidates every previously queued packet.
func (r *Router) WatchVideo(subscriber, publisher, slot string, generation, revision, session uint64, active bool) (started bool, err error) {
	if slot != SlotCam && slot != SlotScreen || generation == 0 || revision == 0 {
		return false, ErrVideoWatch
	}
	r.mu.RLock()
	channel := r.clientChan[subscriber]
	if channel <= 0 || channel != r.clientChan[publisher] || !r.publisherAllowedLocked(subscriber, publisher) {
		r.mu.RUnlock()
		return false, ErrVideoWatch
	}
	r.watchMu.Lock()
	key := watchKey{subscriber, publisher, slot}
	old := r.watches[key]
	if session == 0 || r.watchSessions[subscriber] != session || r.publications[publicationKey{publisher, slot}] != generation ||
		(old.session == session && old.publication == generation && (revision < old.revision || revision == old.revision && active != old.active)) || r.watchEpoch == ^uint64(0) {
		r.watchMu.Unlock()
		r.mu.RUnlock()
		return false, ErrVideoWatch
	}
	if old.session != session || old.publication != generation || revision != old.revision {
		r.watchEpoch++
		if r.watches == nil {
			r.watches = make(map[watchKey]videoWatch)
		}
		r.watches[key] = videoWatch{generation, revision, r.watchEpoch, session, active}
	}
	started = active && (!old.active || old.session != session || old.publication != generation)
	r.watchMu.Unlock()
	r.mu.RUnlock()
	if active {
		r.RequestKeyframe(publisher, slot, "")
	}
	return started, nil
}

// VideoWatchSession binds controls to the current recipient peer lifecycle.
func (r *Router) VideoWatchSession(subscriber string) uint64 {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.clientChan[subscriber] <= 0 {
		return 0
	}
	r.watchMu.Lock()
	defer r.watchMu.Unlock()
	if r.watchSessions[subscriber] == 0 && r.watchEpoch != ^uint64(0) {
		r.watchEpoch++
		if r.watchSessions == nil {
			r.watchSessions = make(map[string]uint64)
		}
		r.watchSessions[subscriber] = r.watchEpoch
	}
	return r.watchSessions[subscriber]
}

// VideoPublications returns only sources visible to this channel member.
func (r *Router) VideoPublications(subscriber string) []VideoPublication {
	r.mu.RLock()
	defer r.mu.RUnlock()
	r.watchMu.RLock()
	defer r.watchMu.RUnlock()
	list := []VideoPublication{}
	for key, generation := range r.publications {
		if r.clientChan[subscriber] > 0 && r.clientChan[subscriber] == r.clientChan[key.publisher] && r.publisherAllowedLocked(subscriber, key.publisher) {
			previewAt := int64(0)
			if preview := r.previews[key]; !preview.captured.IsZero() {
				previewAt = preview.captured.UnixMilli()
			}
			revision := uint64(0)
			if watch := r.watches[watchKey{subscriber, key.publisher, key.slot}]; watch.session == r.watchSessions[subscriber] {
				revision = watch.revision
			}
			list = append(list, VideoPublication{key.publisher, key.slot, generation, previewAt, revision})
		}
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].PublisherID != list[j].PublisherID {
			return list[i].PublisherID < list[j].PublisherID
		}
		return list[i].Slot < list[j].Slot
	})
	return list
}

func (r *Router) removePublicationLocked(key publicationKey) {
	delete(r.publications, key)
	delete(r.previews, key)
	for watch := range r.watches {
		if watch.publisher == key.publisher && watch.slot == key.slot {
			delete(r.watches, watch)
		}
	}
}

// invalidateVideoLocked is called with Router.mu held, including peer rebuilds.
func (r *Router) invalidateVideoLocked(client string) {
	r.watchMu.Lock()
	defer r.watchMu.Unlock()
	delete(r.watchSessions, client)
	for key := range r.publications {
		if key.publisher == client {
			r.removePublicationLocked(key)
		}
	}
	for key := range r.watches {
		if key.subscriber == client {
			// The invalidated receiver session rejects every delayed request;
			// unlike visibility pruning, it needs no revision tombstone.
			delete(r.watches, key)
		}
	}
}

// RevokeVideo invalidates a slot when publishing permission is removed. A later
// grant requires a new publication and fresh viewer consent.
func (r *Router) RevokeVideo(publisher, slot string) {
	r.watchMu.Lock()
	defer r.watchMu.Unlock()
	r.removePublicationLocked(publicationKey{publisher, slot})
}

func (r *Router) captureWatch(delivery *MediaDelivery) {
	if delivery.Tap || watchSlot(delivery.Slot) == "" {
		return
	}
	r.watchMu.RLock()
	defer r.watchMu.RUnlock()
	watch := r.watches[watchKey{delivery.RecipientID, delivery.SenderID, watchSlot(delivery.Slot)}]
	if watch.active {
		delivery.Publication, delivery.WatchEpoch = watch.publication, watch.epoch
	}
}

func (r *Router) watchAllowedLocked(d MediaDelivery) bool {
	if d.Tap || watchSlot(d.Slot) == "" {
		return true
	}
	key := watchKey{d.RecipientID, d.SenderID, watchSlot(d.Slot)}
	watch := r.watches[key]
	return d.Publication != 0 && d.WatchEpoch != 0 && watch.active &&
		watch.publication == d.Publication && watch.epoch == d.WatchEpoch &&
		r.publications[publicationKey{d.SenderID, key.slot}] == d.Publication
}

func (r *Router) withWatch(d MediaDelivery, write func() error) error {
	if d.Tap || watchSlot(d.Slot) == "" {
		return write()
	}
	if !r.watchMu.TryRLock() {
		return nil
	}
	defer r.watchMu.RUnlock()
	if !r.watchAllowedLocked(d) {
		return nil
	}
	return write()
}

func (v *Voice) PublishVideo(publisher, slot string, generation uint64, active bool) (uint64, error) {
	return v.router.PublishVideo(publisher, slot, generation, active)
}
func (v *Voice) WatchVideo(subscriber, publisher, slot string, generation, revision, session uint64, active bool) (started bool, err error) {
	return v.router.WatchVideo(subscriber, publisher, slot, generation, revision, session, active)
}
func (v *Voice) VideoWatchSession(subscriber string) uint64 {
	return v.router.VideoWatchSession(subscriber)
}
func (v *Voice) VideoPublications(subscriber string) []VideoPublication {
	return v.router.VideoPublications(subscriber)
}
func (v *Voice) RevokeVideo(publisher, slot string) { v.router.RevokeVideo(publisher, slot) }
