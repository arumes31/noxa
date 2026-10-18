package webrtc

import (
	"strings"

	"github.com/pion/webrtc/v4"
)

// setVideoCodec replaces subscriber bindings when an owned publication changes
// codec. The sender still publishes exactly one encoding; the SFU only forwards.
// A fresh binding has a fresh continuity epoch and waits for a complete keyframe.
func (r *Router) setVideoCodec(clientID, slot, rid string, token uint64, codec webrtc.RTPCodecCapability) bool {
	if strings.EqualFold(codec.MimeType, webrtc.MimeTypeH264) && !admittedH264Codec(codec) {
		return false
	}
	if !strings.EqualFold(codec.MimeType, webrtc.MimeTypeVP8) && !strings.EqualFold(codec.MimeType, webrtc.MimeTypeH264) && !strings.EqualFold(codec.MimeType, webrtc.MimeTypeH265) {
		return true
	}
	// Every forwarded packet passes here. The unchanged-codec case only reads
	// router state; take the exclusive lock only to rebind on a real change.
	if admitted, unchanged := r.currentVideoCodec(clientID, slot, rid, token, codec); unchanged {
		return admitted
	}
	r.mu.Lock()
	if r.slotClaims[clientID][slot+slotSep+rid].token != token {
		r.mu.Unlock()
		return false
	}
	if strings.EqualFold(codec.MimeType, webrtc.MimeTypeH265) {
		level, valid := admittedH265Level(codec)
		r.watchMu.RLock()
		key := publicationKey{clientID, slot}
		admitted := valid && r.publicationCodecCapabilities[key]&videoCodecH265 != 0 && r.publicationHEVCLevels[key] == level
		r.watchMu.RUnlock()
		if !admitted {
			r.mu.Unlock()
			return false
		}
	}
	ref := videoSourceRef{clientID, slot}
	previous, exists := r.videoCodecs[ref]
	if exists && sameVideoCodec(previous, codec) {
		r.mu.Unlock()
		return true
	}
	// A slot cannot interleave different codecs from simultaneous source layers.
	for layer := range r.videoSources[clientID][slot] {
		if layer != rid && exists {
			r.mu.Unlock()
			return false
		}
	}
	if r.videoCodecs == nil {
		r.videoCodecs = make(map[videoSourceRef]webrtc.RTPCodecCapability)
	}
	r.videoCodecs[ref] = codec
	var changed []string
	r.beginDeferredTrackRemovalsLocked()
	for sub := range r.pubTracks {
		if r.syncSubscriberVideoCodecLocked(sub, clientID, slot) {
			changed = append(changed, sub)
		}
	}
	hook := r.onRenegotiate
	r.unlockFlushingTrackRemovals()
	if hook != nil {
		for _, sub := range changed {
			hook(sub)
		}
	}
	return true
}

// currentVideoCodec answers setVideoCodec under shared locks when the source
// keeps its bound codec. unchanged=false defers to the exclusive path.
func (r *Router) currentVideoCodec(clientID, slot, rid string, token uint64, codec webrtc.RTPCodecCapability) (admitted, unchanged bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.slotClaims[clientID][slot+slotSep+rid].token != token {
		return false, true
	}
	previous, exists := r.videoCodecs[videoSourceRef{clientID, slot}]
	if !exists || !sameVideoCodec(previous, codec) {
		return false, false
	}
	if strings.EqualFold(codec.MimeType, webrtc.MimeTypeH265) {
		level, valid := admittedH265Level(codec)
		r.watchMu.RLock()
		key := publicationKey{clientID, slot}
		admitted := valid && r.publicationCodecCapabilities[key]&videoCodecH265 != 0 && r.publicationHEVCLevels[key] == level
		r.watchMu.RUnlock()
		return admitted, true
	}
	return true, true
}

// Unknown receivers must be able to finish their initial voice negotiation.
// Upgrade their output binding only after a current watch declares the format.
// Router.mu is held; acquire watchMu in the established control-lock order.
func (r *Router) subscriberVideoCodecLocked(subscriber, publisher, slot string) webrtc.RTPCodecCapability {
	codec, exists := r.videoCodecs[videoSourceRef{publisher, slot}]
	fallback := webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8, ClockRate: 90000}
	if !exists {
		return fallback
	}
	if strings.EqualFold(codec.MimeType, webrtc.MimeTypeH264) {
		if !admittedH264Codec(codec) {
			return fallback
		}
		r.watchMu.RLock()
		watch := r.watches[watchKey{subscriber, publisher, slot}]
		supported := watch.session != 0 && watch.session == r.watchSessions[subscriber] && watch.codecs&videoCodecH264 != 0
		r.watchMu.RUnlock()
		if !supported {
			return fallback
		}
	}
	if strings.EqualFold(codec.MimeType, webrtc.MimeTypeH265) {
		level, valid := admittedH265Level(codec)
		r.watchMu.RLock()
		watch := r.watches[watchKey{subscriber, publisher, slot}]
		supported := valid && watch.active && watch.session != 0 && watch.session == r.watchSessions[subscriber] && watch.publication == r.publications[publicationKey{publisher, slot}] && watch.codecs&videoCodecH265 != 0 && watch.hevcLevel >= level
		r.watchMu.RUnlock()
		if !supported {
			return fallback
		}
	}
	return codec
}

func (r *Router) syncSubscriberVideoCodecLocked(subscriber, publisher, slot string) bool {
	publication := r.pubTracks[subscriber][publisher]
	pc := r.pubPeers[subscriber]
	if pc == nil || publication == nil || publication.video[slot] == nil || sameVideoCodec(publication.video[slot].track.Codec(), r.subscriberVideoCodecLocked(subscriber, publisher, slot)) {
		return false
	}
	r.removeSlotLocked(pc, publication, slot)
	r.addSlotLocked(pc, subscriber, publisher, publication, slot)
	return true
}

func (r *Router) syncSubscriberVideoCodec(subscriber, publisher, slot string) {
	r.mu.Lock()
	r.beginDeferredTrackRemovalsLocked()
	changed := r.syncSubscriberVideoCodecLocked(subscriber, publisher, slot)
	hook := r.onRenegotiate
	r.unlockFlushingTrackRemovals()
	if changed && hook != nil {
		hook(subscriber)
	}
}

func sameVideoCodec(a, b webrtc.RTPCodecCapability) bool {
	return strings.EqualFold(a.MimeType, b.MimeType) && a.ClockRate == b.ClockRate && a.SDPFmtpLine == b.SDPFmtpLine
}
