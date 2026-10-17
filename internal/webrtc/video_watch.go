package webrtc

import (
	"errors"
	"sort"
	"time"
)

var ErrVideoPublication = errors.New("video publication is no longer available")
var ErrVideoWatch = errors.New("video watch request is stale or unavailable")

type publicationKey struct{ publisher, slot string }
type watchKey struct{ subscriber, publisher, slot string }
type videoWatch struct {
	publication, revision, epoch, session uint64
	active                                bool
	startupPending                        bool
	qualityRID                            string
	codecs                                uint8
	hevcLevel                             uint8
}

// VideoPublication is a current, explicitly announced camera or screen source.
// Listing a source does not authorize its media or preview delivery.
type VideoPublication struct {
	PublisherID   string `json:"publisher_id"`
	Slot          string `json:"slot"`
	Generation    uint64 `json:"generation"`
	PreviewAt     int64  `json:"preview_at"`
	WatchRevision uint64 `json:"watch_revision"`
	ViewerCount   *int   `json:"viewer_count,omitempty"`
	QualityMode   string `json:"quality_mode,omitempty"`
	UploadActive  *bool  `json:"upload_active,omitempty"`
	Codec         string `json:"codec,omitempty"`
	HEVCLevelID   uint8  `json:"hevc_level_id,omitempty"`
	CodecRevision uint64 `json:"codec_revision,string,omitempty"`
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
	return r.PublishVideoWithMode(publisher, slot, generation, active, "")
}

// PublishVideoWithMode pins the source's quality policy to its publication.
// Source mode publishes one sender-selected quality to every viewer.
func (r *Router) PublishVideoWithMode(publisher, slot string, generation uint64, active bool, mode string) (uint64, error) {
	return r.PublishVideoWithCodecs(publisher, slot, generation, active, mode, nil)
}

