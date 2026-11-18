package main

// native_share.go: native screen sharing (Windows Graphics Capture + Media
// Foundation H.264 + a publish-only pion peer "native"). The webview keeps
// the publication lifecycle (MsgVideoStreamControl via stream-publication.js),
// the share UI and the media key engine; it mirrors the "self|screen" key
// epochs here and reports generation/upload demand/codec. Frames never pass
// through the webview.

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"log"
	"runtime"
	"slices"
	"strconv"
	"strings"
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
	// Audio is the dialog's choice: "none", "application" or "system".
	// A Studio share (Source.Studio) takes its audio from the scene instead.
	Audio string `json:"audio,omitempty"`
	// Studio is the first program scene of a Studio share.
	Studio *NativeStudioScene `json:"studio,omitempty"`
}

// NativeStudioScene is a Studio program scene with its overlay images
// (PNG, rendered by the webview at the program size).
type NativeStudioScene struct {
	nativecapture.Scene
	Overlays [][]byte `json:"overlays"`
}

// Overlay image bounds (encoded bytes, pixels per side).
const (
	nativeOverlayBytes = 8 << 20
	nativeOverlaySide  = 3840
)

// decodeOverlays decodes the webview's overlay PNGs; anything malformed or
// oversized is refused, never half applied.
func decodeOverlays(pngs [][]byte) ([]*image.NRGBA, error) {
	if len(pngs) > nativecapture.MaxStudioLayers {
		return nil, errors.New("too many overlay images")
	}
	out := make([]*image.NRGBA, len(pngs))
	for i, data := range pngs {
		if len(data) > nativeOverlayBytes {
			return nil, errors.New("overlay image too large")
		}
		cfg, err := png.DecodeConfig(bytes.NewReader(data))
		if err != nil || cfg.Width < 1 || cfg.Height < 1 || cfg.Width > nativeOverlaySide || cfg.Height > nativeOverlaySide {
			return nil, errors.New("invalid overlay image")
		}
		img, err := png.Decode(bytes.NewReader(data))
		if err != nil {
			return nil, errors.New("invalid overlay image")
		}
		nrgba, ok := img.(*image.NRGBA)
		if !ok {
			nrgba = image.NewNRGBA(img.Bounds())
			draw.Draw(nrgba, nrgba.Bounds(), img, img.Bounds().Min, draw.Src)
		}
		out[i] = nrgba
	}
	return out, nil
}

// studioAudio is the audio a Studio program streams: none without a visible
// display source with audio on; that window's own audio when it is the only
// one and a window; system audio (minus noXa) otherwise.
func studioAudio(scene nativecapture.Scene) (string, uint64) {
	var sources []nativecapture.Source
	for _, l := range scene.Layers {
		if l.Kind == nativecapture.LayerDisplay && l.Audio && !slices.Contains(sources, l.Source) {
			sources = append(sources, l.Source)
		}
	}
	switch {
	case len(sources) == 0:
		return string(nativecapture.AudioNone), 0
	case len(sources) == 1 && sources[0].Window != 0:
		return string(nativecapture.AudioApplication), sources[0].Window
	default:
		return string(nativecapture.AudioSystem), 0
	}
}

// NativeShareUpdate carries the publication state owned by the webview.
type NativeShareUpdate struct {
	ID           string `json:"id"`
	Generation   uint64 `json:"generation,string"`
	UploadActive bool   `json:"upload_active"`
	Codec        string `json:"codec"`
}

// NativeKeyOp mirrors one media-key engine operation for pid "self|screen"
// (Slot "" or "screen") or "self|screenaudio" (Slot "screenaudio").
// Call set: the key belongs to that private call's media session instead.
type NativeKeyOp struct {
	Slot  string `json:"slot,omitempty"`
	Call  string `json:"call,omitempty"`
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
	// CallAvailable: a private call share is possible (it needs no server peer).
	CallAvailable bool `json:"call_available"`
}

// nativeShareEventData is emitted as "native_share": started, minimized,
// restored, ended (window closed / stopped), fallback (with reason) or audio
// (reason: the audio actually captured when it differs from the request:
// "system" or "none").
// Studio shares also emit camera (reason "busy" or "unavailable") when a
// scene camera cannot be opened.
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

// nativeCameraReason is the "camera" event reason for a Studio camera failure.
func nativeCameraReason(msg string) string {
	if strings.Contains(msg, nativecapture.ErrCameraBusy.Error()) {
		return "busy"
	}
	return "unavailable"
}

