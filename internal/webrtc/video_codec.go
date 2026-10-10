package webrtc

import (
	"strings"

	"github.com/pion/webrtc/v4"
)

// setVideoCodec replaces subscriber bindings when an owned publication changes
// codec. The sender still publishes exactly one encoding; the SFU only forwards.
// A fresh binding has a fresh continuity epoch and waits for a complete keyframe.
func (r *Router) setVideoCodec(clientID, slot, rid string, token uint64, codec webrtc.RTPCodecCapability) bool {
	if !strings.EqualFold(codec.MimeType, webrtc.MimeTypeVP8) && !strings.EqualFold(codec.MimeType, webrtc.MimeTypeH264) {
		return true
	}
	r.mu.Lock()
	if r.slotClaims[clientID][slot+slotSep+rid].token != token {
		r.mu.Unlock()
		return false
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
	for sub := range r.pubTracks {
		if r.syncSubscriberVideoCodecLocked(sub, clientID, slot) {
			changed = append(changed, sub)
		}
	}
	hook := r.onRenegotiate
	r.mu.Unlock()
	if hook != nil {
		for _, sub := range changed {
			hook(sub)
		}
	}
	return true
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
		r.watchMu.RLock()
		watch := r.watches[watchKey{subscriber, publisher, slot}]
		supported := watch.session != 0 && watch.session == r.watchSessions[subscriber] && watch.codecs&videoCodecH264 != 0
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
	changed := r.syncSubscriberVideoCodecLocked(subscriber, publisher, slot)
	hook := r.onRenegotiate
	r.mu.Unlock()
	if changed && hook != nil {
		hook(subscriber)
	}
}

func sameVideoCodec(a, b webrtc.RTPCodecCapability) bool {
	return strings.EqualFold(a.MimeType, b.MimeType) && a.ClockRate == b.ClockRate && a.SDPFmtpLine == b.SDPFmtpLine
}
