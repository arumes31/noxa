//go:build integration

package webrtc

import (
	"bufio"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/rtp"
	"go.uber.org/zap"
)

// Encrypted channel media (Task 2) through the ACTUAL SFU: a real Voice
// (Pion engine with the E2EE codec policy, the production router, a
// recorder-style tap) and headless Chromium participants that run the real
// client modules:
//
//   - "desktop" pages: channel-media.js + video-negotiation.js (the desktop
//     glue: secure offers, sender/receiver binding) over a bridged
//     window.go.main.App;
//   - "pwa" pages: mobile/src/core/voice.js (the PWA Voice class) over a
//     bridged control connection;
//   - every page: the shared MediaSession / MediaKeyEngine / worker with the
//     pinned LiveKit FrameCryptor, on BOTH transform APIs (pages alternate
//     RTCRtpScriptTransform and createEncodedStreams).
//
// Captures are Chromium's fake devices (--use-fake-device-for-media-stream:
// a beeping microphone and a moving test pattern camera) plus a canvas
// "screen" and an oscillator "screen audio". The authenticated ratchet
// control transport is replaced by this harness relay (it forwards the
// exact control bodies from one device to the named device and logs them);
// that transport has its own tests (Task 1c, ratchet_control).
//
//	go test -tags integration -run TestBrowserE2EEChannelMediaThroughSFU -v ./internal/webrtc/
//
// Needs node, the frontend's node_modules (Vite, Playwright) and mobile's
// node_modules (livekit-client). NOXA_E2EE_LONG_MINUTES runs the long-session
// phase for that many minutes (default: skipped).
func TestBrowserE2EEChannelMediaThroughSFU(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is required")
	}
	frontend, _ := filepath.Abs("../../client/frontend")
	if _, err := os.Stat(filepath.Join(frontend, "node_modules", "vite")); err != nil {
		t.Skip("client/frontend node_modules are not installed")
	}
	h := newE2EEHarness(t)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	cmd := exec.Command(node, filepath.Join("testdata", "browser_e2ee.mjs"), srv.URL)
	cmd.Dir, _ = filepath.Abs(".")
	cmd.Env = append(os.Environ(), "NOXA_FRONTEND="+frontend)
	out, _ := cmd.StdoutPipe()
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var lines []string
	var linesMu sync.Mutex
	done := make(chan struct{})
	go func() {
		scanner := bufio.NewScanner(out)
		scanner.Buffer(make([]byte, 1<<20), 1<<20)
		for scanner.Scan() {
			linesMu.Lock()
			lines = append(lines, scanner.Text())
			linesMu.Unlock()
			t.Log(scanner.Text())
		}
		close(done)
	}()
	waitErr := cmd.Wait()
	<-done
	if waitErr != nil {
		t.Fatalf("browser scenario failed: %v", waitErr)
	}
	linesMu.Lock()
	passed := len(lines) > 0 && strings.Contains(lines[len(lines)-1], "E2EE SCENARIO PASSED")
	linesMu.Unlock()
	if !passed {
		t.Fatal("browser scenario did not report success")
	}
	// Recorder/tap ingress: every channel packet the SFU handed to a tap was
	// a library-encrypted frame (trailer present), never plaintext.
	h.mu.Lock()
	defer h.mu.Unlock()
	t.Logf("tap: audio packets %d (encrypted %d), video frames %d (encrypted %d)", h.tapAudio, h.tapAudioEncrypted, h.tapVideoFrames, h.tapVideoEncrypted)
	// Statistical plaintext detector (a plaintext frame ends in [12, <16] with
	// probability ~2^-12); real decrypt failure without keys is shown in phase 3.
	if h.tapAudio == 0 || h.tapVideoFrames == 0 || h.tapAudio != h.tapAudioEncrypted || h.tapVideoFrames != h.tapVideoEncrypted {
		t.Fatalf("tap saw non-encrypted media: audio %d/%d video %d/%d", h.tapAudioEncrypted, h.tapAudio, h.tapVideoEncrypted, h.tapVideoFrames)
	}
	for _, answer := range h.answers {
		lower := strings.ToLower(answer)
		if strings.Contains(lower, "h264/") || strings.Contains(lower, "h265/") || strings.Contains(lower, "vp9/") || strings.Contains(lower, "av1/") {
			t.Fatalf("SFU answered an unproven codec while media is encrypted:\n%s", answer)
		}
	}
}

const e2eeChannel = 1
const e2eeEchoChannel = 2

var errLossInjected = errors.New("injected loss")

type e2eePeer struct {
	id, principal, device string
	events                []map[string]any
	wake                  chan struct{}
	hidden                map[string]bool // members this peer's roster omits
}

