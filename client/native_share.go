package main

// native_share.go: native screen sharing (Windows Graphics Capture + Media
// Foundation H.264 + a publish-only pion peer "native"). The webview keeps
// the publication lifecycle (MsgVideoStreamControl via stream-publication.js),
// the share UI and the media key engine; it mirrors the "self|screen" key
// epochs here and reports generation/upload demand/codec. Frames never pass
// through the webview.

import (
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"runtime"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/webrtc/v4"

	"noxa/client/internal/nativecapture"
	"noxa/internal/framecrypt"
	"noxa/internal/nativesender"
	"noxa/internal/netproto"
)

const (
	nativeShareEvent       = "native_share"
	nativeReconnectLimit   = 3
	nativeDiagnosticsLimit = 15 * time.Second
)

// NativeShareRequest starts a native share for one surface and profile.
type NativeShareRequest struct {
	ID        string               `json:"id"` // the webview's share identity, echoed in events
	Source    nativecapture.Source `json:"source"`
	MaxWidth  int                  `json:"max_width"`
	MaxHeight int                  `json:"max_height"`
	FPS       int                  `json:"fps"`
	Bitrate   int                  `json:"bitrate"`
}

// NativeShareUpdate carries the publication state owned by the webview.
type NativeShareUpdate struct {
	ID           string `json:"id"`
	Generation   uint64 `json:"generation,string"`
	UploadActive bool   `json:"upload_active"`
	Codec        string `json:"codec"`
}

// NativeKeyOp mirrors one media-key engine operation for pid "self|screen".
type NativeKeyOp struct {
	Op    string `json:"op"` // key, activate, retire, retireOne, forget
	Epoch uint64 `json:"epoch"`
	Kid   uint8  `json:"kid"`
	Key   string `json:"key,omitempty"` // base64, 32 bytes
}

// NativeShareStatus tells the webview whether Native mode can be offered.
type NativeShareStatus struct {
	Available  bool                    `json:"available"`
	Reason     string                  `json:"reason,omitempty"`
	Monitors   []nativecapture.Monitor `json:"monitors"`
	Windows    []nativecapture.Window  `json:"windows"`
	Foreground uint64                  `json:"foreground,string"`
}

// nativeShareEventData is emitted as "native_share": started, minimized,
// restored, ended (window closed / stopped) or fallback (with reason).
type nativeShareEventData struct {
	Tab    string `json:"tab"`
	Share  string `json:"share"`
	State  string `json:"state"`
	Reason string `json:"reason,omitempty"`
}

// errNativeShareStopped: a resource finished opening after the share stopped.
var errNativeShareStopped = errors.New("native screen share stopped")

// nativeBitrateCap bounds the requested bitrate by the server's video limit,
// less the 15% RTP overhead reserve the webview's capVideoEncodings keeps.
// shortcut: the budget is not split with an active camera; the server's
// packet meter stays authoritative. Split it here if cameras share it.
func nativeBitrateCap(requested int, limits netproto.MediaLimits) int {
	bitrate := max(requested, 500_000)
	if limits.VideoMaxBitrate > 0 {
		bitrate = min(bitrate, limits.VideoMaxBitrate*85/100)
	}
	return bitrate
}

// nativeShareDecision is the pure fallback policy: what a failure leads to.
func nativeShareDecision(err error, restarts int) string {
	switch {
	case errors.Is(err, nativecapture.ErrSourceClosed):
		return "ended"
	case restarts < 1:
		return "restart"
	default:
		return "fallback"
	}
}

// nativeShareCodecFallback: the native encoder only produces H.264.
func nativeShareCodecFallback(codec string) bool { return codec != "" && codec != "h264" }

type nativeShare struct {
	cm   *connManager
	keys *framecrypt.Encryptor

	mu         sync.Mutex
	req        NativeShareRequest
	session    *nativecapture.Session
	sender     *nativesender.Sender
	ladder     *nativesender.Ladder
	level      nativesender.Level
	generation uint64
	upload     bool
	running    bool
	restarts   int
	reconnects int
	minimized  bool
	stop       chan struct{}
	prev       nativeSample
	badFrames  atomic.Int32
	// Server candidates that arrive while an offer is still in flight.
	pending []netproto.ICECandidate
}

type nativeSample struct {
	at                                 time.Time
	captured, encoded, sent, keyframes uint64
	bytes                              uint64
	retransmitBytes                    uint64
	encodeTime                         time.Duration
}

