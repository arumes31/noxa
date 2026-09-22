// voice.go ties the Engine and Router together and exposes plain-type
// operations for the TCP control server, so the server package never touches
// Pion types directly.
package webrtc

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/pion/sdp/v3"
	"github.com/pion/webrtc/v4"
	"go.uber.org/zap"
)

// ErrNoPeer is returned by Voice operations that reference a client with no
// active peer connection.
var ErrNoPeer = errors.New("webrtc: no peer connection for client")

// ErrPeerReset requires a fresh signaling session after a partially applied SDP.
var ErrPeerReset = errors.New("webrtc: media negotiation requires reconnect")

// renegState tracks per-peer renegotiation scheduling (debounce + rate limit
// + unanswered-offer tolerance).
type renegState struct {
	timer      renegTimer
	generation uint64
	pending    bool
	lastSent   time.Time
	unanswered int
}

type renegTimer interface {
	Stop() bool
}

// OfferGuard synchronously authorizes SDP construction and delivery together.
// It runs outside all voice/router locks and must never retain send.
type OfferGuard func(clientID string, send func() error) error

type renegClock interface {
	Now() time.Time
	AfterFunc(time.Duration, func()) renegTimer
}

type realtimeRenegClock struct{}

func (realtimeRenegClock) Now() time.Time {
	return time.Now()
}

func (realtimeRenegClock) AfterFunc(delay time.Duration, callback func()) renegTimer {
	return time.AfterFunc(delay, callback)
}

const (
	defaultRenegDebounce  = 200 * time.Millisecond
	defaultRenegRateLimit = 2 * time.Second
)

// Voice is the signaling and routing facade for the noxa voice pipeline.
type Voice struct {
	engine *Engine
	router *Router
	logger *zap.Logger

	videoLimitsMu contextRWMutex

	renegMu        sync.Mutex
	reneg          map[string]*renegState
	offerSender    func(clientID, offerSDP string) error
	offerGuard     OfferGuard
	renegClock     renegClock
	renegDebounce  time.Duration
	renegRateLimit time.Duration
}

// NewVoice constructs a Voice facade over the given engine and router.
func NewVoice(engine *Engine, router *Router, logger *zap.Logger) *Voice {
	if logger == nil {
		logger = zap.NewNop()
	}
	v := &Voice{
		engine:         engine,
		router:         router,
		logger:         logger,
		reneg:          make(map[string]*renegState),
		renegClock:     realtimeRenegClock{},
		renegDebounce:  defaultRenegDebounce,
		renegRateLimit: defaultRenegRateLimit,
	}
	router.SetRenegotiateHook(v.scheduleRenegotiate)
	return v
}

// SetHandlers installs the talk-permission gate and the speaking-state
// callback on the router. See Router.SetHandlers.
func (v *Voice) SetHandlers(canTalk func(clientID string) bool, onSpeaking func(clientID string, speaking bool)) {
	v.router.SetHandlers(canTalk, onSpeaking)
}

// SetVideoHandlers installs the video-publish permission gate on the router.
// See Router.SetVideoHandlers.
func (v *Voice) SetVideoHandlers(canVideo func(clientID string) bool) {
	v.router.SetVideoHandlers(canVideo)
}

// SetVideoQuality sets a subscriber's preferred simulcast layer ("high",
// "mid", or "low"). See Router.SetVideoQuality.
func (v *Voice) SetVideoQuality(clientID, quality string) error {
	return v.router.SetVideoQuality(clientID, quality)
}

// DeclareTrackSlots records which media slot each of the client's outbound
// tracks occupies, keyed by MSID track ID. It must be called BEFORE the offer
// that introduces those tracks is handled, so the first answer already carries
// the matching output tracks. See Router.SetTrackSlots (70).
func (v *Voice) DeclareTrackSlots(clientID string, slots map[string]string) {
	v.router.SetTrackSlots(clientID, slots)
}