type e2eeHarness struct {
	t *testing.T
	e *Engine
	r *Router
	v *Voice

	mu                                sync.Mutex
	peers                             map[string]*e2eePeer
	inChannel                         map[string]int64
	controls                          []map[string]any
	watchRevision                     map[string]uint64
	answers                           []string
	candidatesIn, candidatesOut       map[string]int
	reordered                         int
	reorderMu                         sync.Mutex
	reorderHeld                       map[string]func() error
	keyframesServed, keyframesRefused int
	tapAudio, tapAudioEncrypted       int
	tapVideoFrames, tapVideoEncrypted int
}

func newE2EEHarness(t *testing.T) *e2eeHarness {
	t.Helper()
	e, err := NewWithMediaPolicy(zap.NewNop(), nil, false, NetworkConfig{UDPAddr: "127.0.0.1:0", ExternalIPs: []string{"127.0.0.1"}}, VideoBounds{}, MediaPolicy{E2EE: true})
	if err != nil {
		t.Fatal(err)
	}
	e.iceServers = nil
	h := &e2eeHarness{t: t, e: e, peers: map[string]*e2eePeer{}, inChannel: map[string]int64{}, watchRevision: map[string]uint64{}, candidatesIn: map[string]int{}, candidatesOut: map[string]int{}}
	h.r = NewRouter(zap.NewNop())
	h.v = NewVoice(e, h.r, zap.NewNop())
	h.v.renegDebounce = 20 * time.Millisecond
	h.v.renegRateLimit = 150 * time.Millisecond
	h.v.renegAnswer = 2 * time.Second
	h.v.SetHandlers(func(string) bool { return true }, nil)
	h.v.SetVideoHandlers(func(string) bool { return true })
	h.r.SetPublisherGuard(func(pair PublisherAccess) bool {
		return pair.ChannelID > 0 && pair.ChannelID == pair.SubscriberChannelID
	})
	h.v.SetOfferGuard(func(_ string, send func() error) error { return send() })
	h.v.SetOfferSender(func(id, sdp string) error {
		h.push(id, map[string]any{"type": "offer", "sdp": sdp})
		return nil
	})
	// The echo test channel returns each member's own media to itself.
	h.v.SetEchoChannel(e2eeEchoChannel)
	h.v.AddTap(e2eeChannel, "recorder-tap", tapWriter{h: h, video: false}, tapWriter{h: h, video: true})
	t.Cleanup(func() { _ = e.Close() })
	return h
}

// tapWriter stands where the server recorder would read channel media.
type tapWriter struct {
	h     *e2eeHarness
	video bool
}

// isEncryptedFrameTail: the library appends IV (12) | 12 | key index to every
// frame, so the last packet of each frame ends with [12, kid < 16].
func isEncryptedFrameTail(payload []byte) bool {
	n := len(payload)
	return n >= 31 && payload[n-2] == 12 && payload[n-1] < 16
}

func (w tapWriter) WriteRTP(pkt *rtp.Packet) error {
	w.h.mu.Lock()
	defer w.h.mu.Unlock()
	if !w.video {
		if len(pkt.Payload) == 0 {
			return nil
		}
		w.h.tapAudio++
		if isEncryptedFrameTail(pkt.Payload) {
			w.h.tapAudioEncrypted++
		}
		return nil
	}
	if !pkt.Marker || len(pkt.Payload) == 0 {
		return nil
	}
	w.h.tapVideoFrames++
	if isEncryptedFrameTail(pkt.Payload) {
		w.h.tapVideoEncrypted++
	}
	return nil
}

func (h *e2eeHarness) push(id string, event map[string]any) {
	h.mu.Lock()
	p := h.peers[id]
	if p != nil {
		p.events = append(p.events, event)
	}
	h.mu.Unlock()
	if p != nil {
		select {
		case p.wake <- struct{}{}:
		default:
		}
	}
}

// pushRosterLocked sends every peer its view of the channel roster.
func (h *e2eeHarness) pushRosters() {
	h.mu.Lock()
	type entry = map[string]any
	views := map[string][]entry{}
	for id, p := range h.peers {
		var clients []entry
		for member, channel := range h.inChannel {
			m := h.peers[member]
			if m == nil || p.hidden[member] || channel != h.inChannel[id] {
				continue
			}
			clients = append(clients, entry{"client_id": m.id, "unique_id": m.principal, "media_device": m.device, "channel_id": channel})
		}
		views[id] = clients
	}
	h.mu.Unlock()
	for id, clients := range views {
		h.push(id, map[string]any{"type": "roster", "clients": clients})
	}
}