// nativeKeys returns this connection's mirrored screen-slot keys.
func (m *connManager) nativeKeys() *framecrypt.Encryptor {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.nativeKeyring == nil {
		m.nativeKeyring = framecrypt.NewEncryptor()
	}
	return m.nativeKeyring
}

func (m *connManager) nativeStatus() NativeShareStatus {
	m.mu.Lock()
	supported, e2ee := m.supportsNativeMediaPeer, m.supportsMediaE2EE
	m.mu.Unlock()
	st := NativeShareStatus{Monitors: nativecapture.Monitors(), Windows: nativecapture.Windows(), Foreground: nativecapture.ForegroundWindow()}
	switch {
	case runtime.GOOS != "windows":
		st.Reason = "unsupported_platform"
	case !supported:
		st.Reason = "server_outdated"
	case !e2ee:
		st.Reason = "media_e2ee_unavailable"
	default:
		st.Available = true
	}
	return st
}

// NativeScreenShareStatusForTab reports Native-mode availability and sources.
func (a *App) NativeScreenShareStatusForTab(tabID string) (NativeShareStatus, error) {
	cm, err := a.requireTabCM(tabID)
	if err != nil {
		return NativeShareStatus{}, err
	}
	return cm.nativeStatus(), nil
}

// NativeScreenKeyForTab mirrors one "self|screen" key operation.
func (a *App) NativeScreenKeyForTab(tabID string, op NativeKeyOp) error {
	cm, err := a.requireTabCM(tabID)
	if err != nil {
		return err
	}
	return applyNativeKeyOp(cm.nativeKeys(), op)
}

func applyNativeKeyOp(keys *framecrypt.Encryptor, op NativeKeyOp) error {
	switch op.Op {
	case "key":
		material, err := base64.StdEncoding.DecodeString(op.Key)
		if err != nil {
			return errors.New("invalid media key")
		}
		return keys.SetKey(op.Epoch, op.Kid, material)
	case "activate":
		return keys.Activate(op.Epoch)
	case "retire":
		keys.Retire(op.Epoch)
	case "retireOne":
		keys.RetireOne(op.Epoch)
	case "forget":
		keys.Forget()
	default:
		return errors.New("unknown media key operation")
	}
	return nil
}

// StartNativeScreenShareForTab opens capture, encoder and the native peer.
// Upload stays paused until UpdateNativeScreenShareForTab reports demand.
func (a *App) StartNativeScreenShareForTab(tabID string, req NativeShareRequest) error {
	cm, err := a.requireTabCM(tabID)
	if err != nil {
		return err
	}
	if st := cm.nativeStatus(); !st.Available {
		return fmt.Errorf("native screen capture unavailable: %s", st.Reason)
	}
	n := &nativeShare{cm: cm, keys: cm.nativeKeys(), req: req, stop: make(chan struct{})}
	// One swap: concurrent starts cannot both install a share. Installed before
	// the offer, so its answer's candidates route here; old.close() leaves a
	// newer cm.nativeShare alone and sends its empty offer before n's offer.
	cm.mu.Lock()
	old := cm.nativeShare
	cm.nativeShare = n
	cm.mu.Unlock()
	if old != nil {
		old.close()
	}
	if err := n.start(); err != nil {
		n.close()
		return err
	}
	return nil
}

// UpdateNativeScreenShareForTab applies generation, demand and codec.
func (a *App) UpdateNativeScreenShareForTab(tabID string, update NativeShareUpdate) error {
	n, err := a.nativeShareFor(tabID)
	if err != nil {
		return err
	}
	if n.req.ID != update.ID {
		return errors.New("stale native screen share")
	}
	return n.applyUpdate(update)
}

// applyUpdate adopts the webview's publication state. A stale update for an
// older generation never overrides a newer one.
func (n *nativeShare) applyUpdate(update NativeShareUpdate) error {
	n.mu.Lock()
	if update.Generation < n.generation {
		n.mu.Unlock()
		return errors.New("stale native share generation")
	}
	if nativeShareCodecFallback(update.Codec) {
		n.mu.Unlock()
		n.fallback("codec")
		return errors.New("publication codec is not H.264")
	}
	n.generation = update.Generation
	resume := update.UploadActive && !n.upload
	n.upload = update.UploadActive
	// Under n.mu (atomic stores): overlapping bridge calls reach the session
	// in the order they adopted their state.
	if n.session != nil {
		n.session.SetPaused(!update.UploadActive)
		if resume {
			n.session.RequestKeyframe()
		}
	}
	n.mu.Unlock()
	return nil
}

