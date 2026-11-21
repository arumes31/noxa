package webrtc

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v4"
	"go.uber.org/zap"
)

// PeerConnectionWrapper wraps a Pion webrtc.PeerConnection with the metadata
// and signaling plumbing required by the noxa server. It tracks the owning
// clientID, exposes offer/answer/ICE/track helpers, and surfaces local ICE
// candidates and the local/remote SDP through buffered channels for the
// signaling layer to consume.
type PeerConnectionWrapper struct {
	// stateHook reports connection state changes (Engine.peerState). It is
	// set by the engine before any remote description, so before callbacks.
	stateHook   func(clientID string, state webrtc.PeerConnectionState)
	mediaPolicy MediaPolicy
	pc          *webrtc.PeerConnection
	clientID    string
	logger      *zap.Logger
	// Immutable codec bounds captured when the engine creates this peer.
	videoBounds             VideoBounds
	egress                  *mediaEgressRegistry
	upstreamFeedback        *upstreamFeedbackObserver
	videoRepairRTTMu        sync.Mutex
	videoRepairRTTInFlight  *videoRepairRTTResult
	videoRepairRTTCompleted time.Time

	// localCandidates forwards ICE candidates generated locally. It is never
	// closed (see Close); consumers should also select on Done.
	localCandidates chan webrtc.ICECandidateInit

	// remoteCandidates is a buffered channel for candidates received from the
	// remote peer via signaling. It is consumed by an internal goroutine that
	// applies them to the underlying PeerConnection.
	remoteCandidates chan webrtc.ICECandidateInit

	// candidateMu guards the distinct remote candidates seen since the last
	// ICE restart (see AddICECandidate).
	candidateMu     sync.Mutex
	seenCandidates  map[string]struct{}
	candidateWarned bool

	onTrack             func(track *webrtc.TrackRemote, receiver *webrtc.RTPReceiver)
	videoStartupMu      sync.Mutex
	videoStartupWake    chan struct{}
	videoStartupDone    chan struct{}
	videoStartupRun     bool
	videoStartupHandler func() time.Duration

	closeOnce sync.Once
	signalMu  sync.Mutex
	// removalMu guards signalingBusy and deferredRemovals. Pion starts every
	// newly negotiated sender when an answer is applied (local answer or
	// remote answer) and fails that whole SetLocal/SetRemoteDescription with
	// "Sender Track has been removed" if one of them was removed while the
	// offer/answer exchange was in flight, leaving the connection half started.
	// Removals that race an exchange are therefore applied after it settles.
	removalMu           sync.Mutex
	signalingBusy       bool
	deferredRemovals    []*webrtc.RTPSender
	videoRecoveryGrowth int // signalMu; cumulative new transceivers from recovery
	videoRecoveryLast   time.Time
	closed              chan struct{}
}

// ReceiverReportStats is the loss/jitter/RTT view derived from RTCP Receiver
// Reports generated and consumed by Pion's report interceptor.
type ReceiverReportStats struct {
	SSRC         uint32
	Kind         string
	PacketsLost  int32
	FractionLost float64
	Jitter       time.Duration
	RTT          time.Duration
}

// newPeerConnectionWrapper constructs a wrapper around pc and starts the
// internal goroutine that drains remote ICE candidates. It is called by the
// Engine when a new peer connection is created.
func newPeerConnectionWrapper(pc *webrtc.PeerConnection, clientID string, logger *zap.Logger) *PeerConnectionWrapper {
	w := &PeerConnectionWrapper{
		pc:               pc,
		clientID:         clientID,
		logger:           logger,
		localCandidates:  make(chan webrtc.ICECandidateInit, 64),
		remoteCandidates: make(chan webrtc.ICECandidateInit, 64),
		closed:           make(chan struct{}),
		videoStartupWake: make(chan struct{}, 1),
	}

	pc.OnICECandidate(w.handleLocalICECandidate)
	pc.OnTrack(w.handleTrack)
	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		if state == webrtc.PeerConnectionStateConnected {
			w.notifyVideoStartup()
		}
		if w.stateHook != nil {
			w.stateHook(w.clientID, state)
		}
	})

	go w.drainRemoteCandidates()
	return w
}

// ClientID returns the identifier of the client that owns this peer connection.
func (w *PeerConnectionWrapper) ClientID() string { return w.clientID }

// LocalCandidates returns a channel that receives locally-gathered ICE
// candidates. The channel is never closed (Pion's gatherer may send
// concurrently with Close); consumers should also select on Done.
func (w *PeerConnectionWrapper) LocalCandidates() <-chan webrtc.ICECandidateInit {
	return w.localCandidates
}

