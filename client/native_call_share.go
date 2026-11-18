package main

// native_call_share.go: a native screen share inside a private call. The
// capture, encoder and audio pipeline are the channel share's (nativeShare);
// instead of the server's publish-only "native" peer, every viewer gets its
// own relay-only peer-to-peer connection, the call's "screen"
// sub-connection, negotiated over the call's sealed signals. Frames carry
// the call's media keys ("self|screen" of the call's media session, mirrored
// from the webview like the channel's).

import (
	"encoding/json"
	"errors"
	"log"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/pion/webrtc/v4"

	"noxa/client/internal/nativecapture"
	"noxa/internal/framecrypt"
	"noxa/internal/nativesender"
	"noxa/internal/netproto"
)

// nativeCallAnswerTimeout bounds one viewer's answer (it travels through the
// server and the viewer's webview).
const nativeCallAnswerTimeout = 15 * time.Second

// NativeCallShareRequest starts a native share into private call CallID,
// streamed to the accepted participants Targets (those that advertised
// callFeatureScreenSub).
type NativeCallShareRequest struct {
	NativeShareRequest
	CallID  string   `json:"call_id"`
	Targets []string `json:"targets"`
}

// nativeCallKeys are the mirrored "self|screen" and "self|screenaudio" keys
// of one private call's media session.
type nativeCallKeys struct {
	callID       string
	video, audio *framecrypt.Encryptor
}

// callKeys returns call callID's keyring of slot ("screen"/"" or
// "screenaudio"). Another call's keys are dropped: an earlier call's epoch
// never encrypts this one's frames.
func (m *connManager) callKeys(callID, slot string) *framecrypt.Encryptor {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.nativeCallKeys == nil || m.nativeCallKeys.callID != callID {
		m.nativeCallKeys = &nativeCallKeys{callID: callID, video: framecrypt.NewEncryptor(), audio: framecrypt.NewEncryptor()}
	}
	if slot == "screenaudio" {
		return m.nativeCallKeys.audio
	}
	return m.nativeCallKeys.video
}

// nativeCallAvailable: a call share needs no server peer, only Windows
// capture and the session's encrypted media.
func nativeCallAvailable(e2ee bool) bool { return runtime.GOOS == "windows" && nativecapture.Supported() && e2ee }

// callRelayServers is the relay-only ICE configuration of a call's screen
// sub-connection, the webview's privateCallRTCConfiguration: TURN entries
// with credentials only; none means no private-call media at all.
func callRelayServers(servers []netproto.ICEServer) ([]webrtc.ICEServer, error) {
	var out []webrtc.ICEServer
	for _, s := range servers {
		turn := slices.ContainsFunc(s.URLs, func(url string) bool {
			url = strings.ToLower(url)
			return strings.HasPrefix(url, "turn:") || strings.HasPrefix(url, "turns:")
		})
		if turn && s.Username != "" && s.Credential != "" {
			out = append(out, webrtc.ICEServer{URLs: slices.Clone(s.URLs), Username: s.Username, Credential: s.Credential})
		}
	}
	if len(out) == 0 {
		return nil, errors.New("private calls require an available TURN relay")
	}
	return out, nil
}