// StopNativeScreenShareForTab ends native share id and closes its peer; a
// stop for an older share never ends its replacement.
func (a *App) StopNativeScreenShareForTab(tabID, id string) error {
	cm, err := a.requireTabCM(tabID)
	if err != nil {
		return err
	}
	cm.mu.Lock()
	n := cm.nativeShare
	if n != nil && n.req.ID != id {
		n = nil
	}
	if n != nil {
		cm.nativeShare = nil
	}
	cm.mu.Unlock()
	if n != nil {
		n.close()
	}
	return nil
}

// NativeScreenDiagnosticsForTab returns the native sender's diagnostics row
// (the same netproto.VideoSenderDiagnostics as the webview's RTCStats rows).
func (a *App) NativeScreenDiagnosticsForTab(tabID string) (*netproto.VideoSenderDiagnostics, error) {
	n, err := a.nativeShareFor(tabID)
	if err != nil {
		return nil, err
	}
	return n.diagnostics(time.Now()), nil
}

func (a *App) nativeShareFor(tabID string) (*nativeShare, error) {
	cm, err := a.requireTabCM(tabID)
	if err != nil {
		return nil, err
	}
	cm.mu.Lock()
	n := cm.nativeShare
	cm.mu.Unlock()
	if n == nil {
		return nil, errors.New("no native screen share")
	}
	return n, nil
}

func (n *nativeShare) start() error {
	fps := max(1, min(n.req.FPS, 60))
	top := nativesender.Level{MaxWidth: n.req.MaxWidth, MaxHeight: n.req.MaxHeight, FPS: fps, Bitrate: nativeBitrateCap(n.req.Bitrate, n.cm.mediaLimitsSnapshot())}
	n.mu.Lock()
	n.ladder = nativesender.NewLadder(top, min(top.Bitrate, 2_500_000))
	n.level = n.ladder.Current()
	n.mu.Unlock()
	if err := n.openSender(); err != nil {
		return err
	}
	if err := n.openCapture(); err != nil {
		return err
	}
	n.mu.Lock()
	n.running = true
	n.prev = nativeSample{at: time.Now()}
	n.mu.Unlock()
	go n.watch()
	n.emit("started", "")
	return nil
}

func (n *nativeShare) emit(state, reason string) {
	n.cm.emit(nativeShareEvent, nativeShareEventData{Tab: n.cm.tabID, Share: n.req.ID, State: state, Reason: reason})
}

// stoppedLocked reports a started shutdown; the caller holds n.mu.
func (n *nativeShare) stoppedLocked() bool {
	select {
	case <-n.stop:
		return true
	default:
		return false
	}
}

// applyLimits re-bounds the bitrate after a server media limit change.
func (n *nativeShare) applyLimits(limits netproto.MediaLimits) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.ladder == nil {
		return
	}
	n.ladder.Top.Bitrate = nativeBitrateCap(n.req.Bitrate, limits)
	n.level.Bitrate = min(n.level.Bitrate, n.ladder.Top.Bitrate)
	if n.session != nil {
		n.session.SetBitrate(n.level.Bitrate)
	}
}

func (n *nativeShare) openCapture() error {
	n.mu.Lock()
	level := n.level
	n.mu.Unlock()
	session, err := nativecapture.Start(nativecapture.Config{Source: n.req.Source, MaxWidth: level.MaxWidth, MaxHeight: level.MaxHeight,
		FPS: level.FPS, Bitrate: level.Bitrate}, n.onFrame)
	if err != nil {
		return err
	}
	n.mu.Lock()
	if n.stoppedLocked() {
		n.mu.Unlock()
		session.Close()
		return errNativeShareStopped
	}
	session.SetPaused(!n.upload)
	n.session = session
	n.mu.Unlock()
	return nil
}