// SetOfferSender installs the callback used to deliver server-initiated
// renegotiation offers to clients (the TCP control channel in production).
func (v *Voice) SetOfferSender(fn func(clientID, offerSDP string) error) {
	v.renegMu.Lock()
	defer v.renegMu.Unlock()
	v.offerSender = fn
}

func (v *Voice) SetOfferGuard(guard OfferGuard) {
	v.renegMu.Lock()
	defer v.renegMu.Unlock()
	v.offerGuard = guard
}

// RefreshSubscriber queues renegotiation on the voice scheduler. It does not
// construct SDP here; OfferGuard rechecks access when the timer delivers it.
func (v *Voice) RefreshSubscriber(clientID string) {
	if v.engine.PeerConnection(clientID) != nil {
		v.scheduleRenegotiate(clientID)
	}
}

// SetEchoChannel sets the loopback test channel (15). See
// Router.SetEchoChannel.
func (v *Voice) SetEchoChannel(channelID int64) {
	v.router.SetEchoChannel(channelID)
}

// SetChannelAudioLookup installs the per-channel Opus configuration resolver
// (21-25): it drives SDP fmtp rewriting (per subscriber channel) and
// music-channel detection (talk-gate bypass). See
// Router.SetChannelAudioLookup.
func (v *Voice) SetChannelAudioLookup(fn func(channelID int64) ChannelAudio) {
	v.router.SetChannelAudioLookup(fn)
}

// channelAudioFor resolves the Opus configuration of the client's current
// channel. It returns the zero ChannelAudio (server default, no munging)
// when no lookup is installed or the client is in no channel.
func (v *Voice) channelAudioFor(clientID string) ChannelAudio {
	v.router.mu.RLock()
	defer v.router.mu.RUnlock()
	if v.router.channelAudio == nil {
		return ChannelAudio{}
	}
	channelID, ok := v.router.clientChan[clientID]
	if !ok {
		return ChannelAudio{}
	}
	return v.router.channelAudio(channelID)
}

// scheduleRenegotiate debounces track-set changes for a peer into a single
// renegotiation offer, capped at one offer per renegRateLimit.
func (v *Voice) scheduleRenegotiate(clientID string) {
	v.scheduleRenegotiateForState(clientID, nil)
}

// expected prevents a late answer from recreating scheduling state after the
// peer was closed or replaced.
func (v *Voice) scheduleRenegotiateForState(clientID string, expected *renegState) {
	v.renegMu.Lock()
	st, ok := v.reneg[clientID]
	if expected != nil && st != expected {
		v.renegMu.Unlock()
		return
	}
	if !ok {
		st = &renegState{}
		v.reneg[clientID] = st
	}
	if st.timer != nil {
		st.timer.Stop()
		st.timer = nil
	}
	clock, debounce, rateLimit := v.renegScheduleConfigLocked()
	delay := debounce
	if elapsed := clock.Now().Sub(st.lastSent); !st.lastSent.IsZero() && elapsed < rateLimit {
		delay = rateLimit - elapsed
	}
	st.generation++
	generation := st.generation
	st.pending = true
	v.renegMu.Unlock()

	// Schedule outside the renegotiation mutex: production timers run
	// asynchronously, but test schedulers may invoke callbacks immediately.
	timer := clock.AfterFunc(delay, func() { v.sendRenegotiation(clientID, st, generation) })
	v.renegMu.Lock()
	if active, ok := v.reneg[clientID]; ok && active == st &&
		active.generation == generation && active.pending {
		active.timer = timer
		v.renegMu.Unlock()
		return
	}
	v.renegMu.Unlock()
	timer.Stop()
}

func (v *Voice) renegScheduleConfigLocked() (renegClock, time.Duration, time.Duration) {
	clock := v.renegClock
	if clock == nil {
		clock = realtimeRenegClock{}
	}
	debounce := v.renegDebounce
	if debounce <= 0 {
		debounce = defaultRenegDebounce
	}
	rateLimit := v.renegRateLimit
	if rateLimit <= 0 {
		rateLimit = defaultRenegRateLimit
	}
	return clock, debounce, rateLimit
}