func (r *Router) PublishVideoWithCodecs(publisher, slot string, generation uint64, active bool, mode string, codecs []string) (uint64, error) {
	return r.PublishVideoWithFormats(publisher, slot, generation, active, mode, codecs, 0)
}
func (r *Router) PublishVideoWithFormats(publisher, slot string, generation uint64, active bool, mode string, codecs []string, hevcLevel uint8, revisions ...uint64) (uint64, error) {
	var codecRevision uint64
	if len(revisions) > 1 {
		return 0, ErrVideoPublication
	}
	if len(revisions) == 1 {
		codecRevision = revisions[0]
	}
	capabilities, valid := videoFormatCapabilities(codecs, hevcLevel)
	if !valid {
		return 0, ErrVideoPublication
	}
	if (slot != SlotCam && slot != SlotScreen) || (mode != "" && mode != "source") {
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
		storedRevision := r.publicationCodecRevisions[key]
		if codecRevision != 0 || storedRevision != 0 {
			if generation != current || mode != r.publicationModes[key] || codecRevision < storedRevision || codecRevision == 0 {
				return 0, ErrVideoPublication
			}
			if codecRevision == storedRevision {
				if capabilities != r.publicationCodecCapabilities[key] || hevcLevel != r.publicationHEVCLevels[key] {
					return 0, ErrVideoPublication
				}
				return current, nil
			}
			r.publicationCodecCapabilities[key] = capabilities
			if r.publicationHEVCLevels == nil {
				r.publicationHEVCLevels = make(map[publicationKey]uint8)
			}
			r.publicationHEVCLevels[key] = hevcLevel
			if r.publicationCodecRevisions == nil {
				r.publicationCodecRevisions = make(map[publicationKey]uint64)
			}
			r.publicationCodecRevisions[key] = codecRevision
			return current, nil
		}
		// A transient client capability query can initially publish VP8 only.
		// Add newly available codecs to that source lifetime without replacing watches,
		// preview, ingress diagnostics or transport. Selection still intersects
		// current viewers and the recording/dimension policies below.
		if generation == current && mode == "source" && r.publicationModes[key] == "source" &&
			capabilities|r.publicationCodecCapabilities[key] == capabilities &&
			(r.publicationHEVCLevels[key] == 0 || hevcLevel == r.publicationHEVCLevels[key]) {
			r.publicationCodecCapabilities[key] = capabilities
			if r.publicationHEVCLevels == nil {
				r.publicationHEVCLevels = make(map[publicationKey]uint8)
			}
			r.publicationHEVCLevels[key] = hevcLevel
			return current, nil
		}
		if generation == current && (mode == "" || mode == r.publicationModes[key]) && capabilities == r.publicationCodecCapabilities[key] && hevcLevel == r.publicationHEVCLevels[key] {
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
	if r.publicationCodecCapabilities == nil {
		r.publicationCodecCapabilities = make(map[publicationKey]uint8)
	}
	r.publicationCodecCapabilities[key] = capabilities
	if r.publicationHEVCLevels == nil {
		r.publicationHEVCLevels = make(map[publicationKey]uint8)
	}
	r.publicationHEVCLevels[key] = hevcLevel
	if r.publicationCodecRevisions == nil {
		r.publicationCodecRevisions = make(map[publicationKey]uint64)
	}
	r.publicationCodecRevisions[key] = codecRevision
	if mode != "" {
		if r.publicationModes == nil {
			r.publicationModes = make(map[publicationKey]string)
		}
		r.publicationModes[key] = mode
	}
	// A reused track can publish again without creating a new RTP reader.
	// Its diagnostic history must still start at this publication boundary.
	for source, input := range r.videoIngress {
		if source.publisher == publisher && source.slot == slot {
			input.mu.Lock()
			input.started = time.Now()
			input.stage = videoStageCounter{}
			input.h264Rejects = nil
			input.keyframeRequests = nil
			input.mu.Unlock()
		}
	}
	return r.watchEpoch, nil
}

// WatchVideo records explicit recipient intent. Revisions order requests within
// a publication; a new watch epoch invalidates every previously queued packet.
func (r *Router) WatchVideo(subscriber, publisher, slot string, generation, revision, session uint64, active bool) (started bool, err error) {
	return r.WatchVideoWithCodecs(subscriber, publisher, slot, generation, revision, session, active, nil)
}

func (r *Router) WatchVideoWithCodecs(subscriber, publisher, slot string, generation, revision, session uint64, active bool, codecs []string) (bool, error) {
	return r.WatchVideoWithFormats(subscriber, publisher, slot, generation, revision, session, active, codecs, 0)
}
func (r *Router) WatchVideoWithFormats(subscriber, publisher, slot string, generation, revision, session uint64, active bool, codecs []string, hevcLevel uint8) (started bool, err error) {
	capabilities, valid := videoFormatCapabilities(codecs, hevcLevel)
	if !valid {
		return false, ErrVideoWatch
	}
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
		(old.session == session && old.publication == generation && (revision < old.revision || revision == old.revision && (active != old.active || capabilities != old.codecs || hevcLevel != old.hevcLevel))) || r.watchEpoch == ^uint64(0) {
		r.watchMu.Unlock()
		r.mu.RUnlock()
		return false, ErrVideoWatch
	}
	if old.session != session || old.publication != generation || revision != old.revision {
		r.watchEpoch++
		if r.watches == nil {
			r.watches = make(map[watchKey]videoWatch)
		}
		qualityRID := ""
		if active && old.active && old.publication == generation && old.session == session {
			qualityRID = old.qualityRID
		}
		r.watches[key] = videoWatch{publication: generation, revision: revision, epoch: r.watchEpoch, session: session, active: active, startupPending: active, qualityRID: qualityRID, codecs: capabilities, hevcLevel: hevcLevel}
	}
	// A newer active revision is an explicit retry. Notify the publisher again
	// so paused screen capture can produce a fresh frame; exact retries remain
	// idempotent and stale revisions were rejected above.
	started = active && (!old.active || old.session != session || old.publication != generation || revision > old.revision)
	r.watchMu.Unlock()
	r.mu.RUnlock()
	r.syncSubscriberVideoCodec(subscriber, publisher, slot)
	if started {
		r.requestWatchStartup(subscriber)
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
	// Catalog polling normally reads an existing session. Do not queue a
	// writer behind packet leases: writer preference would make unrelated
	// final packet TryRLock checks fail even though no consent is changing.
	r.watchMu.RLock()
	session := r.watchSessions[subscriber]
	r.watchMu.RUnlock()
	if session != 0 {
		return session
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
			r.previewMu.Lock()
			if preview := r.previews[key]; !preview.captured.IsZero() {
				previewAt = preview.captured.UnixMilli()
			}
			r.previewMu.Unlock()
			revision := uint64(0)
			if watch := r.watches[watchKey{subscriber, key.publisher, key.slot}]; watch.session == r.watchSessions[subscriber] {
				revision = watch.revision
			}
			stream := VideoPublication{PublisherID: key.publisher, Slot: key.slot, Generation: generation, PreviewAt: previewAt, WatchRevision: revision, QualityMode: r.publicationModes[key]}
			// Only the publisher sees an aggregate count. Watcher identities,
			// including invisible members, never leave the router.
			if subscriber == key.publisher {
				count := 0
				for watcher, watch := range r.watches {
					if watcher.publisher == key.publisher && watcher.slot == key.slot && watcher.subscriber != subscriber &&
						watch.active && watch.publication == generation && watch.session == r.watchSessions[watcher.subscriber] &&
						r.clientChan[watcher.subscriber] == r.clientChan[subscriber] && r.publisherAllowedLocked(watcher.subscriber, key.publisher) {
						count++
					}
				}
				stream.ViewerCount = &count
				active := count > 0 || r.videoRecordingConsumerLocked(key)
				stream.UploadActive = &active
				stream.Codec = r.publicationCodecLocked(key, generation)
				stream.CodecRevision = r.publicationCodecRevisions[key]
				if stream.Codec == "h265" {
					stream.HEVCLevelID = r.publicationHEVCLevels[key]
				}
			}
			list = append(list, stream)
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
	delete(r.publicationModes, key)
	delete(r.publicationCodecCapabilities, key)
	delete(r.publicationHEVCLevels, key)
	delete(r.publicationCodecRevisions, key)
	r.previewMu.Lock()
	delete(r.previews, key)
	r.previewMu.Unlock()
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

// withWatchWait is withWatch for the ingress dispatcher. Skipping a packet
// there leaves a sequence hole no retransmission can fill (the packet was
// never sent) and costs every viewer a keyframe; wait for the short update.
// The dispatcher holds no router, watch or egress lock at this point.
func (r *Router) withWatchWait(d MediaDelivery, write func() error) error {
	if d.Tap || watchSlot(d.Slot) == "" {
		return write()
	}
	r.watchMu.RLock()
	defer r.watchMu.RUnlock()
	if !r.watchAllowedLocked(d) {
		return nil
	}
	return write()
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
func (v *Voice) PublishVideoWithMode(publisher, slot string, generation uint64, active bool, mode string) (uint64, error) {
	return v.router.PublishVideoWithMode(publisher, slot, generation, active, mode)
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
