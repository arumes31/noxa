//go:build integration

package webrtc

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// A real Chromium listener (the desktop client's WebView engine) in a channel
// with Pion speakers that leave, rejoin and reconnect. Rejoins make the server
// reuse the listener's inactive media sections for new tracks; the listener
// uses the frontend's own remote-media.js / media-track-id.js mapping. It
// checks that every speaker keeps arriving and resolves to the right user.
//
//	go test -tags integration -run TestBrowserVoiceListenerSurvivesSpeakerChurn -v ./internal/webrtc/
//
// Needs node and Playwright with Chromium (NOXA_PLAYWRIGHT_MODULE may point at
// the playwright package directory).
func TestBrowserVoiceListenerSurvivesSpeakerChurn(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is required")
	}
	module := os.Getenv("NOXA_PLAYWRIGHT_MODULE")
	if module == "" {
		for _, candidate := range []string{"/opt/node-tools/node_modules/playwright", "../../client/frontend/node_modules/@playwright/test"} {
			if _, err := os.Stat(candidate); err == nil {
				module, _ = filepath.Abs(candidate)
				break
			}
		}
	}
	if module == "" {
		t.Skip("playwright is not installed")
	}
	cfg := quietChaosConfig(4)
	cfg.bound = 15 * time.Second
	// Lost offers and transient server socket failures, as in the chaos test.
	cfg.socketFault, cfg.offerDrop = 0.003, 0.1
	cycles := 12
	if n, err := strconv.Atoi(os.Getenv("NOXA_BROWSER_VOICE_CYCLES")); err == nil && n > 0 {
		cycles = n
	}
	h := newChaosHarness(t, cfg, 21)
	b := newBrowserListener(t, h, "browser")

	srv := httptest.NewServer(b)
	t.Cleanup(srv.Close)
	cmd := exec.Command(node, "testdata/browser_voice_listener.mjs", srv.URL+"/")
	cmd.Env = append(os.Environ(), "NOXA_PLAYWRIGHT_MODULE="+module)
	out, _ := cmd.StdoutPipe()
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// SIGTERM lets the script close Chromium; kill only if it does not
		// exit in time (or the platform cannot deliver SIGTERM).
		exited := make(chan struct{})
		go func() { _ = cmd.Wait(); close(exited) }()
		if err := cmd.Process.Signal(syscall.SIGTERM); err == nil {
			select {
			case <-exited:
				return
			case <-time.After(10 * time.Second):
			}
		}
		_ = cmd.Process.Kill()
		<-exited
	})
	go func() {
		scanner := bufio.NewScanner(out)
		for scanner.Scan() {
			h.logf("browser: %s", scanner.Text())
		}
	}()

	ids := joinAll(h, 4)
	h.verify("initial")
	b.verify(ids, "initial")
	for i := 0; i < cycles; i++ {
		id := ids[i%len(ids)]
		label := fmt.Sprintf("cycle %d: ", i)
		switch i % 3 {
		case 0:
			label += "rejoin " + id
			h.reconnect(id, true)
		case 1:
			label += "reconnect " + id
			h.reconnect(id, false)
		default:
			label += "ice-restart " + id
			h.client(id).reoffer(true)
		}
		h.verify(label)
		b.verify(ids, label)
	}
}

type browserListener struct {
	t  *testing.T
	h  *chaosHarness
	id string

	mu      sync.Mutex
	events  []map[string]any
	wake    chan struct{}
	packets map[string]float64
	bound   []string
	state   string
	// duplicate is the first receiver track.id two transceivers shared.
	duplicate         string
	reportedDuplicate bool
}

func newBrowserListener(t *testing.T, h *chaosHarness, id string) *browserListener {
	b := &browserListener{t: t, h: h, id: id, wake: make(chan struct{}, 1), packets: map[string]float64{}}
	h.mu.Lock()
	h.external[id] = func(offer string) error {
		if h.dropOffers.Load() || h.chance(h.cfg.offerDrop) {
			h.logf("fault: offer to %s lost", id)
			return nil
		}
		b.push(map[string]any{"type": "offer", "sdp": offer})
		return nil
	}
	h.mu.Unlock()
	h.refreshGuard()
	h.v.JoinChannel(id, chaosChannel)
	return b
}

func (b *browserListener) push(event map[string]any) {
	b.mu.Lock()
	b.events = append(b.events, event)
	b.mu.Unlock()
	select {
	case b.wake <- struct{}{}:
	default:
	}
}