// StartNativeCallShareForTab opens capture, encoder and one screen
// sub-connection per target of private call req.CallID.
func (a *App) StartNativeCallShareForTab(tabID string, req NativeCallShareRequest) error {
	cm, err := a.requireTabCM(tabID)
	if err != nil {
		return err
	}
	cm.mu.Lock()
	e2ee, conn := cm.supportsMediaE2EE, cm.conn
	cm.mu.Unlock()
	if !nativeCallAvailable(e2ee) {
		return errors.New("native screen capture unavailable")
	}
	if parsed, err := uuid.Parse(req.CallID); err != nil || parsed.String() != req.CallID {
		return errors.New("invalid call")
	}
	if req.Source.Studio || req.Studio != nil {
		return errors.New("a Studio cannot be shared into a private call")
	}
	// Every connection attempt reads the current relay lease: reconnects and
	// joiners of a long share must not use expired TURN credentials.
	servers := func() ([]webrtc.ICEServer, error) {
		_ = cm.refreshRelay(conn) // keep the cached relay during a transient renewal failure
		return callRelayServers(cm.iceServersSnapshot())
	}
	if _, err := servers(); err != nil {
		return err
	}
	n := &nativeShare{cm: cm, keys: cm.callKeys(req.CallID, "screen"), audioKeys: cm.callKeys(req.CallID, "screenaudio"),
		req: req.NativeShareRequest, stop: make(chan struct{}), audioMode: req.Audio, audioWindow: req.Source.Window,
		// No publication: a call viewer gets frames as soon as it connects.
		upload: true, generation: 1}
	n.call = &nativeCall{share: n, callID: req.CallID, servers: servers, policy: webrtc.ICETransportPolicyRelay, peers: map[string]*nativeCallPeer{}}
	if err := n.call.setTargets(req.Targets); err != nil {
		return err
	}
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

// UpdateNativeCallShareTargetsForTab follows the call's membership: joiners
// that advertised the screen sub-connection get one, leavers lose theirs.
func (a *App) UpdateNativeCallShareTargetsForTab(tabID, id string, targets []string) error {
	n, err := a.nativeShareFor(tabID)
	if err != nil {
		return err
	}
	if n.req.ID != id || n.call == nil {
		return errors.New("stale native screen share")
	}
	return n.call.setTargets(targets)
}

// nativeCall is the fan-out of one native call share: one publish-only
// sender per viewer, all fed by the same encoder.
type nativeCall struct {
	share   *nativeShare
	callID  string
	servers func() ([]webrtc.ICEServer, error)
	policy  webrtc.ICETransportPolicy
	// Tests only: loopback candidates and an in-process viewer.
	settings *webrtc.SettingEngine
	sendTo   func(uid string, d PrivateCallDescription) error

	mu      sync.Mutex
	peers   map[string]*nativeCallPeer
	started bool
	closed  bool
}

type nativeCallPeer struct {
	uid        string
	link       *nativeCallLink // the current connection attempt
	reconnects int
	estimate   int
	closed     bool
}

// nativeCallLink is one connection attempt to a viewer and its signaling
// (nativesender.Signaling over sealed call signals, sub "screen").
type nativeCallLink struct {
	call    *nativeCall
	peer    *nativeCallPeer
	answers chan string
	// Guarded by call.mu.
	sender      *nativesender.Sender
	remote      []netproto.ICECandidate // viewer candidates before the sender exists
	remoteCount int
	local       []callCandidate
	localCount  int
	offerSent   bool
	flushing    bool
}

// callCandidate is one entry of a "candidates" description (RTCIceCandidateInit).
type callCandidate struct {
	Candidate     string  `json:"candidate"`
	SDPMid        *string `json:"sdpMid,omitempty"`
	SDPMLineIndex *uint16 `json:"sdpMLineIndex,omitempty"`
}

func (c *nativeCall) setTargets(targets []string) error {
	if len(targets) > netproto.MaxConversationMembers {
		return errors.New("too many call viewers")
	}
	c.share.cm.mu.Lock()
	self := c.share.cm.uniqueID
	c.share.cm.mu.Unlock()
	want := map[string]bool{}
	for _, uid := range targets {
		if uid == "" || uid == self || len(uid) > 128 {
			return errors.New("invalid call viewer")
		}
		want[uid] = true
	}
	c.mu.Lock()
	var removed []*nativesender.Sender
	var added []*nativeCallPeer
	for uid, p := range c.peers {
		if !want[uid] {
			p.closed = true
			if p.link != nil && p.link.sender != nil {
				removed = append(removed, p.link.sender)
			}
			delete(c.peers, uid)
		}
	}
	for uid := range want {
		if c.peers[uid] == nil {
			p := &nativeCallPeer{uid: uid}
			c.peers[uid] = p
			added = append(added, p)
		}
	}
	connect := c.started && !c.closed
	c.mu.Unlock()
	for _, s := range removed {
		_ = s.Close()
	}
	if connect {
		for _, p := range added {
			go func() { _ = c.connect(p) }()
		}
	}
	return nil
}

// open connects every viewer; it fails only when none answered.
func (c *nativeCall) open() error {
	c.mu.Lock()
	c.started = true
	peers := make([]*nativeCallPeer, 0, len(c.peers))
	for _, p := range c.peers {
		peers = append(peers, p)
	}
	c.mu.Unlock()
	if len(peers) == 0 {
		return errors.New("no call participant can receive a native screen share")
	}
	errs := make(chan error, len(peers))
	for _, p := range peers {
		go func() { errs <- c.connect(p) }()
	}
	var last error
	connected := false
	for range peers {
		if err := <-errs; err != nil {
			last = err
		} else {
			connected = true
		}
	}
	if !connected {
		return last
	}
	return nil
}

// connect makes one attempt; a failure counts towards the reconnect limit.
func (c *nativeCall) connect(p *nativeCallPeer) error {
	link := &nativeCallLink{call: c, peer: p, answers: make(chan string, 1)}
	c.mu.Lock()
	if c.closed || p.closed {
		c.mu.Unlock()
		return errNativeShareStopped
	}
	p.link = link
	c.mu.Unlock()
	ice, err := c.servers()
	if err != nil {
		c.lost(p, link)
		return err
	}
	n := c.share
	n.mu.Lock()
	initial, maxBitrate := n.level.Bitrate, n.ladder.Top.Bitrate
	var audioKeys *framecrypt.Encryptor
	if n.audio != nil {
		audioKeys = n.audioKeys
	}
	n.mu.Unlock()
	sender, err := nativesender.New(nativesender.Config{ICEServers: ice, ICETransportPolicy: c.policy, SettingEngine: c.settings,
		Signaling: link, Keys: n.keys, AudioKeys: audioKeys, InitialBitrate: initial, MaxBitrate: maxBitrate,
		OnKeyframeRequest: n.requestKeyframe, OnEstimate: func(bps int) { c.estimate(p, bps) }})
	if err != nil {
		if !errors.Is(err, errNativeShareStopped) {
			log.Printf("native call share offer: %v", err)
			c.lost(p, link)
		}
		return err
	}
	sender.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		switch state {
		case webrtc.PeerConnectionStateConnected:
			c.mu.Lock()
			if p.link == link {
				p.reconnects = 0
			}
			c.mu.Unlock()
			n.requestKeyframe() // a new viewer starts on a keyframe
		case webrtc.PeerConnectionStateFailed, webrtc.PeerConnectionStateClosed:
			c.lost(p, link)
		}
	})
	c.mu.Lock()
	if c.closed || p.closed || p.link != link {
		c.mu.Unlock()
		_ = sender.Close()
		return errNativeShareStopped
	}
	link.sender = sender
	pending := link.remote
	link.remote = nil
	c.mu.Unlock()
	for _, candidate := range pending {
		_ = sender.AddCandidate(candidate)
	}
	return nil
}