// nativeShareCodecFallback: the native encoder only produces H.264.
func nativeShareCodecFallback(codec string) bool { return codec != "" && codec != "h264" }

type nativeShare struct {
	cm        *connManager
	keys      *framecrypt.Encryptor
	audioKeys *framecrypt.Encryptor

	mu         sync.Mutex
	req        NativeShareRequest
	session    *nativecapture.Session
	audio      *nativecapture.AudioSession
	sender     *nativesender.Sender
	ladder     *nativesender.Ladder
	level      nativesender.Level
	generation uint64
	upload     bool
	running    bool
	// Studio: the scene on air (re-applied after a restart); the audio plan.
	scene       nativecapture.Scene
	overlays    []*image.NRGBA
	audioMode   string
	audioWindow uint64
	cameraErr   string
	restarts    int
	reconnects  int
	minimized   bool
	stop        chan struct{}
	prev        nativeSample
	badFrames   atomic.Int32
	frames      nativeFrameDiag
	fellBack    atomic.Bool // fallback runs once: onFrame, peerLost and watch can all trigger it
	// Server candidates that arrive while an offer is still in flight.
	pending []netproto.ICECandidate
	// call: a private call share (native_call_share.go) sends to its viewers
	// over screen sub-connections instead of the server's native peer.
	call *nativeCall
}

type nativeSample struct {
	at                                 time.Time
	captured, encoded, sent, keyframes uint64
	bytes                              uint64
	retransmitBytes                    uint64
	encodeTime                         time.Duration
}

// nativeKeys returns this connection's mirrored keys of slot "screen" (or
// "") or "screenaudio".
func (m *connManager) nativeKeys(slot string) *framecrypt.Encryptor {
	m.mu.Lock()
	defer m.mu.Unlock()
	ring := &m.nativeKeyring
	if slot == "screenaudio" {
		ring = &m.nativeAudioKeyring
	}
	if *ring == nil {
		*ring = framecrypt.NewEncryptor()
	}
	return *ring
}