func (b *browserListener) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SDP, Candidate, Mid string
		Index               uint16
		Packets             map[string]float64
		Bound               []string
		State               string
		Duplicate           string
	}
	if r.Method == http.MethodPost {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	switch r.URL.Path {
	case "/":
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(browserListenerPage))
	case "/offer":
		b.h.refreshGuard()
		answer, err := b.h.v.HandleOffer(b.id, body.SDP, func(candidate, mid string, index uint16) {
			b.push(map[string]any{"type": "candidate", "candidate": candidate, "mid": mid, "index": index})
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		_, _ = w.Write([]byte(answer))
	case "/answer":
		if err := b.h.v.HandleAnswer(b.id, body.SDP); err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
		}
	case "/candidate":
		_ = b.h.v.AddICECandidate(b.id, body.Candidate, body.Mid, body.Index)
	case "/events":
		select {
		case <-b.wake:
		case <-time.After(time.Second):
		case <-r.Context().Done():
		}
		b.mu.Lock()
		events := b.events
		b.events = nil
		b.mu.Unlock()
		if events == nil {
			events = []map[string]any{}
		}
		_ = json.NewEncoder(w).Encode(events)
	case "/stats":
		b.mu.Lock()
		b.packets, b.bound, b.state = body.Packets, body.Bound, body.State
		if body.Duplicate != "" && b.duplicate == "" {
			b.duplicate = body.Duplicate
		}
		b.mu.Unlock()
	default:
		if stub, ok := strings.CutPrefix(r.URL.Path, "/stub/"); ok && browserStubs[stub] != "" {
			w.Header().Set("Content-Type", "text/javascript")
			_, _ = w.Write([]byte(browserStubs[stub]))
			return
		}
		if len(r.URL.Path) > 5 && r.URL.Path[:5] == "/src/" {
			src := "../../client/frontend/src"
			if override := os.Getenv("NOXA_BROWSER_FRONTEND_SRC"); override != "" {
				src = override // e.g. an older remote-audio.js to confirm the test catches it
				if _, err := os.Stat(filepath.Join(src, filepath.Base(r.URL.Path))); err != nil {
					src = "../../client/frontend/src"
				}
			}
			w.Header().Set("Content-Type", "text/javascript")
			http.ServeFile(w, r, filepath.Join(src, filepath.Base(r.URL.Path)))
			return
		}
		http.NotFound(w, r)
	}
}