func (h *e2eeHarness) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "content-type")
	if r.Method == http.MethodOptions {
		return
	}
	var body struct {
		ID, Principal, Device, SDP, Candidate, Mid, To, Hide, Publisher, Slot string
		Index                                                                 uint16
		Slots                                                                 []map[string]string
		Body                                                                  json.RawMessage
		Active                                                                bool
		Channel                                                               int64
	}
	if r.Method == http.MethodPost {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	reply := func(v any) { w.Header().Set("Content-Type", "application/json"); _ = json.NewEncoder(w).Encode(v) }
	switch r.URL.Path {
	case "/register":
		h.mu.Lock()
		if h.peers[body.ID] == nil {
			h.peers[body.ID] = &e2eePeer{id: body.ID, principal: body.Principal, device: body.Device, wake: make(chan struct{}, 1), hidden: map[string]bool{}}
		}
		h.mu.Unlock()
		reply(map[string]any{"ok": true})
	case "/join":
		channel := int64(e2eeChannel)
		if body.Channel != 0 {
			channel = body.Channel
		}
		h.mu.Lock()
		h.inChannel[body.ID] = channel
		h.mu.Unlock()
		h.v.JoinChannel(body.ID, channel)
		h.pushRosters()
		reply(map[string]any{"ok": true})
	case "/leave":
		h.mu.Lock()
		channel := h.inChannel[body.ID]
		delete(h.inChannel, body.ID)
		h.mu.Unlock()
		h.v.LeaveChannel(body.ID, channel)
		_ = h.v.ClosePeer(body.ID)
		h.pushRosters()
		reply(map[string]any{"ok": true})
	case "/hide":
		// The viewer's roster omits a member that the SFU still serves
		// (a removed/revoked device that keeps receiving packets).
		h.mu.Lock()
		if p := h.peers[body.ID]; p != nil {
			p.hidden[body.Hide] = true
		}
		h.mu.Unlock()
		h.pushRosters()
		reply(map[string]any{"ok": true})
	case "/close":
		_ = h.v.ClosePeer(body.ID)
		reply(map[string]any{"ok": true})
	case "/loss":
		// Packet loss on the SFU's egress socket (SRTP only).
		if !body.Active {
			mediaSocketFault.Store(nil)
		} else {
			var n atomic.Uint64
			fault := func(datagram []byte) error {
				if isSRTPDatagram(datagram) && n.Add(1)%10 == 0 {
					return errLossInjected
				}
				return nil
			}
			mediaSocketFault.Store(&fault)
			h.t.Cleanup(func() { mediaSocketFault.Store(nil) })
		}
		reply(map[string]any{"ok": true})
	case "/keyframe":
		// An encoded-streams receiver's keyframe request (server action
		// "keyframe"): served only for a publication the viewer receives.
		ok := h.v.RequestViewerKeyframe(body.ID, body.Publisher, body.Slot)
		h.mu.Lock()
		if ok {
			h.keyframesServed++
		} else {
			h.keyframesRefused++
		}
		h.mu.Unlock()
		reply(map[string]any{"ok": ok})
	case "/keyframes":
		h.mu.Lock()
		reply(map[string]any{"served": h.keyframesServed, "refused": h.keyframesRefused})
		h.mu.Unlock()
	case "/reorder":
		// SRTP-level reordering on the SFU's egress socket, per destination:
		// every 7th SRTP datagram to a peer is held back and written after the
		// next SRTP datagram to the same peer (a real swap at that receiver).
		// Switching it off flushes held datagrams. The receiver's jitter
		// buffer reorders packets before the frame transform sees them, so
		// this shows the stack tolerates packet reordering; frame-level
		// reordering is covered by unit/media-replay.test.mjs.
		h.reorderMu.Lock()
		if !body.Active {
			mediaSocketShape.Store(nil)
			for _, flush := range h.reorderHeld {
				_ = flush()
			}
			h.reorderHeld = nil
		} else {
			counts := map[string]uint64{}
			h.reorderHeld = map[string]func() error{}
			shape := func(datagram []byte, addr net.Addr, write func([]byte) error) error {
				if !isSRTPDatagram(datagram) {
					return write(datagram)
				}
				h.reorderMu.Lock()
				defer h.reorderMu.Unlock()
				if h.reorderHeld == nil { // switched off while this write waited
					return write(datagram)
				}
				key := addr.String()
				counts[key]++
				held := h.reorderHeld[key]
				if held == nil && counts[key]%7 == 0 {
					d := append([]byte(nil), datagram...)
					h.reorderHeld[key] = func() error { return write(d) }
					h.mu.Lock()
					h.reordered++
					h.mu.Unlock()
					return nil
				}
				err := write(datagram)
				if held != nil {
					_ = held()
					delete(h.reorderHeld, key)
				}
				return err
			}
			mediaSocketShape.Store(&shape)
			h.t.Cleanup(func() { mediaSocketShape.Store(nil) })
		}
		h.reorderMu.Unlock()
		h.mu.Lock()
		reordered := h.reordered
		h.mu.Unlock()
		reply(map[string]any{"ok": true, "reordered": reordered})
	case "/offer":
		slots := map[string]string{}
		for _, s := range body.Slots {
			slots[s["track_id"]] = s["slot"]
		}
		h.v.DeclareTrackSlots(body.ID, slots)
		answer, err := h.v.HandleOffer(body.ID, body.SDP, func(candidate, mid string, index uint16) {
			h.mu.Lock()
			h.candidatesOut[body.ID]++
			h.mu.Unlock()
			h.t.Logf("server candidate for %s: %s mid=%q", body.ID, candidate, mid)
			h.push(body.ID, map[string]any{"type": "candidate", "candidate": candidate, "mid": mid, "index": index})
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		h.t.Logf("offer from %s answered (answer candidates %d, ice-lite %v)", body.ID, strings.Count(answer, "a=candidate"), strings.Contains(answer, "ice-lite"))
		h.mu.Lock()
		h.answers = append(h.answers, answer)
		h.mu.Unlock()
		reply(map[string]any{"sdp": answer})
	case "/answer":
		if err := h.v.HandleAnswer(body.ID, body.SDP); err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		reply(map[string]any{"ok": true})
	case "/candidate":
		err := h.v.AddICECandidate(body.ID, body.Candidate, body.Mid, body.Index)
		h.mu.Lock()
		h.candidatesIn[body.ID]++
		h.mu.Unlock()
		if err != nil {
			h.t.Logf("candidate from %s rejected: %v", body.ID, err)
		}
		reply(map[string]any{"ok": true})
	case "/publish":
		gen, err := h.v.PublishVideoWithCodecs(body.ID, body.Slot, 0, body.Active, "", []string{"vp8"})
		if err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		reply(map[string]any{"generation": gen})
	case "/watch":
		var gen uint64
		for _, p := range h.v.VideoPublications(body.ID) {
			if p.PublisherID == body.Publisher && p.Slot == body.Slot {
				gen = p.Generation
			}
		}
		key := body.ID + "|" + body.Publisher + "|" + body.Slot
		h.mu.Lock()
		h.watchRevision[key]++
		rev := h.watchRevision[key]
		h.mu.Unlock()
		started, err := h.v.WatchVideoWithCodecs(body.ID, body.Publisher, body.Slot, gen, rev, h.v.VideoWatchSession(body.ID), true, []string{"vp8"})
		if err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		reply(map[string]any{"started": started, "generation": gen})
	case "/control":
		// The relay stands in for the authenticated ratchet control transport:
		// one exact body, from one device, to the one named device.
		var target string
		var event map[string]any
		h.mu.Lock()
		from := h.peers[body.ID]
		for id, p := range h.peers {
			if p.device == body.To {
				target = id
			}
		}
		var parsed map[string]any
		_ = json.Unmarshal(body.Body, &parsed)
		if from != nil {
			event = map[string]any{"type": "control", "sender": from.principal, "senderDevice": from.device, "body": parsed}
			h.controls = append(h.controls, map[string]any{"from": from.device, "to": body.To, "epoch": parsed["epoch"]})
		}
		h.mu.Unlock()
		if target == "" || event == nil {
			reply(map[string]any{"ok": false, "code": "device_unavailable"})
			return
		}
		h.push(target, event)
		reply(map[string]any{"ok": true, "state": "accepted"})
	case "/controls":
		h.mu.Lock()
		out := append([]map[string]any(nil), h.controls...)
		h.mu.Unlock()
		reply(out)
	case "/tap":
		h.mu.Lock()
		reply(map[string]any{"cand_in": h.candidatesIn, "cand_out": h.candidatesOut, "audio": h.tapAudio, "audio_encrypted": h.tapAudioEncrypted, "video": h.tapVideoFrames, "video_encrypted": h.tapVideoEncrypted})
		h.mu.Unlock()
	case "/events":
		id := r.URL.Query().Get("id")
		h.mu.Lock()
		p := h.peers[id]
		h.mu.Unlock()
		if p == nil {
			reply([]any{})
			return
		}
		select {
		case <-p.wake:
		case <-time.After(time.Second):
		case <-r.Context().Done():
		}
		h.mu.Lock()
		events := p.events
		p.events = nil
		h.mu.Unlock()
		if events == nil {
			events = []map[string]any{}
		}
		reply(events)
	default:
		http.NotFound(w, r)
	}
}