// sendRenegotiation creates and delivers a renegotiation offer for a peer.
// An unanswered offer suspends delivery while retaining pending track changes.
// HandleAnswer resumes those changes once signaling is stable again.
func (v *Voice) sendRenegotiation(clientID string, expected *renegState, generation uint64) {
	v.renegMu.Lock()
	st, ok := v.reneg[clientID]
	if !ok || st != expected || st.generation != generation || !st.pending {
		v.renegMu.Unlock()
		return
	}
	st.timer = nil
	if st.unanswered > 0 {
		unanswered := st.unanswered
		v.renegMu.Unlock()
		v.logger.Warn("client did not answer previous renegotiation offer; pending changes will resume after its answer",
			zap.String("client_id", clientID),
			zap.Int("unanswered", unanswered),
		)
		return
	}
	st.pending = false
	sender := v.offerSender
	guard := v.offerGuard
	clock, _, _ := v.renegScheduleConfigLocked()
	var wrapper *PeerConnectionWrapper
	if v.engine != nil {
		wrapper = v.engine.PeerConnection(clientID)
	}
	if wrapper != nil {
		// Reserve the offer before CreateOffer changes Pion's signaling state.
		// A concurrent track change must remain pending even before delivery.
		st.unanswered = 1
	}
	v.renegMu.Unlock()

	if wrapper == nil {
		return
	}

	send := func() error {
		// Guard acquisition may have waited through a peer replacement.
		v.renegMu.Lock()
		// A newer track-change generation is retained in pending and will
		// follow this reserved offer's answer; a replaced peer must be skipped.
		current := v.reneg[clientID] == st
		v.renegMu.Unlock()
		if !current || v.engine.PeerConnection(clientID) != wrapper {
			return ErrNoPeer
		}
		v.router.PrepareSubscriber(clientID)
		offer, err := wrapper.CreateOffer()
		if err != nil {
			v.renegMu.Lock()
			if v.reneg[clientID] == st {
				st.unanswered = 0
			}
			v.renegMu.Unlock()
			v.logger.Debug("renegotiation offer skipped",
				zap.String("client_id", clientID),
				zap.Error(err),
			)
			return err
		}

		// Per-channel Opus parameters (21-23): rewrite the offer's Opus fmtp for
		// the subscriber's channel before delivery.
		if cfg := v.channelAudioFor(clientID); !cfg.IsZero() {
			offer = RewriteOpusFMTP(offer, cfg)
		}

		v.renegMu.Lock()
		if v.reneg[clientID] != st {
			v.renegMu.Unlock()
			return ErrNoPeer
		}
		st.lastSent = clock.Now()
		v.renegMu.Unlock()

		if sender != nil {
			return sender(clientID, offer)
		}
		return nil
	}
	var err error
	if guard == nil {
		err = send()
	} else {
		err = guard(clientID, send)
	}
	if err != nil {
		v.renegMu.Lock()
		if v.reneg[clientID] == st {
			st.unanswered = 0
			st.pending = true
		}
		v.renegMu.Unlock()
		v.logger.Debug("renegotiation not delivered", zap.String("client_id", clientID), zap.Error(err))
	}
}

// AddTap registers an additional subscriber (e.g. a recorder) in a channel
// with explicit audio/video output tracks.
func (v *Voice) AddTap(channelID int64, tapID string, audio, video TrackWriter) {
	if audio != nil {
		v.router.AddOutput(tapID, audio)
	}
	if video != nil {
		v.router.addVideoOutput(tapID, video)
	}
	v.router.JoinChannel(channelID, tapID)
}

// RemoveTap removes a tap previously registered with AddTap.
func (v *Voice) RemoveTap(tapID string) {
	v.router.DetachPeer(tapID)
}

