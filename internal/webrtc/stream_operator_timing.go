package webrtc

import (
	"time"

	"github.com/pion/interceptor"
)

func (o *upstreamFeedbackObserver) UnbindRemoteStream(info *interceptor.StreamInfo) {
	if capture := o.timing.Load(); capture != nil && info != nil && info.SSRC == capture.scope.SSRC {
		capture.stop("stream_unbound", time.Now())
	}
}
func (v *Voice) StreamTimingDiagnostics(request StreamTimingRequest, action string) (*StreamTimingDiagnostics, error) {
	return v.router.streamTiming(request, action, readSelectedStreamRoute)
}

// No route getter runs in the background. Explicit operator requests use the
// existing synchronous local Pion getters without holding diagnostic locks.
func (r *Router) streamTiming(request StreamTimingRequest, action string, read func(*PeerConnectionWrapper) (selectedStreamRoute, error)) (*StreamTimingDiagnostics, error) {
	if request.RouteToken == "" || request.PeerID == "" || request.IngressStartedAt <= 0 {
		return nil, ErrStreamRouteUnavailable
	}
	r.timingMu.Lock()
	existing := r.timingDiagnostic
	r.timingMu.Unlock()
	if action != "arm" {
		if existing == nil || existing.scope != request {
			return nil, ErrStreamRouteUnavailable
		}
		switch action {
		case "stop":
			existing.stop("operator_stopped", time.Now())
		case "read":
			route, err := r.streamRoute(request.StreamRouteRequest, read)
			matches := err == nil && route.PeerID == request.PeerID && route.IngressStartedAt == request.IngressStartedAt
			now := time.Now()
			at := now.UnixMilli()
			existing.mu.Lock()
			existing.routeEnd, existing.routeEndAt = &matches, &at
			existing.mu.Unlock()
			if !matches {
				existing.stop("route_changed", now)
			}
		default:
			return nil, ErrStreamRouteUnavailable
		}
		return existing.snapshot(time.Now()), nil
	}
	if existing != nil {
		return nil, ErrStreamRouteUnavailable
	}
	var routePeer *PeerConnectionWrapper
	route, err := r.streamRoute(request.StreamRouteRequest, func(p *PeerConnectionWrapper) (selectedStreamRoute, error) {
		if routePeer != nil && routePeer != p {
			return selectedStreamRoute{}, ErrStreamRouteUnavailable
		}
		routePeer = p
		return read(p)
	})
	if err != nil || route.PeerID != request.PeerID || route.IngressStartedAt != request.IngressStartedAt {
		return nil, ErrStreamRouteUnavailable
	}
	if !r.mu.TryRLock() {
		return nil, ErrStreamRouteUnavailable
	}
	if !r.watchMu.TryRLock() {
		r.mu.RUnlock()
		return nil, ErrStreamRouteUnavailable
	}
	peer := r.pubPeers[request.PublisherID]
	input := r.videoIngress[videoIngressKey{request.PublisherID, request.Slot, route.RID}]
	valid := peer != nil && peer == routePeer && input != nil && peer.upstreamFeedback != nil && r.publications[publicationKey{request.PublisherID, request.Slot}] == request.Generation
	var inputStarted time.Time
	if valid {
		if !input.mu.TryLock() {
			valid = false
		} else {
			inputStarted = input.started
			valid = input.started.UnixMilli() == request.IngressStartedAt && input.ssrc == request.SSRC
			input.mu.Unlock()
		}
	}
	r.watchMu.RUnlock()
	r.mu.RUnlock()
	if !valid {
		return nil, ErrStreamRouteUnavailable
	}
	observer := peer.upstreamFeedback
	s := &upstreamTimingSession{scope: request, done: make(chan struct{}), reason: "recording", rtp: make([]StreamTimingRTP, 0, upstreamTimingMaxRTP), feedback: make([]StreamTimingFeedback, 0, upstreamTimingMaxReports)}
	s.guard = func() (bool, bool) {
		if !r.mu.TryRLock() {
			return false, false
		}
		defer r.mu.RUnlock()
		if !r.watchMu.TryRLock() {
			return false, false
		}
		defer r.watchMu.RUnlock()
		if r.pubPeers[request.PublisherID] != peer || r.clientChan[request.PublisherID] != route.ChannelID || r.publications[publicationKey{request.PublisherID, request.Slot}] != request.Generation || r.videoIngress[videoIngressKey{request.PublisherID, request.Slot, route.RID}] != input || r.videoSources[request.PublisherID][request.Slot][route.RID] != request.SSRC {
			return false, true
		}
		if !input.mu.TryLock() {
			return false, false
		}
		defer input.mu.Unlock()
		return input.started == inputStarted && input.ssrc == request.SSRC, true
	}
	r.timingMu.Lock()
	defer r.timingMu.Unlock()
	if r.timingDiagnostic != nil {
		return nil, ErrStreamRouteUnavailable
	}
	if ok, checked := s.guard(); !checked || !ok {
		return nil, ErrStreamRouteUnavailable
	}
	if !observer.mu.TryLock() {
		return nil, ErrStreamRouteUnavailable
	}
	s.started = time.Now()
	if observer.closed || !observer.timing.CompareAndSwap(nil, s) {
		observer.mu.Unlock()
		return nil, ErrStreamRouteUnavailable
	}
	observer.mu.Unlock()
	r.timingDiagnostic = s
	deadline := time.AfterFunc(upstreamTimingDuration, func() { s.stop("complete", time.Now()); observer.timing.CompareAndSwap(s, nil) })
	time.AfterFunc(upstreamTimingDuration+upstreamTimingRetention, func() {
		r.timingMu.Lock()
		defer r.timingMu.Unlock()
		s.expire()
		if r.timingDiagnostic == s {
			r.timingDiagnostic = nil
		}
	})
	go func() {
		defer deadline.Stop()
		defer observer.timing.CompareAndSwap(s, nil)
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-s.done:
				return
			case <-ticker.C:
				if !s.scopeOK(time.Now()) {
					return
				}
			}
		}
	}()
	return s.snapshot(time.Now()), nil
}