func (n *nativeShare) openSender() error {
	n.cm.mu.Lock()
	servers := slices.Clone(n.cm.iceServers)
	n.cm.mu.Unlock()
	ice := make([]webrtc.ICEServer, 0, len(servers))
	for _, s := range servers {
		ice = append(ice, webrtc.ICEServer{URLs: s.URLs, Username: s.Username, Credential: s.Credential})
	}
	n.mu.Lock()
	initial := n.level.Bitrate
	maxBitrate := n.ladder.Top.Bitrate
	n.mu.Unlock()
	sender, err := nativesender.New(nativesender.Config{ICEServers: ice, Signaling: nativeSignaling{n.cm, n}, Keys: n.keys,
		InitialBitrate: initial, MaxBitrate: maxBitrate,
		OnKeyframeRequest: n.requestKeyframe, OnEstimate: n.onEstimate})
	if err != nil {
		return err
	}
	sender.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		switch state {
		case webrtc.PeerConnectionStateConnected:
			n.peerConnected(sender)
		case webrtc.PeerConnectionStateFailed, webrtc.PeerConnectionStateClosed:
			n.peerLost(sender)
		}
	})
	n.mu.Lock()
	if n.stoppedLocked() {
		n.mu.Unlock()
		_ = sender.Close()
		// The server holds this late peer: release it unless a replacement
		// share (whose own offer supersedes it) is installed.
		n.cm.mu.Lock()
		idle := n.cm.nativeShare == nil
		n.cm.mu.Unlock()
		if idle {
			_ = n.cm.write(netproto.MsgWebRTCOffer, netproto.WebRTCOffer{Peer: netproto.PeerNative})
		}
		return errNativeShareStopped
	}
	n.sender = sender
	pending := n.pending
	n.pending = nil
	n.mu.Unlock()
	for _, c := range pending {
		_ = sender.AddCandidate(c)
	}
	return nil
}

func (n *nativeShare) requestKeyframe() {
	n.mu.Lock()
	session := n.session
	n.mu.Unlock()
	if session != nil {
		session.RequestKeyframe()
	}
}

func (n *nativeShare) onFrame(f nativecapture.Frame) {
	n.mu.Lock()
	sender, upload := n.sender, n.upload
	n.mu.Unlock()
	if sender == nil || !upload {
		return
	}
	err := sender.WriteFrame(f.Data, f.Keyframe, f.SeqHeader, f.Captured)
	if err == nil || errors.Is(err, nativesender.ErrDropped) {
		n.badFrames.Store(0)
		return
	}
	// The encoder ignores the one-slice/parameter-set rules: never publish
	// such frames; a second of them falls back to Browser capture.
	if n.badFrames.Add(1) == 30 {
		log.Printf("native share encoder output rejected: %v", err)
		go n.fallback("encoder")
	}
}

func (n *nativeShare) onEstimate(bps int) {
	n.mu.Lock()
	before := n.level
	n.level = n.ladder.Update(bps, time.Now())
	level, session := n.level, n.session
	n.mu.Unlock()
	if session == nil {
		return
	}
	session.SetBitrate(level.Bitrate)
	session.SetFPS(level.FPS)
	if level.MaxWidth != before.MaxWidth || level.MaxHeight != before.MaxHeight {
		session.SetMaxSize(level.MaxWidth, level.MaxHeight)
	}
}

// peerConnected restarts the reconnect budget: only consecutive failures
// count towards nativeReconnectLimit.
func (n *nativeShare) peerConnected(s *nativesender.Sender) {
	n.mu.Lock()
	if n.sender == s {
		n.reconnects = 0
	}
	n.mu.Unlock()
}

// peerLost re-offers while the share is active: the server closes the native
// peer without notice (browser peer rebuild, channel move, revocation).
func (n *nativeShare) peerLost(lost *nativesender.Sender) {
	n.mu.Lock()
	if !n.running || n.sender != lost {
		n.mu.Unlock()
		return
	}
	n.sender = nil
	n.reconnects++
	attempt := n.reconnects
	n.mu.Unlock()
	go func() {
		_ = lost.Close()
		if attempt > nativeReconnectLimit {
			n.fallback("peer")
			return
		}
		select {
		case <-n.stop:
			return
		case <-time.After(time.Duration(attempt) * time.Second):
		}
		if err := n.openSender(); err != nil {
			if errors.Is(err, errNativeShareStopped) {
				return
			}
			log.Printf("native share re-offer: %v", err)
			n.mu.Lock()
			n.sender = lost // let the next failure count another attempt
			n.mu.Unlock()
			n.peerLost(lost)
			return
		}
		n.requestKeyframe()
	}()
}