// lost reconnects a viewer whose connection failed (its webview closed the
// sub-connection, a relay failure); nativeReconnectLimit consecutive
// failures end the share.
func (c *nativeCall) lost(p *nativeCallPeer, link *nativeCallLink) {
	c.mu.Lock()
	if c.closed || p.closed || p.link != link {
		c.mu.Unlock()
		return
	}
	p.link = nil
	p.reconnects++
	attempt := p.reconnects
	sender := link.sender
	c.mu.Unlock()
	go func() {
		if sender != nil {
			_ = sender.Close()
		}
		if attempt > nativeReconnectLimit {
			c.share.fail("peer")
			return
		}
		select {
		case <-c.share.stop:
			return
		case <-time.After(time.Duration(attempt) * time.Second):
		}
		_ = c.connect(p)
	}()
}

// estimate drives the shared encoder by the slowest viewer.
func (c *nativeCall) estimate(p *nativeCallPeer, bps int) {
	c.mu.Lock()
	p.estimate = bps
	lowest := 0
	for _, other := range c.peers {
		if other.link != nil && other.estimate > 0 && (lowest == 0 || other.estimate < lowest) {
			lowest = other.estimate
		}
	}
	c.mu.Unlock()
	if lowest > 0 {
		c.share.onEstimate(lowest)
	}
}

func (c *nativeCall) senders() []*nativesender.Sender {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]*nativesender.Sender, 0, len(c.peers))
	for _, p := range c.peers {
		if p.link != nil && p.link.sender != nil {
			out = append(out, p.link.sender)
		}
	}
	return out
}

// writeFrame sends one access unit to every viewer; it reports the first
// error other than a withheld frame.
func (c *nativeCall) writeFrame(f nativecapture.Frame) error {
	var first error
	for _, s := range c.senders() {
		if err := s.WriteFrame(f.Data, f.Keyframe, f.SeqHeader, f.Captured); err != nil && !errors.Is(err, nativesender.ErrDropped) && first == nil {
			first = err
		}
	}
	return first
}

func (c *nativeCall) writeAudio(p nativecapture.AudioPacket) {
	for _, s := range c.senders() {
		_ = s.WriteAudio(p.Data, p.Captured) // a keyless packet is dropped, never sent
	}
}

func (c *nativeCall) close() {
	c.mu.Lock()
	c.closed = true
	var senders []*nativesender.Sender
	for _, p := range c.peers {
		if p.link != nil && p.link.sender != nil {
			senders = append(senders, p.link.sender)
		}
	}
	c.mu.Unlock()
	for _, s := range senders {
		_ = s.Close()
	}
}