// maxRemoteSSRCAttributes bounds the a=ssrc and a=ssrc-group lines a client
// description may carry. Pion collects receiver SSRCs with repeated linear
// scans, so a 1 MiB signaling frame of SSRC attributes would cost quadratic
// work under the channel-control lock. A browser publishing every slot with
// simulcast and RTX uses a few dozen.
const maxRemoteSSRCAttributes = 512

// maxRemoteMediaSections bounds the m= sections of a client description.
// Each one becomes a Pion transceiver. A subscriber's description carries one
// section per received slot (mic, cam, screen, screen audio) of every other
// member, so a full 64-seat voice channel needs about 4*63 plus its own few
// and the data channel; the rest is headroom for recycled sections.
const maxRemoteMediaSections = 384

// maxRemoteICECandidates bounds the distinct remote candidates of one peer
// (per set of remote ICE credentials), trickled or in a description. Pion
// pairs every remote candidate with every local one using linear checklist
// scans; a browser sends a handful (connectionbenchmark uses the same cap).
const maxRemoteICECandidates = 64

// ErrTooManyICECandidates rejects remote candidates over that budget.
var ErrTooManyICECandidates = errors.New("webrtc: too many remote ICE candidates")

// ErrSDPTooManySSRCs rejects a remote description over the SSRC budget.
var ErrSDPTooManySSRCs = errors.New("webrtc: session description declares too many SSRCs")

// ErrSDPTooManyMediaSections rejects a remote description with too many m= lines.
var ErrSDPTooManyMediaSections = errors.New("webrtc: session description declares too many media sections")

func checkRemoteSDPBudget(sdp string) error {
	ssrcs, sections, candidates := 0, 0, 0
	// Pion's lexer also ends a line at a bare CR, so split at CR and LF alike.
	for _, line := range strings.FieldsFunc(sdp, func(r rune) bool { return r == '\r' || r == '\n' }) {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "a=ssrc") {
			if ssrcs++; ssrcs > maxRemoteSSRCAttributes {
				return ErrSDPTooManySSRCs
			}
		}
		if strings.HasPrefix(line, "m=") {
			if sections++; sections > maxRemoteMediaSections {
				return ErrSDPTooManyMediaSections
			}
		}
		if strings.HasPrefix(line, "a=candidate:") {
			if candidates++; candidates > maxRemoteICECandidates {
				return ErrTooManyICECandidates
			}
		}
	}
	return nil
}

// descriptionCandidates lists the candidates a remote description carries.
// Pion adds them on every SetRemoteDescription, so re-offers count too.
func descriptionCandidates(sdp string) (candidates []string) {
	for _, line := range strings.FieldsFunc(sdp, func(r rune) bool { return r == '\r' || r == '\n' }) {
		if line = strings.TrimSpace(line); strings.HasPrefix(line, "a=candidate:") {
			candidates = append(candidates, line[len("a="):])
		}
	}
	return candidates
}

// remoteICEUfrag is the remote ICE username fragment Pion uses now, "" before
// it has one.
func (w *PeerConnectionWrapper) remoteICEUfrag() string {
	params, err := w.pc.SCTP().Transport().ICETransport().GetRemoteParameters()
	if err != nil {
		return ""
	}
	return params.UsernameFragment
}

// offerRestartsICE reports whether Pion, if it applies offer, sees new remote
// ICE credentials and so restarts ICE, discarding its remote candidates. The
// offer is parsed with Pion's own parser: Pion takes the fragment from the
// session or from one media section, so an offer that names the current one
// anywhere is not treated as a restart.
func offerRestartsICE(offer webrtc.SessionDescription, current string) bool {
	parsed, err := offer.Unmarshal()
	if err != nil || current == "" {
		return false
	}
	if ufrag, _ := parsed.Attribute("ice-ufrag"); ufrag == current {
		return false
	}
	for _, media := range parsed.MediaDescriptions {
		if ufrag, _ := media.Attribute("ice-ufrag"); ufrag == current {
			return false
		}
	}
	return true
}