// RequestSourceKeyframe addresses the exact recording source, including its
// simulcast layer, rather than an arbitrary SSRC sharing the publisher slot.
func (v *Voice) RequestSourceKeyframe(publisher, slot string, ssrc uint32) {
	v.router.mu.RLock()
	var rid string
	found := false
	for candidate, source := range v.router.videoSources[publisher][slot] {
		if source == ssrc {
			rid, found = candidate, true
			break
		}
	}
	v.router.mu.RUnlock()
	if found {
		v.router.RequestKeyframe(publisher, slot, rid)
	}
}

// HandleOffer preserves the transport for camera, screen and ICE renegotiation.
// A new remote DTLS identity (for example after switching desktop tabs) replaces
// the peer while retaining channel membership, whisper intent and quality.
//
// onLocalCandidate, if non-nil, is invoked asynchronously for every locally
// gathered ICE candidate until the peer connection is closed.
func (v *Voice) HandleOffer(clientID, offerSDP string, onLocalCandidate func(candidate, sdpMid string, mlineIndex uint16)) (string, error) {
	fingerprint, err := remoteFingerprint(offerSDP)
	if err != nil {
		return "", err
	}
	if existing := v.engine.PeerConnection(clientID); existing != nil {
		if remote := existing.pc.RemoteDescription(); remote != nil {
			previous, parseErr := remoteFingerprint(remote.SDP)
			if parseErr == nil && previous == fingerprint {
				v.router.EnsurePublishers(clientID)
				v.router.PrepareSubscriber(clientID)
				v.engine.mu.RLock()
				bounds := v.engine.videoBounds
				v.engine.mu.RUnlock()
				answer, err := existing.handleOffer(offerSDP, &bounds)
				if err == nil {
					if cfg := v.channelAudioFor(clientID); !cfg.IsZero() {
						answer = RewriteOpusFMTP(answer, cfg)
					}
				}
				return answer, err
			}
		}
	}
	// The rebuild replaces the peer connection only: dropping the client's
	// channel would leave it routed to nobody after an ICE restart (59), and
	// dropping plus restoring it would undo a move that lands mid-rebuild (the
	// control server owns membership and can move the client concurrently).
	v.stopReneg(clientID)
	v.router.DetachPeerKeepChannel(clientID)
	_ = v.engine.ClosePeerConnection(clientID)

	wrapper, err := v.engine.NewPeerConnection(clientID)
	if err != nil {
		return "", err
	}
	if err := v.router.AttachPeer(clientID, wrapper); err != nil {
		_ = v.engine.ClosePeerConnection(clientID)
		return "", err
	}

	// Per-publisher model: create tracks for every current channel member
	// BEFORE the initial answer so they are negotiated right away.
	v.router.EnsurePublishers(clientID)
	v.router.PrepareSubscriber(clientID)

	answer, err := wrapper.HandleOffer(offerSDP)
	if err != nil {
		v.router.DetachPeerKeepChannel(clientID)
		_ = v.engine.ClosePeerConnection(clientID)
		return "", err
	}

	// Per-channel Opus parameters (21-23): rewrite the answer's Opus fmtp
	// for the subscriber's channel.
	if cfg := v.channelAudioFor(clientID); !cfg.IsZero() {
		answer = RewriteOpusFMTP(answer, cfg)
	}

	if onLocalCandidate != nil {
		go func() {
			candidates := wrapper.LocalCandidates()
			for {
				select {
				case c, ok := <-candidates:
					if !ok {
						return
					}
					var mid string
					if c.SDPMid != nil {
						mid = *c.SDPMid
					}
					var idx uint16
					if c.SDPMLineIndex != nil {
						idx = *c.SDPMLineIndex
					}
					onLocalCandidate(c.Candidate, mid, idx)
				case <-wrapper.Done():
					return
				}
			}
		}()
	}

	v.logger.Info("voice session established", zap.String("client_id", clientID))
	return answer, nil
}

