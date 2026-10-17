package webrtc

import (
	"time"

	pion "github.com/pion/webrtc/v4"
)

// Coalesce readiness notifications outside Pion sender locks. A worker exists
// only while processing notifications or a finite shared-throttle deadline.
// It never polls watches or occupies the remote ICE candidate consumer.
func (w *PeerConnectionWrapper) notifyVideoStartup() {
	w.videoStartupMu.Lock()
	defer w.videoStartupMu.Unlock()
	select {
	case <-w.closed:
		return
	default:
	}
	if w.videoStartupHandler == nil || w.videoStartupWake == nil {
		return
	}
	select {
	case w.videoStartupWake <- struct{}{}:
	default:
	}
	if !w.videoStartupRun {
		w.videoStartupRun = true
		w.videoStartupDone = make(chan struct{})
		go w.runVideoStartup(w.videoStartupDone)
	}
}

func (w *PeerConnectionWrapper) runVideoStartup(done chan struct{}) {
	for {
		w.videoStartupMu.Lock()
		select {
		case <-w.closed:
			w.videoStartupRun = false
			close(done)
			w.videoStartupMu.Unlock()
			return
		default:
		}
		handler := w.videoStartupHandler
		select {
		case <-w.videoStartupWake:
		default:
		}
		w.videoStartupMu.Unlock()
		delay := handler()
		w.videoStartupMu.Lock()
		select {
		case <-w.closed:
			w.videoStartupRun = false
			close(done)
			w.videoStartupMu.Unlock()
			return
		default:
		}
		select {
		case <-w.videoStartupWake:
			w.videoStartupMu.Unlock()
			continue
		default:
		}
		if delay <= 0 {
			w.videoStartupRun = false
			close(done)
			w.videoStartupMu.Unlock()
			return
		}
		w.videoStartupMu.Unlock()
		timer := time.NewTimer(delay)
		select {
		case <-w.closed:
		case <-w.videoStartupWake:
		case <-timer.C:
		}
		timer.Stop()
	}
}

func (r *Router) requestWatchStartup(subscriber string) {
	r.mu.RLock()
	peer := r.pubPeers[subscriber]
	r.mu.RUnlock()
	if peer == nil {
		return
	}
	// Preserve the immediate ready path; later binding/connection notifications
	// revisit pending watches even when a static publisher produces no packets.
	if r.flushWatchStartup(subscriber, peer) > 0 {
		peer.notifyVideoStartup()
	}
}

// BindLocalStream runs inside Pion Send, before the interceptor writer is
// published. GetParameters takes the same sender lock: after observing a pacer
// binding it provides the completion barrier. Never wait for it under Router or
// watch locks, or from the interceptor callback itself. Outputs are immutable;
// retiring/replacing one deactivates its egress and normal scope checks reject it.
func (r *Router) finishVideoStartupBindings(subscriber string, peer *PeerConnectionWrapper) {
	var outputs []*pubSlot
	r.mu.RLock()
	if r.pubPeers[subscriber] == peer {
		for _, tracks := range r.pubTracks[subscriber] {
			for _, output := range tracks.video {
				if output.egress != nil && output.sender != nil && !output.videoBindingReady.Load() && output.videoPacerBound() {
					outputs = append(outputs, output)
				}
			}
		}
	}
	r.mu.RUnlock()
	for _, output := range outputs {
		select {
		case <-peer.closed:
			return
		default:
		}
		_ = output.sender.GetParameters()
		if output.videoPacerBound() {
			output.videoBindingReady.Store(true)
		}
	}
}

func (r *Router) watchStartupPending(subscriber string) bool {
	r.watchMu.RLock()
	defer r.watchMu.RUnlock()
	for key, watch := range r.watches {
		if key.subscriber == subscriber && watch.startupPending {
			return true
		}
	}
	return false
}