func (b *browserListener) verify(ids []string, label string) {
	b.t.Helper()
	present := map[string]bool{}
	for _, id := range ids {
		if b.h.present(id) {
			present[id] = true
		}
	}
	// packets is keyed "publisher/ssrc": a reconnecting speaker arrives on a
	// new SSRC whose counter starts at zero.
	b.mu.Lock()
	baseline := map[string]float64{}
	for key, n := range b.packets {
		baseline[key] = n
	}
	b.mu.Unlock()
	fresh := func(id string) float64 {
		total := 0.0
		for key, n := range b.packets {
			if strings.HasPrefix(key, id+"/") {
				total += n - baseline[key]
			}
		}
		return total
	}
	deadline := time.Now().Add(b.h.cfg.bound)
	for {
		b.mu.Lock()
		var missing []string
		boundSet := map[string]bool{}
		for _, id := range b.bound {
			boundSet[id] = true
		}
		for id := range present {
			if got := fresh(id); got < 10 || !boundSet[id] {
				missing = append(missing, fmt.Sprintf("%s (fresh packets %v bound=%v)", id, got, boundSet[id]))
			}
		}
		state, duplicate := b.state, b.duplicate
		b.mu.Unlock()
		if duplicate != "" && !b.reportedDuplicate {
			// Not a failure: it is why remote-audio.js keys by track object.
			b.reportedDuplicate = true
			b.t.Logf("%s: Chromium gave two receivers the same track.id %q", label, duplicate)
		}
		if len(missing) == 0 {
			return
		}
		if time.Now().After(deadline) {
			b.h.mu.Lock()
			logs := b.h.logs
			b.h.mu.Unlock()
			var diag []string
			for id := range present {
				diag = append(diag, id+": "+b.h.diagnose(b.id, id))
			}
			b.t.Fatalf("%s: browser (%s) cannot hear %v\nserver view:\n%v\n%v", label, state, missing, diag, logs)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// browserStubs replace remote-audio.js's UI-only dependencies; audio.js keeps
// the real playback element + MediaStreamAudioSourceNode construction.
var browserStubs = map[string]string{
	"positional-audio.js":    "export class SpatialVoice { attach() {} detach() {} peer() {} reset() {} retainPeers() {} update() {} local() {} }",
	"video.js":               "export { parseTrackID } from '/src/media-track-id.js';",
	"i18n.js":                "export const t = key => key;",
	"microphone-recovery.js": "export const watchAudioOutput = () => {};",
	"audio.js": `export const makeLimiter = ctx => ctx.createGain();
export const personalVolumeGain = value => value;
export const registerUserChain = () => {}, unregisterUserChain = () => {};
export const createAudioLevelSampler = () => () => 0;
export const getUserShareVolume = () => 1, isUserShareMuted = () => false;
export const onShareAudioChange = () => {}, setDucking = () => {};
export const attachUserNormalizer = (ctx, key, src) => src;
export const detachUserNormalizer = () => {}, detachAllUserNormalizers = () => {};
export const resumeAudioPlayback = async ctx => { await ctx?.resume?.(); };
export function createRemoteAudioSource(ctx, track) {
  const playback = new Audio(); playback.srcObject = new MediaStream([track]); playback.muted = true; void playback.play().catch(() => {});
  return { src: ctx.createMediaStreamSource(new MediaStream([track])), playback };
}`,
}

const browserListenerPage = `<!doctype html><meta charset="utf-8"><title>voice listener</title>
<script type="importmap">{"imports": {
  "/src/positional-audio.js": "/stub/positional-audio.js", "/src/audio.js": "/stub/audio.js", "/src/video.js": "/stub/video.js",
  "/src/i18n.js": "/stub/i18n.js", "/src/microphone-recovery.js": "/stub/microphone-recovery.js"}}</script>
<script type="module">
import { startRemoteMedia } from '/src/remote-media.js';
import { remoteTrackIDs, parseTrackID } from '/src/media-track-id.js';
import { createRemoteAudio } from '/src/remote-audio.js';
const post = (path, body) => fetch(path, { method: 'POST', body: JSON.stringify(body) });
const stream = await navigator.mediaDevices.getUserMedia({ audio: true });
const pc = new RTCPeerConnection();
pc.addTransceiver(stream.getAudioTracks()[0], { direction: 'sendrecv', streams: [stream] });
const bound = new Map();
const players = new Map();
// The frontend's playback owner, with UI-only dependencies stubbed.
const audio = createRemoteAudio({ state: { settings: { voice_limiter: false }, clients: [], trackUsers: new Map() }, toast: m => console.log('toast ' + m), sysMsg: m => console.log('sysMsg ' + m), voiceEpoch: () => 1 });
startRemoteMedia(pc, () => true, (track, id) => {
  bound.set(track, id);
  if (!parseTrackID(id).slot) audio.attachRemoteAudio(track, null, id);
}, (track, id) => { bound.delete(track); if (!parseTrackID(id).slot) audio.detachRemoteTrack(track); },
  (track, id) => track.kind !== 'audio' || parseTrackID(id).slot !== '' || audio.hasRemoteAudio(track, id));
pc.onicecandidate = e => { if (e.candidate) post('/candidate', { Candidate: e.candidate.candidate, Mid: e.candidate.sdpMid || '', Index: e.candidate.sdpMLineIndex || 0 }); };
let queue = Promise.resolve();
const serialize = op => (queue = queue.then(op, op));
await serialize(async () => {
  for (let attempt = 0; attempt < 5; attempt++) {
    const offer = await pc.createOffer();
    await pc.setLocalDescription(offer);
    const response = await fetch('/offer', { method: 'POST', body: JSON.stringify({ SDP: offer.sdp }) });
    if (response.ok) { await pc.setRemoteDescription({ type: 'answer', sdp: await response.text() }); return; }
    await pc.setLocalDescription({ type: 'rollback' });
    await new Promise(resolve => setTimeout(resolve, 300));
  }
});
(async () => {
  for (;;) {
    const events = await (await fetch('/events')).json();
    for (const event of events) {
      if (event.type === 'offer') {
        await serialize(async () => {
          await pc.setRemoteDescription({ type: 'offer', sdp: event.sdp });
          const answer = await pc.createAnswer();
          await pc.setLocalDescription(answer);
          await post('/answer', { SDP: answer.sdp });
        }).catch(error => console.log('offer failed: ' + error));
      } else if (event.type === 'candidate') {
        await pc.addIceCandidate({ candidate: event.candidate, sdpMid: event.mid, sdpMLineIndex: event.index }).catch(() => {});
      }
    }
  }
})();
setInterval(async () => {
  const ids = remoteTrackIDs(pc.remoteDescription?.sdp);
  const packets = {};
  (await pc.getStats()).forEach(stat => {
    if (stat.type !== 'inbound-rtp' || stat.kind !== 'audio') return;
    const id = ids.get(stat.mid);
    if (!id) return;
    const { clientID, slot } = parseTrackID(id);
    if (!slot) packets[clientID + '/' + stat.ssrc] = stat.packetsReceived;
  });
  // "Bound" = publishers whose CURRENT receiver (by mid -> a=msid) plays
  // through remote-audio.js as that publisher.
  const boundIDs = [];
  for (const transceiver of pc.getTransceivers()) {
    const id = ids.get(transceiver.mid);
    if (id && !parseTrackID(id).slot && audio.hasRemoteAudio(transceiver.receiver.track, id)) boundIDs.push(parseTrackID(id).clientID);
  }
  // remote-audio.js keys playback chains by receiver track.id: two live
  // receivers sharing one (e.g. an msid-derived id surviving media-section
  // reuse) would let one publisher's attach tear down another's chain.
  const seen = new Set();
  let duplicate = '';
  for (const transceiver of pc.getTransceivers()) {
    const id = transceiver.receiver.track.id;
    if (seen.has(id)) duplicate = id;
    seen.add(id);
  }
  await post('/stats', { Packets: packets, Bound: boundIDs, State: pc.connectionState, Duplicate: duplicate });
}, 500);
</script>`