// The bundled transport must have one unambiguous DTLS identity. Media-level
// and session-level fingerprint placement are both used by real clients.
func remoteFingerprint(raw string) (string, error) {
	var description sdp.SessionDescription
	if err := description.Unmarshal([]byte(raw)); err != nil {
		return "", fmt.Errorf("parsing remote SDP: %w", err)
	}
	var fingerprint string
	read := func(attributes []sdp.Attribute) error {
		for _, attribute := range attributes {
			if attribute.Key != "fingerprint" {
				continue
			}
			value := strings.ToLower(strings.Join(strings.Fields(attribute.Value), " "))
			if value == "" || (fingerprint != "" && fingerprint != value) {
				return errors.New("inconsistent remote DTLS fingerprint")
			}
			fingerprint = value
		}
		return nil
	}
	if err := read(description.Attributes); err != nil {
		return "", err
	}
	for _, media := range description.MediaDescriptions {
		if err := read(media.Attributes); err != nil {
			return "", err
		}
	}
	if fingerprint == "" {
		return "", errors.New("remote DTLS fingerprint missing")
	}
	return fingerprint, nil
}

// HandleAnswer applies an SDP answer from the client (used when the server
// initiated renegotiation).
func (v *Voice) HandleAnswer(clientID, answerSDP string) error {
	v.renegMu.Lock()
	st := v.reneg[clientID]
	v.renegMu.Unlock()
	wrapper := v.engine.PeerConnection(clientID)
	if wrapper == nil {
		return ErrNoPeer
	}
	if err := wrapper.HandleAnswer(answerSDP); err != nil {
		return err
	}
	v.renegMu.Lock()
	pending := false
	if current := v.reneg[clientID]; st != nil && current == st {
		st.unanswered = 0
		pending = st.pending
	}
	v.renegMu.Unlock()
	if pending {
		v.scheduleRenegotiateForState(clientID, st)
	}
	return nil
}

// AddICECandidate applies a trickle ICE candidate received from the client.
func (v *Voice) AddICECandidate(clientID, candidate, sdpMid string, mlineIndex uint16) error {
	wrapper := v.engine.PeerConnection(clientID)
	if wrapper == nil {
		return ErrNoPeer
	}
	init := webrtc.ICECandidateInit{Candidate: candidate}
	if sdpMid != "" {
		init.SDPMid = &sdpMid
	}
	init.SDPMLineIndex = &mlineIndex
	return wrapper.AddICECandidate(init)
}

// ClosePeer tears down the client's voice session: peer connection, router
// output, channel membership, and whisper configuration. It is a no-op when
// no session exists.
func (v *Voice) ClosePeer(clientID string) error {
	v.stopReneg(clientID)
	v.router.DetachPeer(clientID)
	return v.engine.ClosePeerConnection(clientID)
}

// stopReneg drops the peer's renegotiation scheduling state, cancelling a
// pending offer timer so it cannot fire against the next peer connection.
func (v *Voice) stopReneg(clientID string) {
	v.renegMu.Lock()
	defer v.renegMu.Unlock()
	if st, ok := v.reneg[clientID]; ok {
		if st.timer != nil {
			st.timer.Stop()
		}
		delete(v.reneg, clientID)
	}
}

// JoinChannel records channel membership for routing.
func (v *Voice) JoinChannel(clientID string, channelID int64) {
	v.router.JoinChannel(channelID, clientID)
}

// LeaveChannel removes channel membership for routing.
func (v *Voice) LeaveChannel(clientID string, channelID int64) {
	v.router.LeaveChannel(channelID, clientID)
}

// SetWhisper replaces the client's whisper configuration.
func (v *Voice) SetWhisper(clientID string, clients []string, channels []int64, active bool) {
	v.router.SetWhisper(clientID, clients, channels, active)
}

// WhisperTargets returns the client IDs the client's active whisper reaches
// (nil when it is not whispering). See Router.WhisperTargets (32/33).
func (v *Voice) WhisperTargets(clientID string) []string {
	return v.router.WhisperTargets(clientID)
}

// PeerCount returns the number of active peer connections.
func (v *Voice) PeerCount() int {
	return v.engine.PeerCount()
}