// watch handles pipeline failures and the minimized notice.
func (n *nativeShare) watch() {
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	for {
		n.mu.Lock()
		session := n.session
		n.mu.Unlock()
		var fatal <-chan error
		if session != nil {
			fatal = session.Fatal()
		}
		select {
		case <-n.stop:
			return
		case err := <-fatal:
			n.mu.Lock()
			decision := nativeShareDecision(err, n.restarts)
			n.restarts++
			n.session = nil
			n.mu.Unlock()
			session.Close()
			switch decision {
			case "ended":
				n.end("source_closed")
				return
			case "restart":
				log.Printf("native share restart after: %v", err)
				rerr := n.openCapture()
				if rerr == nil {
					continue
				}
				if errors.Is(rerr, errNativeShareStopped) {
					return
				}
				fallthrough
			default:
				n.fallback("capture")
				return
			}
		case <-tick.C:
			if session == nil {
				continue
			}
			minimized := session.Stats().Minimized
			n.mu.Lock()
			changed := minimized != n.minimized
			n.minimized = minimized
			n.mu.Unlock()
			if changed {
				state := "restored"
				if minimized {
					state = "minimized"
				}
				n.emit(state, "")
			}
		}
	}
}

func (n *nativeShare) end(reason string) {
	n.close()
	n.emit("ended", reason)
}

func (n *nativeShare) fallback(reason string) {
	n.close()
	n.emit("fallback", reason)
}

// close stops capture and closes the native peer (an empty-SDP native offer
// tells the server; no acknowledgement). The publication is the webview's.
func (n *nativeShare) close() {
	n.mu.Lock()
	wasRunning := n.running
	n.running = false
	session, sender := n.session, n.sender
	n.session, n.sender = nil, nil
	select {
	case <-n.stop:
	default:
		close(n.stop)
	}
	n.mu.Unlock()
	if session != nil {
		session.Close()
	}
	if sender != nil {
		_ = sender.Close()
	}
	if wasRunning || sender != nil {
		_ = n.cm.write(netproto.MsgWebRTCOffer, netproto.WebRTCOffer{Peer: netproto.PeerNative})
	}
	n.cm.mu.Lock()
	if n.cm.nativeShare == n {
		n.cm.nativeShare = nil
	}
	n.cm.mu.Unlock()
}

// diagnostics builds one sender row from counters since the previous call.
func (n *nativeShare) diagnostics(now time.Time) *netproto.VideoSenderDiagnostics {
	n.mu.Lock()
	session, sender, prev, level, generation := n.session, n.sender, n.prev, n.level, n.generation
	n.mu.Unlock()
	if session == nil || sender == nil || generation == 0 {
		return nil
	}
	cs, ss, info := session.Stats(), sender.Stats(), session.Info()
	cur := nativeSample{at: now, captured: cs.Captured, encoded: cs.Encoded, sent: ss.FramesSent, keyframes: cs.Keyframes,
		bytes: ss.BytesSent, retransmitBytes: ss.RetransmittedBytes, encodeTime: cs.EncodeTime}
	n.mu.Lock()
	n.prev = cur
	n.mu.Unlock()
	row := nativeDiagnosticsRow(prev, cur, cs, ss, info, level, generation)
	if !row.Valid() {
		return nil
	}
	return &row
}

