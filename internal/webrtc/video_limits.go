package webrtc

import (
	"fmt"
	"time"
)

type videoBudget struct {
	tokens float64
	at     time.Time
}

type videoPolicy struct {
	bounds                   VideoBounds
	revision, boundsRevision uint64
}

func (r *Router) videoPolicySnapshot() videoPolicy {
	r.videoPolicyMu.RLock()
	defer r.videoPolicyMu.RUnlock()
	return r.videoPolicy
}

// SetVideoLimits atomically replaces relay bitrate and encoded dimensions.
// It drains earlier output writes before returning; packets inspected under an
// older revision cannot subsequently reach subscribers or recording taps.
// Enabling bounds on non-VP8 peers requires the caller to renegotiate those peers.
// No engine codec or client capture setting is changed by this router operation.
func (r *Router) SetVideoLimits(bitsPerSecond int, bounds VideoBounds) error {
	r.videoPolicyMu.Lock()
	defer r.videoPolicyMu.Unlock()
	return r.setVideoLimitsLocked(bitsPerSecond, bounds)
}

// SetVideoBitrateLimit configures an independent per-publisher RTP byte ceiling
// across all video slots and simulcast layers. Zero disables it. The one-second
// burst absorbs keyframes; excess packets are dropped before fan-out and taps.
// This is a relay ceiling, not an inbound network or encoded-resolution limit.
func (r *Router) SetVideoBitrateLimit(bitsPerSecond int) error {
	r.videoPolicyMu.Lock()
	defer r.videoPolicyMu.Unlock()
	return r.setVideoLimitsLocked(bitsPerSecond, r.videoPolicy.bounds)
}

func validateVideoLimits(bitsPerSecond int, bounds VideoBounds) error {
	if bitsPerSecond < 0 || bitsPerSecond > 100_000_000 {
		return fmt.Errorf("video bitrate limit must be between 0 and 100000000 bits/s")
	}
	return bounds.validate()
}

func (r *Router) setVideoLimitsLocked(bitsPerSecond int, bounds VideoBounds) error {
	if err := validateVideoLimits(bitsPerSecond, bounds); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.videoBitrateLimit == bitsPerSecond && r.videoPolicy.bounds == bounds {
		return nil
	}
	r.videoPolicy.revision++
	if r.videoPolicy.bounds != bounds {
		r.videoPolicy.bounds = bounds
		r.videoPolicy.boundsRevision++
	}
	if r.videoBitrateLimit != bitsPerSecond {
		r.videoBitrateLimit = bitsPerSecond
		r.videoBudgets = make(map[string]videoBudget)
	}
	return nil
}

func (r *Router) allowVideoPacket(clientID string, bytes int, now time.Time) bool {
	r.mu.RLock()
	unlimited := r.videoBitrateLimit == 0
	r.mu.RUnlock()
	if unlimited {
		return true
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.videoBitrateLimit == 0 {
		return true
	}
	if _, joined := r.clientChan[clientID]; !joined {
		return false
	}
	capacity := float64(r.videoBitrateLimit) / 8
	b, exists := r.videoBudgets[clientID]
	if !exists {
		b = videoBudget{tokens: capacity, at: now}
	} else if now.After(b.at) {
		b.tokens = min(capacity, b.tokens+now.Sub(b.at).Seconds()*capacity)
		b.at = now
	}
	allowed := bytes >= 0 && float64(bytes) <= b.tokens
	if allowed {
		b.tokens -= float64(bytes)
	}
	r.videoBudgets[clientID] = b
	return allowed
}