// admitCandidatesLocked records new candidates, refusing all of them once
// the budget would be exceeded. Repeats are free. It unlocks candidateMu.
func (w *PeerConnectionWrapper) admitCandidatesLocked(candidates ...string) error {
	fresh := make(map[string]struct{})
	for _, c := range candidates {
		if _, seen := w.seenCandidates[c]; !seen {
			fresh[c] = struct{}{}
		}
	}
	if len(w.seenCandidates)+len(fresh) > maxRemoteICECandidates {
		warn := !w.candidateWarned
		w.candidateWarned = true
		w.candidateMu.Unlock()
		if warn {
			w.logger.Warn("webrtc remote ice candidate budget exhausted",
				zap.String("client_id", w.clientID),
				zap.Int("limit", maxRemoteICECandidates))
		}
		return ErrTooManyICECandidates
	}
	if w.seenCandidates == nil {
		w.seenCandidates = make(map[string]struct{})
	}
	for c := range fresh {
		w.seenCandidates[c] = struct{}{}
	}
	w.candidateMu.Unlock()
	return nil
}

// HandleOffer sets the remote description (an offer SDP), creates an answer,
// sets the local description, and returns the answer SDP string.
func (w *PeerConnectionWrapper) HandleOffer(sdp string) (string, error) {
	return w.handleOffer(sdp, nil)
}

func (w *PeerConnectionWrapper) handleOffer(sdp string, bounds *VideoBounds) (result string, resultErr error) {
	w.signalMu.Lock()
	defer w.signalMu.Unlock()
	w.beginSignaling()
	defer w.endSignaling()
	if w.pc.SignalingState() == webrtc.SignalingStateHaveLocalOffer {
		return "", ErrOfferCollision
	}
	defer func() {
		// Pion cannot roll back a partially applied remote offer. Glare leaves
		// have-local-offer intact and can be retried after its answer instead.
		if resultErr != nil && w.pc.SignalingState() == webrtc.SignalingStateHaveRemoteOffer {
			resultErr = fmt.Errorf("%w: %v", ErrPeerReset, resultErr)
		}
	}()
	if err := checkRemoteSDPBudget(sdp); err != nil {
		return "", err
	}
	offer := webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: sdp}
	// The candidate budget restarts only together with Pion's candidate set:
	// an ICE restart offer is not charged to the old budget (checkRemoteSDPBudget
	// capped it at the limit), every other description is.
	candidates := descriptionCandidates(sdp)
	ufrag := w.remoteICEUfrag()
	restart := offerRestartsICE(offer, ufrag)
	if !restart {
		w.candidateMu.Lock()
		if err := w.admitCandidatesLocked(candidates...); err != nil {
			return "", err
		}
	}
	err := w.pc.SetRemoteDescription(offer)
	if restart {
		// Pion may have restarted ICE even when it then rejected the offer.
		restarted := w.remoteICEUfrag() != ufrag
		w.candidateMu.Lock()
		if restarted {
			w.seenCandidates, w.candidateWarned = nil, false
		}
		if w.seenCandidates == nil {
			w.seenCandidates = make(map[string]struct{})
		}
		for _, c := range candidates {
			w.seenCandidates[c] = struct{}{}
		}
		w.candidateMu.Unlock()
	}
	if err != nil {
		return "", fmt.Errorf("setting remote offer: %w", err)
	}

	if bounds != nil {
		w.videoBounds = *bounds
	}
	if err := w.setVideoBoundsCodecPreferences(); err != nil {
		return "", err
	}
	answer, err := w.pc.CreateAnswer(nil)
	if err != nil {
		return "", fmt.Errorf("creating answer: %w", err)
	}
	if err := w.pc.SetLocalDescription(answer); err != nil {
		return "", fmt.Errorf("setting local answer: %w", err)
	}

	w.logger.Info("webrtc offer handled",
		zap.String("client_id", w.clientID),
		zap.Int("sdp_len", len(answer.SDP)),
	)
	return answer.SDP, nil
}

// HandleAnswer sets the remote description (an answer SDP) after a local offer
// has been created and sent.
func (w *PeerConnectionWrapper) HandleAnswer(sdp string) error {
	w.signalMu.Lock()
	defer w.signalMu.Unlock()
	w.beginSignaling()
	defer w.endSignaling()
	if err := checkRemoteSDPBudget(sdp); err != nil {
		return err
	}
	// An answer never restarts ICE, so it never restarts the candidate budget.
	w.candidateMu.Lock()
	if err := w.admitCandidatesLocked(descriptionCandidates(sdp)...); err != nil {
		return err
	}
	answer := webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: sdp}
	if err := w.pc.SetRemoteDescription(answer); err != nil {
		return fmt.Errorf("setting remote answer: %w", err)
	}
	w.logger.Info("webrtc answer handled",
		zap.String("client_id", w.clientID),
		zap.Int("sdp_len", len(sdp)),
	)
	return nil
}