func nativeDiagnosticsRow(prev, cur nativeSample, cs nativecapture.Stats, ss nativesender.Stats, info nativecapture.EncoderInfo, level nativesender.Level, generation uint64) netproto.VideoSenderDiagnostics {
	f := func(v float64) *float64 { return &v }
	b := func(v bool) *bool { return &v }
	row := netproto.VideoSenderDiagnostics{SSRC: ss.SSRC, Slot: "screen", Generation: generation, Codec: "video/h264",
		EncoderImplementation: "MediaFoundationVideoEncoder", EncoderVendor: info.Vendor, PowerEfficient: b(info.Hardware),
		CaptureBackend: "native", QualityReason: "none",
		RequestedFPS: f(float64(level.FPS)), SettingsFPS: f(float64(level.FPS)),
		Width: f(float64(cs.Width)), Height: f(float64(cs.Height)),
		TargetBitrateBPS: f(float64(level.Bitrate)),
		FramesEncoded:    f(float64(cs.Encoded)), FramesSent: f(float64(ss.FramesSent)), KeyFrames: f(float64(min(cs.Keyframes, cs.Encoded))),
		PacketsSent: f(float64(ss.PacketsSent)), BytesSent: f(float64(ss.BytesSent)),
		RetransmittedPackets: f(float64(min(ss.RetransmittedPackets, ss.PacketsSent))), RetransmittedBytes: f(float64(min(ss.RetransmittedBytes, ss.BytesSent)))}
	if cs.Width > 0 {
		row.CaptureWidth, row.CaptureHeight = f(float64(cs.Width)), f(float64(cs.Height))
	}
	if level.Bitrate < 4_000_000 && level.FPS < 60 {
		row.QualityReason = "bandwidth"
	}
	if ss.EstimateBPS > 0 {
		row.AvailableOutgoingBitrateBPS = f(float64(min(ss.EstimateBPS, 1_000_000_000)))
	}
	if ss.HasRemote {
		row.RemoteRTTMS = f(min(float64(ss.RTT.Milliseconds()), 60000))
		row.RemoteFractionLost = f(min(max(ss.FractionLost, 0), 1))
	}
	elapsed := cur.at.Sub(prev.at)
	if prev.at.IsZero() || elapsed <= 0 || elapsed > nativeDiagnosticsLimit || cur.encoded < prev.encoded || cur.bytes < prev.bytes {
		return row
	}
	ms := float64(elapsed.Milliseconds())
	rate := func(a, b uint64) *float64 { return f(min(float64(a-b)*1000/ms, 240)) }
	row.SampleMS = f(ms)
	row.CaptureFPS, row.EncodedFPS, row.SentFPS = rate(cur.captured, prev.captured), rate(cur.encoded, prev.encoded), rate(cur.sent, prev.sent)
	row.BitrateBPS = f(min(float64(cur.bytes-prev.bytes)*8000/ms, 1e9))
	if kd := cur.keyframes - prev.keyframes; cur.keyframes >= prev.keyframes && kd <= cur.encoded-prev.encoded {
		row.KeyFramesDelta = f(float64(kd))
	}
	if frames := cur.encoded - prev.encoded; frames > 0 {
		row.EncodeMS = f(min(float64((cur.encodeTime-prev.encodeTime).Milliseconds())/float64(frames), 60000))
		row.FrameBytes = f(float64(cur.bytes-prev.bytes) / float64(frames))
	}
	if cur.retransmitBytes >= prev.retransmitBytes && cur.bytes > prev.bytes {
		retry := float64(cur.retransmitBytes - prev.retransmitBytes)
		if retry <= float64(cur.bytes-prev.bytes) {
			row.RetransmitBitrateBPS = f(retry * 8000 / ms)
			row.RetransmitPercent = f(retry * 100 / float64(cur.bytes-prev.bytes))
		}
	}
	return row
}

// nativeSignaling sends the native peer's offer and candidates on the
// control connection; the reply must be the native answer.
type nativeSignaling struct {
	cm    *connManager
	share *nativeShare
}

// current: a replaced share must not signal over its replacement.
func (s nativeSignaling) current() bool {
	s.cm.mu.Lock()
	defer s.cm.mu.Unlock()
	return s.cm.nativeShare == s.share
}

func (s nativeSignaling) Offer(offer netproto.WebRTCOffer) (netproto.WebRTCAnswer, error) {
	if !s.current() {
		return netproto.WebRTCAnswer{}, errNativeShareStopped
	}
	f, err := s.cm.request(netproto.MsgWebRTCOffer, netproto.MsgWebRTCAnswer, offer, 10*time.Second)
	if err != nil {
		return netproto.WebRTCAnswer{}, err
	}
	var answer netproto.WebRTCAnswer
	if err := decodeJSON(f, &answer); err != nil {
		return netproto.WebRTCAnswer{}, err
	}
	if answer.Peer != netproto.PeerNative {
		return netproto.WebRTCAnswer{}, errors.New("answer is not for the native peer")
	}
	return answer, nil
}

func (s nativeSignaling) Candidate(c netproto.ICECandidate) error {
	if !s.current() {
		return errNativeShareStopped
	}
	return s.cm.write(netproto.MsgICECandidate, c)
}

// routeNativeCandidate delivers a server ICE candidate tagged for the native
// peer; it reports whether the frame belonged to the native peer.
func (m *connManager) routeNativeCandidate(c netproto.ICECandidate) bool {
	if c.Peer != netproto.PeerNative {
		return false
	}
	m.mu.Lock()
	n := m.nativeShare
	m.mu.Unlock()
	if n == nil {
		return true
	}
	n.mu.Lock()
	sender := n.sender
	if sender == nil && len(n.pending) < 64 {
		n.pending = append(n.pending, c)
	}
	n.mu.Unlock()
	if sender != nil {
		if err := sender.AddCandidate(c); err != nil {
			log.Printf("native share candidate: %v", err)
		}
	}
	return true
}