// reply applies a viewer's sealed reply: its answer or its candidates.
func (c *nativeCall) reply(d PrivateCallDescription) {
	c.mu.Lock()
	p := c.peers[d.From]
	if p == nil || p.link == nil {
		c.mu.Unlock()
		return
	}
	link := p.link
	switch d.Type {
	case "answer":
		select {
		case link.answers <- d.SDP:
		default:
		}
		c.mu.Unlock()
	case "candidates":
		var list []callCandidate
		if err := json.Unmarshal([]byte(d.SDP), &list); err != nil || link.remoteCount+len(list) > 64 {
			c.mu.Unlock()
			return
		}
		link.remoteCount += len(list)
		var add []netproto.ICECandidate
		for _, entry := range list {
			candidate := netproto.ICECandidate{Candidate: entry.Candidate}
			if entry.SDPMid != nil {
				candidate.SDPMid = *entry.SDPMid
			}
			if entry.SDPMLineIndex != nil {
				candidate.SDPMLineIndex = *entry.SDPMLineIndex
			}
			add = append(add, candidate)
		}
		sender := link.sender
		if sender == nil {
			link.remote = append(link.remote, add...)
		}
		c.mu.Unlock()
		if sender != nil {
			for _, candidate := range add {
				if err := sender.AddCandidate(candidate); err != nil {
					log.Printf("native call share candidate: %v", err)
				}
			}
		}
	default:
		c.mu.Unlock()
	}
}

// routeCallScreenReply hands a viewer's reply to this session's native
// call share of that call (if any).
func (m *connManager) routeCallScreenReply(d PrivateCallDescription) {
	m.mu.Lock()
	n := m.nativeShare
	m.mu.Unlock()
	if n == nil || n.call == nil || n.call.callID != d.CallID {
		return
	}
	n.call.reply(d)
}

func (l *nativeCallLink) send(d PrivateCallDescription) error {
	d.Sub = callSubScreen
	if l.call.sendTo != nil {
		return l.call.sendTo(l.peer.uid, d)
	}
	return l.call.share.cm.sendPrivateCallDescription(l.call.callID, l.peer.uid, d)
}

func (l *nativeCallLink) current() bool {
	l.call.mu.Lock()
	defer l.call.mu.Unlock()
	return !l.call.closed && !l.peer.closed && l.peer.link == l
}

// Offer sends the sender's offer and waits for the viewer's answer.
func (l *nativeCallLink) Offer(offer netproto.WebRTCOffer) (netproto.WebRTCAnswer, error) {
	if !l.current() {
		return netproto.WebRTCAnswer{}, errNativeShareStopped
	}
	if err := l.send(PrivateCallDescription{Type: "offer", SDP: offer.SDP}); err != nil {
		return netproto.WebRTCAnswer{}, err
	}
	l.call.mu.Lock()
	l.offerSent = true
	l.call.mu.Unlock()
	l.flush() // candidates gathered while the offer was on its way
	select {
	case sdp := <-l.answers:
		return netproto.WebRTCAnswer{SDP: sdp, Peer: netproto.PeerNative}, nil
	case <-l.call.share.stop:
		return netproto.WebRTCAnswer{}, errNativeShareStopped
	case <-time.After(nativeCallAnswerTimeout):
		return netproto.WebRTCAnswer{}, errors.New("the call participant did not answer the native screen share")
	}
}

// Candidate queues one local candidate; they follow the offer in batches of
// at most 16 (64 per connection), like the webview's call candidates.
func (l *nativeCallLink) Candidate(c netproto.ICECandidate) error {
	mid, index := c.SDPMid, c.SDPMLineIndex
	l.call.mu.Lock()
	if l.localCount >= 64 {
		l.call.mu.Unlock()
		return errors.New("too many call candidates")
	}
	l.localCount++
	l.local = append(l.local, callCandidate{Candidate: c.Candidate, SDPMid: &mid, SDPMLineIndex: &index})
	l.call.mu.Unlock()
	l.flush()
	return nil
}

func (l *nativeCallLink) flush() {
	l.call.mu.Lock()
	if !l.offerSent || l.flushing || len(l.local) == 0 {
		l.call.mu.Unlock()
		return
	}
	l.flushing = true
	l.call.mu.Unlock()
	go func() {
		time.Sleep(150 * time.Millisecond) // batch the gathering burst
		for {
			l.call.mu.Lock()
			batch := l.local[:min(16, len(l.local))]
			l.local = l.local[len(batch):]
			if len(batch) == 0 || l.peer.link != l || l.call.closed {
				l.flushing = false
				l.call.mu.Unlock()
				return
			}
			l.call.mu.Unlock()
			payload, err := json.Marshal(batch)
			if err == nil {
				err = l.send(PrivateCallDescription{Type: "candidates", SDP: string(payload)})
			}
			if err != nil {
				log.Printf("native call share candidates: %v", err)
				l.call.mu.Lock()
				l.flushing = false
				l.call.mu.Unlock()
				return
			}
		}
	}()
}