// CreateOffer creates a local offer (server-initiated renegotiation) and
// returns its SDP. It returns an error when the connection is not in the
// stable signaling state (e.g. a previous offer is still unanswered).
func (w *PeerConnectionWrapper) CreateOffer() (string, error) {
	w.signalMu.Lock()
	defer w.signalMu.Unlock()
	w.beginSignaling()
	defer w.endSignaling()
	if w.pc.SignalingState() != webrtc.SignalingStateStable {
		return "", fmt.Errorf("signaling state %s is not stable", w.pc.SignalingState())
	}
	if err := w.setVideoBoundsCodecPreferences(); err != nil {
		return "", err
	}
	offer, err := w.pc.CreateOffer(nil)
	if err != nil {
		return "", fmt.Errorf("creating offer: %w", err)
	}
	remoteSDP := ""
	if remote := w.pc.RemoteDescription(); remote != nil {
		remoteSDP = remote.SDP
	}
	wireOffer := offer
	wireOffer.SDP, err = stabilizeOfferExtensions(offer.SDP, remoteSDP)
	if err != nil {
		return "", err
	}
	// Pion requires the exact SDP returned by CreateOffer as its local offer.
	// The remote answer installs the wire IDs in the media engine before RTP
	// bindings start; the browser regression verifies those negotiated bindings.
	if err := w.pc.SetLocalDescription(offer); err != nil {
		return "", fmt.Errorf("setting local offer: %w", err)
	}
	w.logger.Info("webrtc renegotiation offer created",
		zap.String("client_id", w.clientID),
		zap.Int("sdp_len", len(wireOffer.SDP)),
		zap.Bool("extensions_remapped", wireOffer.SDP != offer.SDP),
	)
	return wireOffer.SDP, nil
}

// SignalingState reports the current signaling state. (Pion cannot roll back
// a local offer, so an unanswered server offer is redelivered, not undone.)
func (w *PeerConnectionWrapper) SignalingState() webrtc.SignalingState {
	return w.pc.SignalingState()
}

// AddICECandidate enqueues a remote ICE candidate for application to the
// underlying PeerConnection. Candidates are applied asynchronously by an
// internal goroutine to avoid blocking the signaling path. Repeated
// candidates are free; new ones beyond maxRemoteICECandidates are rejected.
func (w *PeerConnectionWrapper) AddICECandidate(candidate webrtc.ICECandidateInit) error {
	if candidate.Candidate != "" { // "" is end-of-candidates
		w.candidateMu.Lock()
		if err := w.admitCandidatesLocked(candidate.Candidate); err != nil {
			return err
		}
	}
	select {
	case w.remoteCandidates <- candidate:
		return nil
	case <-w.closed:
		return fmt.Errorf("peer connection closed")
	default:
		return fmt.Errorf("remote candidate queue full")
	}
}

// AddTrack adds a local track for sending to the remote peer and returns the
// resulting RTPSender.
func (w *PeerConnectionWrapper) AddTrack(track webrtc.TrackLocal) (*webrtc.RTPSender, error) {
	sender, err := w.pc.AddTrack(track)
	if err != nil {
		return nil, fmt.Errorf("adding track: %w", err)
	}
	w.logger.Info("webrtc track added",
		zap.String("client_id", w.clientID),
		zap.String("track_id", track.ID()),
	)
	return sender, nil
}

// removeSender detaches an output sender. While an offer/answer exchange is
// in flight (an SDP call in progress, or a local offer awaiting its answer)
// the removal is deferred until the exchange settles; the caller has already
// stopped the output, so no media flows in the meantime. It never blocks on
// signaling and may be called with Router.mu held.
func (w *PeerConnectionWrapper) removeSender(sender *webrtc.RTPSender) {
	if sender == nil {
		return
	}
	w.removalMu.Lock()
	defer w.removalMu.Unlock()
	if w.signalingBusy || w.pc.SignalingState() != webrtc.SignalingStateStable {
		w.deferredRemovals = append(w.deferredRemovals, sender)
		return
	}
	_ = w.pc.RemoveTrack(sender)
}

func (w *PeerConnectionWrapper) beginSignaling() {
	w.removalMu.Lock()
	w.signalingBusy = true
	w.removalMu.Unlock()
}

// endSignaling applies removals deferred during the exchange once the
// connection is stable again. A local offer still awaiting its answer keeps
// them deferred until HandleAnswer settles it.
func (w *PeerConnectionWrapper) endSignaling() {
	w.removalMu.Lock()
	defer w.removalMu.Unlock()
	w.signalingBusy = false
	if w.pc.SignalingState() != webrtc.SignalingStateStable || len(w.deferredRemovals) == 0 {
		return
	}
	for _, sender := range w.deferredRemovals {
		_ = w.pc.RemoveTrack(sender)
	}
	w.deferredRemovals = nil
}