func (r *Router) flushWatchStartup(subscriber string, peer *PeerConnectionWrapper) time.Duration {
	r.finishVideoStartupBindings(subscriber, peer)
	type request struct {
		publisher string
		ssrc      uint32
		writer    RTCPWriter
		input     *videoIngressDiagnostic
		epoch     time.Time
	}
	var requests []request
	var retry time.Duration
	select {
	case <-peer.closed:
		return 0
	default:
	}
	// Binding and connection notifications arrive for every renegotiation of
	// every recipient. Without a pending startup there is nothing to claim;
	// a needless watch writer would make concurrent final media writes of all
	// recipients fail their non-blocking scope check.
	if !r.watchStartupPending(subscriber) {
		return 0
	}
	r.mu.Lock()
	r.watchMu.Lock()
	now := time.Now()
	if r.pubPeers[subscriber] != peer {
		r.watchMu.Unlock()
		r.mu.Unlock()
		return 0
	}
	for key, watch := range r.watches {
		if key.subscriber != subscriber || !watch.startupPending {
			continue
		}
		if !watch.active || watch.session != r.watchSessions[subscriber] || watch.publication != r.publications[publicationKey{key.publisher, key.slot}] || r.clientChan[subscriber] <= 0 || r.clientChan[subscriber] != r.clientChan[key.publisher] || !r.publisherAllowedLocked(subscriber, key.publisher) {
			continue
		}

		tracks := r.pubTracks[subscriber][key.publisher]
		if tracks != nil && tracks.video[key.slot] != nil && tracks.video[key.slot].videoStartupEpoch.Load() == watch.epoch {
			watch.startupPending = false
			r.watches[key] = watch
			continue
		}
		// Direct router outputs have no network egress. Managed peers must have both
		// the current video sender binding and the connected ICE/DTLS transport.
		if peer.egress != nil {
			if peer.pc == nil || peer.pc.ConnectionState() != pion.PeerConnectionStateConnected || tracks == nil || tracks.video[key.slot] == nil || tracks.video[key.slot].egress == nil || !tracks.video[key.slot].videoOutputBound() {
				continue
			}
		}
		writer := r.rtcpWriters[key.publisher]

		rid := ""
		ssrc := r.videoSources[key.publisher][key.slot][rid]
		if ssrc == 0 {
			pref := watch.qualityRID
			if pref == "" {
				pref = r.layerPrefLocked(subscriber)
			}
			for _, candidate := range layerFallback(pref) {
				if source := r.videoSources[key.publisher][key.slot][candidate]; source != 0 {
					rid, ssrc = candidate, source
					break
				}
			}
		}
		// No known source can be requested. Initial publisher activation and the
		// existing first-delta recovery remain responsible for this case.
		if writer == nil || ssrc == 0 {
			watch.startupPending = false
			r.watches[key] = watch
			continue
		}
		input := r.videoIngress[videoIngressKey{key.publisher, key.slot, rid}]
		if input != nil && input.ssrc != ssrc {
			input = nil
		}
		var epoch time.Time
		if input != nil {
			input.mu.Lock()
			epoch = input.started
			input.mu.Unlock()
		}
		// A live source with a fresh cached keyframe starts this viewer on its
		// next unit without disturbing the publisher. If that has not happened
		// within gopRefillTimeout (source paused, admission failed), ask upstream.
		if peer.egress != nil && tracks != nil && tracks.video[key.slot] != nil &&
			r.gopCacheLocked(key.publisher, key.slot, rid).servable(now, tracks.video[key.slot].track.Codec().MimeType) {
			if watch.cacheStart.IsZero() {
				watch.cacheStart = now
				r.watches[key] = watch
			}
			if waited := now.Sub(watch.cacheStart); waited < gopRefillTimeout {
				if delay := gopRefillTimeout - waited; retry == 0 || delay < retry {
					retry = delay
				}
				continue
			}
		}
		input.recordKeyframeRequest(keyframeWatchStart, keyframeRequested, &epoch)
		if delay, ok := r.claimKeyframeRequest(ssrc, now); !ok {
			input.recordKeyframeRequest(keyframeWatchStart, keyframeCoalesced, &epoch)
			if retry == 0 || delay < retry {
				retry = delay
			}
			continue
		}
		watch.startupPending = false
		r.watches[key] = watch
		requests = append(requests, request{key.publisher, ssrc, writer, input, epoch})
	}
	r.watchMu.Unlock()
	r.mu.Unlock()
	for _, request := range requests {
		select {
		case <-peer.closed:
			return 0
		default:
		}
		r.writeKeyframeRequest(request.writer, request.publisher, request.ssrc, request.input, keyframeWatchStart, request.epoch)
	}
	return retry
}