func (m *connManager) nativeStatus() NativeShareStatus {
	m.mu.Lock()
	supported, e2ee := m.supportsNativeMediaPeer, m.supportsMediaE2EE
	m.mu.Unlock()
	st := NativeShareStatus{Monitors: nativecapture.Monitors(), Windows: nativecapture.Windows(), Foreground: nativecapture.ForegroundWindow(),
		CallAvailable: nativeCallAvailable(e2ee)}
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

// NativeScreenKeyForTab mirrors one "self|screen" or "self|screenaudio" key operation.
func (a *App) NativeScreenKeyForTab(tabID string, op NativeKeyOp) error {
	cm, err := a.requireTabCM(tabID)
	if err != nil {
		return err
	}
	if op.Slot != "" && op.Slot != "screen" && op.Slot != "screenaudio" {
		return errors.New("unknown media key slot")
	}
	if op.Call != "" {
		return applyNativeKeyOp(cm.callKeys(op.Call, op.Slot), op)
	}
	return applyNativeKeyOp(cm.nativeKeys(op.Slot), op)
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
	n := &nativeShare{cm: cm, keys: cm.nativeKeys("screen"), audioKeys: cm.nativeKeys("screenaudio"), req: req, stop: make(chan struct{}),
		audioMode: req.Audio, audioWindow: req.Source.Window}
	switch {
	case req.Source.Studio && req.Studio == nil:
		return errors.New("studio scene missing")
	case req.Source.Studio:
		overlays, err := decodeOverlays(req.Studio.Overlays)
		if err != nil {
			return err
		}
		n.scene, n.overlays = req.Studio.Scene, overlays
		n.audioMode, n.audioWindow = studioAudio(req.Studio.Scene)
		n.req.Studio = nil // the decoded copy is kept
	case req.Studio != nil:
		return errors.New("scene for a non-Studio share")
	}
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

// UpdateNativeStudioForTab puts a Studio Take (or new overlays of the live
// Take) on air and follows its audio.
func (a *App) UpdateNativeStudioForTab(tabID, id string, scene NativeStudioScene) error {
	n, err := a.nativeShareFor(tabID)
	if err != nil {
		return err
	}
	if n.req.ID != id || !n.req.Source.Studio {
		return errors.New("stale native screen share")
	}
	overlays, err := decodeOverlays(scene.Overlays)
	if err != nil {
		return err
	}
	n.setScene(scene.Scene, overlays)
	return nil
}

func (n *nativeShare) setScene(scene nativecapture.Scene, overlays []*image.NRGBA) {
	mode, window := studioAudio(scene)
	n.mu.Lock()
	if scene.Take < n.scene.Take {
		n.mu.Unlock()
		return // a delayed older Take
	}
	n.scene, n.overlays = scene, overlays
	if n.session != nil {
		n.session.SetScene(scene, overlays)
	}
	changed := mode != n.audioMode || window != n.audioWindow
	n.audioMode, n.audioWindow = mode, window
	old := n.audio
	if changed {
		n.audio = nil
	}
	running := n.running
	n.mu.Unlock()
	if !changed {
		return
	}
	// Audio follows the Take like the picture: the old source stops first.
	if old != nil {
		old.Close()
	}
	if running {
		n.audioStarted(n.openAudio())
	}
}

// NativeCameras lists the cameras a native Studio can compose.
func (a *App) NativeCameras() []nativecapture.Camera { return nativecapture.Cameras() }

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
		log.Printf("native share publication codec %q is not H.264", update.Codec)
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
	// Audio first: whether the native offer carries a screenaudio track.
	notice := n.openAudio()
	if err := n.openSender(); err != nil {
		log.Printf("native share start: peer: %v", err)
		return err
	}
	if err := n.openCapture(); err != nil {
		// The native peer was already offered: without this line the server
		// shows an established peer that never sends.
		log.Printf("native share start: capture: %v", err)
		return err
	}
	n.mu.Lock()
	n.running = true
	n.prev = nativeSample{at: time.Now()}
	n.mu.Unlock()
	go n.watch()
	n.emit("started", "")
	n.audioStarted(notice)
	return nil
}

// audioStarted reports an audio fallback, then watches the audio session:
// only after the notice, so a later "none" stays final.
func (n *nativeShare) audioStarted(notice string) {
	if notice != "" {
		n.emit("audio", notice)
	}
	n.mu.Lock()
	session := n.audio
	n.mu.Unlock()
	if session != nil {
		if failed := session.Failed(); failed != nil {
			go n.watchAudio(session, failed)
		}
	}
}

// startNativeAudio is nativecapture.StartAudio; tests replace it.
var startNativeAudio = nativecapture.StartAudio

// openAudio starts the first source of the request's audio plan that works
// and returns the captured mode when it differs from the request ("" when
// it matches): application audio falls back to system audio minus noXa,
// and a missing libopus or loopback leaves the share without audio.
func (n *nativeShare) openAudio() string {
	n.mu.Lock()
	requested, window := n.audioMode, n.audioWindow
	n.mu.Unlock()
	plan := nativecapture.AudioPlan(requested, window != 0)
	for _, mode := range plan {
		session, err := startNativeAudio(mode, window, n.onAudio)
		if err != nil {
			log.Printf("native share audio %s: %v", mode, err)
			if errors.Is(err, nativecapture.ErrOpusUnavailable) {
				break // no encoder: no other source helps
			}
			continue
		}
		n.mu.Lock()
		if n.stoppedLocked() || n.audioMode != requested || n.audioWindow != window {
			// Stopped, or a newer Take changed the plan meanwhile.
			n.mu.Unlock()
			session.Close()
			return ""
		}
		n.audio = session
		n.mu.Unlock()
		if mode != nativecapture.AudioMode(requested) {
			return string(mode)
		}
		return ""
	}
	if len(plan) == 0 {
		return ""
	}
	return string(nativecapture.AudioNone)
}

// watchAudio ends a share's audio whose capture broke for good (capture
// already retried once): the share continues silent and the webview shows
// the "audio unavailable" notice.
func (n *nativeShare) watchAudio(session *nativecapture.AudioSession, failed <-chan error) {
	select {
	case <-n.stop:
	case err := <-failed:
		n.mu.Lock()
		current := n.audio == session
		if current {
			n.audio = nil
		}
		n.mu.Unlock()
		if !current {
			return
		}
		log.Printf("native share audio stopped: %v", err)
		session.Close()
		n.emit("audio", string(nativecapture.AudioNone))
	}
}

func (n *nativeShare) onAudio(p nativecapture.AudioPacket) {
	n.mu.Lock()
	sender, upload := n.sender, n.upload
	n.mu.Unlock()
	if n.call != nil {
		n.call.writeAudio(p)
		return
	}
	if sender != nil && upload {
		if err := sender.WriteAudio(p.Data, p.Captured); err != nil { // a keyless packet is dropped, never sent
			n.frames.note("audio", err)
		}
	}
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
	n.mu.Lock()
	scene, overlays := n.scene, n.overlays
	n.mu.Unlock()
	session, err := nativecapture.Start(nativecapture.Config{Source: n.req.Source, MaxWidth: level.MaxWidth, MaxHeight: level.MaxHeight,
		FPS: level.FPS, Bitrate: level.Bitrate, Scene: scene, Overlays: overlays,
		// The Studio canvas keeps the requested size; the ladder only scales its output.
		CanvasWidth: n.req.MaxWidth, CanvasHeight: n.req.MaxHeight}, n.onFrame)
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
	// A Take or overlay refresh may have arrived while the capture opened.
	session.SetScene(n.scene, n.overlays)
	n.session = session
	n.mu.Unlock()
	return nil
}

func (n *nativeShare) openSender() error {
	if n.call != nil {
		return n.call.open()
	}
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
	n.mu.Lock()
	var audioKeys *framecrypt.Encryptor
	// A Studio always offers the audio track: a later Take may add audio.
	if n.audio != nil || n.req.Source.Studio {
		audioKeys = n.audioKeys
	}
	n.mu.Unlock()
	sender, err := nativesender.New(nativesender.Config{ICEServers: ice, Signaling: nativeSignaling{n.cm, n}, Keys: n.keys, AudioKeys: audioKeys,
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
	var err error
	switch {
	case !upload:
		n.frames.unsent.Add(1)
		return
	case n.call != nil:
		err = n.call.writeFrame(f)
	case sender == nil:
		n.frames.unsent.Add(1)
		return
	default:
		err = sender.WriteFrame(f.Data, f.Keyframe, f.SeqHeader, f.Captured)
	}
	if err != nil {
		n.frames.note(dropKind(err), err)
	}
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

// watch handles pipeline failures, the minimized notice, the stats lines
// and the stall watchdog.
func (n *nativeShare) watch() {
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	started := time.Now()
	logged := 0
	var stall nativeStallWatch
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
				log.Printf("native share restart failed: %v", rerr)
				fallthrough
			default:
				if decision != "restart" {
					log.Printf("native share capture failed again: %v", err)
				}
				n.fallback("capture")
				return
			}
		case <-tick.C:
			if session == nil {
				continue
			}
			stats := session.Stats()
			minimized := stats.Minimized
			n.mu.Lock()
			changed := minimized != n.minimized
			n.minimized = minimized
			cameraChanged := stats.CameraError != n.cameraErr
			n.cameraErr = stats.CameraError
			n.mu.Unlock()
			if cameraChanged && stats.CameraError != "" {
				n.emit("camera", nativeCameraReason(stats.CameraError))
			}
			if changed {
				state := "restored"
				if minimized {
					state = "minimized"
				}
				n.emit(state, "")
			}
			now := time.Now()
			snap, reason := n.observe(&stall, session, stats, now.Sub(started), now)
			if now.Sub(started) >= nativeStatsAt(logged) {
				logged++
				log.Printf("native share stats %s", snap)
			}
			if reason != "" {
				log.Printf("native share stalled (%s): %s", reason, snap)
				n.fallback("stalled")
				return
			}
		}
	}
}

// nativeStallWatch is the watchdog's memory between ticks (watch goroutine only).
type nativeStallWatch struct {
	nativeStallState
	session *nativecapture.Session
	sender  *nativesender.Sender
	packets uint64
	noKey   uint64
}

// observe takes one stats snapshot and advances the stall watchdog.
func (n *nativeShare) observe(w *nativeStallWatch, session *nativecapture.Session, cs nativecapture.Stats, elapsed time.Duration, now time.Time) (nativeShareSnapshot, string) {
	n.mu.Lock()
	sender, upload, generation := n.sender, n.upload, n.generation
	n.mu.Unlock()
	snap := nativeShareSnapshot{elapsed: elapsed, peer: "none", upload: upload, generation: generation, cs: cs, encoder: session.Info(),
		unsent: n.frames.unsent.Load(), rejected: n.frames.rejected.Load(), dropped: n.frames.dropped.Load(), noKey: n.frames.noKey.Load()}
	n.frames.mu.Lock()
	snap.last = n.frames.last
	n.frames.mu.Unlock()
	if n.call != nil {
		snap.peer = "call"
		return snap, "" // call viewers come and go; their links recover themselves
	}
	if sender == nil {
		w.sender = nil // between re-offers: peerLost owns the recovery
		return snap, ""
	}
	state := sender.ConnectionState()
	snap.peer, snap.ss = state.String(), sender.Stats()
	if sender != w.sender {
		// A new peer (start or re-offer): its offer has just been answered.
		w.nativeStallState = nativeStallState{Offered: now, Fired: w.Fired}
		w.sender, w.packets = sender, snap.ss.VideoPackets
	}
	if w.Connected.IsZero() && state == webrtc.PeerConnectionStateConnected {
		w.Connected = now
	}
	if snap.ss.VideoPackets != w.packets {
		w.packets, w.LastVideo = snap.ss.VideoPackets, now
	}
	// Paused (no viewer demand), minimized, or without an active media key
	// (Browser capture would wait for the same key): idle by design. A
	// restarted capture session gets a fresh window too.
	keyless := snap.noKey != w.noKey
	w.noKey = snap.noKey
	w.Idle = !upload || generation == 0 || cs.Minimized || keyless
	if w.Idle || session != w.session {
		w.session, w.Resumed = session, now
	}
	reason := nativeStallReason(w.nativeStallState, now)
	if reason != "" {
		w.Fired = true
	}
	return snap, reason
}

func (n *nativeShare) end(reason string) {
	log.Printf("native share ended: %s", reason)
	n.close()
	n.emit("ended", reason)
}

func (n *nativeShare) fallback(reason string) {
	if !n.fellBack.CompareAndSwap(false, true) {
		return
	}
	log.Printf("native share falling back to Browser capture: %s", reason)
	n.close()
	n.emit("fallback", reason)
}

// close stops capture and closes the native peer (an empty-SDP native offer
// tells the server; no acknowledgement). The publication is the webview's.
func (n *nativeShare) close() {
	n.mu.Lock()
	wasRunning := n.running
	n.running = false
	session, sender, audio := n.session, n.sender, n.audio
	n.session, n.sender, n.audio = nil, nil, nil
	select {
	case <-n.stop:
	default:
		close(n.stop)
	}
	n.mu.Unlock()
	if session != nil {
		session.Close()
	}
	if audio != nil {
		audio.Close()
	}
	if sender != nil {
		_ = sender.Close()
	}
	if n.call != nil {
		n.call.close()
	} else if wasRunning || sender != nil {
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

// nativeThumbnailLimit bounds one picker refresh.
const nativeThumbnailLimit = 32

// nativePickerSource parses a picker value: "m:<monitor index>" or "w:<window handle>".
func nativePickerSource(value string) (nativecapture.Source, bool) {
	kind, id, ok := strings.Cut(value, ":")
	if !ok {
		return nativecapture.Source{}, false
	}
	switch kind {
	case "m":
		if i, err := strconv.Atoi(id); err == nil && i >= 0 && i < 64 && id[0] != '+' {
			return nativecapture.Source{Monitor: i}, true
		}
	case "w":
		if n, err := strconv.ParseUint(id, 10, 64); err == nil && n != 0 {
			return nativecapture.Source{Window: n}, true
		}
	}
	return nativecapture.Source{}, false
}

// NativeScreenThumbnails renders picker thumbnails (JPEG data URLs) for
// picker values; sources that cannot be drawn (minimized, hung) are absent.
func (a *App) NativeScreenThumbnails(values []string) map[string]string {
	var sources []nativecapture.Source
	byValue := map[nativecapture.Source]string{}
	for _, value := range values {
		if src, ok := nativePickerSource(value); ok && len(sources) < nativeThumbnailLimit {
			if _, dup := byValue[src]; !dup {
				sources = append(sources, src)
				byValue[src] = value
			}
		}
	}
	out := map[string]string{}
	for src, jpg := range nativecapture.Thumbnails(sources, 320, 180, time.Second) {
		out[byValue[src]] = "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(jpg)
	}
	return out
}

// NativeScreenPreview renders one picker source large enough to draw a
// Region on (a JPEG data URL; "" when it cannot be drawn).
func (a *App) NativeScreenPreview(value string) string {
	src, ok := nativePickerSource(value)
	if !ok {
		return ""
	}
	jpg := nativecapture.Thumbnails([]nativecapture.Source{src}, 1280, 720, 2*time.Second)[src]
	if len(jpg) == 0 {
		return ""
	}
	return "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(jpg)
}