// WriteRTCP sends RTCP packets to the remote peer (e.g. PLI keyframe
// requests). It satisfies the router's RTCPWriter interface.
func (w *PeerConnectionWrapper) WriteRTCP(pkts []rtcp.Packet) error {
	return w.pc.WriteRTCP(pkts)
}

// ReceiverReports returns the latest per-outbound-track RTCP measurements.
// RegisterDefaultInterceptors owns report generation; this method exposes the
// exact values received from the peer without maintaining a second estimator.
func (w *PeerConnectionWrapper) ReceiverReports() []ReceiverReportStats {
	report := w.pc.GetStats()
	out := make([]ReceiverReportStats, 0)
	for _, raw := range report {
		stat, ok := raw.(webrtc.RemoteInboundRTPStreamStats)
		if !ok {
			continue
		}
		rtt := time.Duration(stat.RoundTripTime * float64(time.Second))
		if w.egress != nil {
			// Egress SRs carry the publisher's NTP clock, so pion's
			// now-LSR-DLSR value includes that clock's offset. Use the RTT
			// measured against this server's own SR send times instead.
			rtt, _ = w.egress.roundTrip(uint32(stat.SSRC))
		}
		out = append(out, ReceiverReportStats{
			SSRC:         uint32(stat.SSRC),
			Kind:         stat.Kind,
			PacketsLost:  stat.PacketsLost,
			FractionLost: stat.FractionLost,
			Jitter:       time.Duration(stat.Jitter * float64(time.Second)),
			RTT:          rtt,
		})
	}
	return out
}

// OnTrack registers the handler invoked when a remote track arrives.
func (w *PeerConnectionWrapper) OnTrack(fn func(track *webrtc.TrackRemote, receiver *webrtc.RTPReceiver)) {
	w.onTrack = fn
}

// Close closes the underlying PeerConnection and releases all resources. It is
// safe to call multiple times.
//
// Note: localCandidates is deliberately NOT closed here. Pion's ICE gatherer
// may send on it concurrently with Close, and sending on a closed channel
// would race/panic. Consumers should select on Done() instead.
func (w *PeerConnectionWrapper) Close() error {
	var err error
	w.closeOnce.Do(func() {
		close(w.closed)
		err = w.pc.Close()
		// A claimed startup RTCP write belongs to another publisher's peer.
		// Closing this recipient cancels pending work, but must not join that
		// foreign write (Engine.Close may need to close its publisher next).
		w.logger.Info("webrtc peer connection closed",
			zap.String("client_id", w.clientID), zap.Error(err))
	})
	return err
}

// Done returns a channel that is closed when the wrapper is closed. Consumers
// of LocalCandidates should select on it to terminate, since LocalCandidates
// itself is never closed (see Close).
func (w *PeerConnectionWrapper) Done() <-chan struct{} {
	return w.closed
}

// handleLocalICECandidate is the OnICECandidate callback. It forwards each
// candidate to localCandidates; a nil candidate signals end-of-gathering.
// Sends fall through once the wrapper is closed.
func (w *PeerConnectionWrapper) handleLocalICECandidate(c *webrtc.ICECandidate) {
	if c == nil {
		w.logger.Debug("webrtc ice gathering complete",
			zap.String("client_id", w.clientID),
		)
		return
	}
	init := c.ToJSON()
	select {
	case w.localCandidates <- init:
	case <-w.closed:
	}
}

// handleTrack is the OnTrack callback. It logs the event and forwards to the
// user-registered handler if one is set.
func (w *PeerConnectionWrapper) handleTrack(track *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) {
	w.logger.Info("webrtc remote track received",
		zap.String("client_id", w.clientID),
		zap.String("track_id", track.ID()),
		zap.String("kind", track.Kind().String()),
		zap.Uint8("payload_type", uint8(track.PayloadType())),
	)
	if w.onTrack != nil {
		w.onTrack(track, receiver)
	}
}

// drainRemoteCandidates applies queued remote ICE candidates to the underlying
// PeerConnection. It exits when the wrapper is closed, preventing goroutine
// leaks.
func (w *PeerConnectionWrapper) drainRemoteCandidates() {
	for {
		select {
		case <-w.closed:
			return
		case c, ok := <-w.remoteCandidates:
			if !ok {
				return
			}
			if err := w.pc.AddICECandidate(c); err != nil {
				w.logger.Warn("webrtc failed to add remote ice candidate",
					zap.String("client_id", w.clientID),
					zap.String("candidate", c.Candidate),
					zap.Error(err),
				)
			}
		}
	}
}
