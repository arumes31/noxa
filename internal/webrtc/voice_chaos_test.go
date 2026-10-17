// voice_chaos_test.go drives the real Voice/Router/Engine with 8-12 real Pion
// client peer connections over loopback ICE/DTLS/SRTP and randomized,
// seeded chaos: concurrent joins, leave/rejoin, reconnect with the same
// client ID, ICE restarts and re-offers racing server renegotiation, mute and
// talk-power storms, lost renegotiation offers, client-side packet
// loss/jitter/reordering and transient server socket write failures.
//
// The invariant checked after every chaos step is the one users notice: every
// listener receives decodable Opus packets from every unmuted speaker within
// a bound, and the client-side track mapping (the same mid -> a=msid lookup
// the desktop frontend performs in media-track-id.js) attributes every packet
// to the publisher that actually sent it.
//
//	go test -race -run Chaos ./internal/webrtc/            short default
//	NOXA_VOICE_CHAOS_SOAK=10m go test -race -run Chaos -timeout 30m ./internal/webrtc/
//	NOXA_VOICE_CHAOS_SEED=42 go test -race -run Chaos ./internal/webrtc/   replay one seed
//
// Seeds make the operation schedule and fault decisions reproducible; packet
// timing on a loaded machine is not, so a seed replays the same scenario
// rather than the same interleaving. NOXA_VOICE_CHAOS_CLIENTS/_STEPS override
// the size; NOXA_VOICE_CHAOS_SDP=1 adds SDP histories to failure reports.
//
// The simulated clients are Pion, not Chromium. Pion occasionally never
// surfaces a receiver whose media section was reused after being inactive;
// verify() classifies that strictly (the server kept writing SRTP for the
// exact SSRC the listener's SDP declares) and rebuilds that listener, at
// most twice per check. voice_browser_churn_test.go (integration tag) runs
// the same churn against a real headless Chromium listener using the
// frontend's own mapping and playback modules.
package webrtc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/rtp"
	pion "github.com/pion/webrtc/v4"
	"go.uber.org/zap"
)

type chaosConfig struct {
	clients     int
	steps       int
	loss        float64       // per-packet client socket loss, each direction
	jitter      time.Duration // max extra client send delay (reorders packets)
	socketFault float64       // probability a server socket write fails
	offerDrop   float64       // probability a server offer never reaches the client
	bound       time.Duration // how long every expected pair may take to deliver
	packetEvery time.Duration
}

func defaultChaosConfig() chaosConfig {
	return chaosConfig{
		clients:     8,
		steps:       8,
		loss:        0.02,
		jitter:      15 * time.Millisecond,
		socketFault: 0.003,
		offerDrop:   0.1,
		bound:       12 * time.Second,
		packetEvery: 60 * time.Millisecond,
	}
}

func TestVoiceChaos(t *testing.T) {
	cfg := defaultChaosConfig()
	seeds := []int64{1}
	var soak time.Duration
	if raw := os.Getenv("NOXA_VOICE_CHAOS_SOAK"); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil {
			d = 5 * time.Minute
		}
		soak = d
		cfg.clients = 12
		cfg.steps = 25
	}
	if n, err := strconv.Atoi(os.Getenv("NOXA_VOICE_CHAOS_CLIENTS")); err == nil && n > 1 {
		cfg.clients = n
	}
	if n, err := strconv.Atoi(os.Getenv("NOXA_VOICE_CHAOS_STEPS")); err == nil && n >= 0 {
		cfg.steps = n
	}
	if raw := os.Getenv("NOXA_VOICE_CHAOS_SEED"); raw != "" {
		seed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			t.Fatalf("NOXA_VOICE_CHAOS_SEED: %v", err)
		}
		seeds = []int64{seed}
		soak = 0
	}
	if soak > 0 {
		deadline := time.Now().Add(soak)
		for seed := int64(1); time.Now().Before(deadline); seed++ {
			if !t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) { runVoiceChaos(t, cfg, seed) }) {
				return
			}
		}
		return
	}
	for _, seed := range seeds {
		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) { runVoiceChaos(t, cfg, seed) })
	}
}

func runVoiceChaos(t *testing.T, cfg chaosConfig, seed int64) {
	h := newChaosHarness(t, cfg, seed)
	ids := make([]string, cfg.clients)
	for i := range ids {
		ids[i] = fmt.Sprintf("c%02d", i)
	}
	// Concurrent joins: everybody arrives at once, so every subscriber is
	// renegotiated N-1 times while its own first offer is in flight.
	h.parallel(len(ids), func(i int) { h.join(ids[i]) })
	h.verify("initial joins")

	for step := 0; step < cfg.steps; step++ {
		ops := 1 + h.rng.Intn(3)
		type op struct {
			name string
			run  func()
		}
		planned := make([]op, 0, ops)
		busy := map[string]bool{}
		for len(planned) < ops {
			id := ids[h.rng.Intn(len(ids))]
			if busy[id] {
				break
			}
			busy[id] = true
			present := h.present(id)
			switch k := h.rng.Intn(10); {
			case !present:
				planned = append(planned, op{"join " + id, func() { h.join(id) }})
			case k == 0 && h.presentCount() > cfg.clients/2:
				planned = append(planned, op{"leave " + id, func() { h.leave(id) }})
			case k == 1:
				planned = append(planned, op{"reconnect " + id, func() { h.reconnect(id, false) }})
			case k == 2:
				planned = append(planned, op{"rejoin " + id, func() { h.reconnect(id, true) }})
			case k == 3 || k == 4:
				planned = append(planned, op{"ice-restart " + id, func() { h.client(id).reoffer(true) }})
			case k == 5:
				planned = append(planned, op{"reoffer " + id, func() { h.client(id).reoffer(false) }})
			case k == 6 || k == 7:
				flips := 2 + h.rng.Intn(6)
				final := h.rng.Intn(3) == 0
				planned = append(planned, op{fmt.Sprintf("mute-storm %s x%d final=%v", id, flips, final), func() { h.muteStorm(id, flips, final) }})
			default:
				muted := h.rng.Intn(3) == 0
				planned = append(planned, op{fmt.Sprintf("talk-power %s muted=%v", id, muted), func() { h.setServerMuted(id, muted) }})
			}
		}
		names := make([]string, len(planned))
		for i, p := range planned {
			names[i] = p.name
		}
		label := fmt.Sprintf("step %d: %s", step, strings.Join(names, ", "))
		h.logf("%s", label)
		h.parallel(len(planned), func(i int) { planned[i].run() })
		h.verify(label)
	}
}

// ---------------------------------------------------------------------------
// Harness
// ---------------------------------------------------------------------------

type chaosHarness struct {
	t   *testing.T
	cfg chaosConfig
	rng *rand.Rand // operation schedule; test goroutine only

	faultMu  sync.Mutex
	faultRng *rand.Rand // fault decisions; any goroutine
	faults   atomic.Int64
	// dropOffers loses every server offer while set (targeted tests).
	dropOffers atomic.Bool
	// clientStalls counts Pion test-client receive stalls (see verifyOnce).
	clientStalls atomic.Int64

	e *Engine
	r *Router
	v *Voice

	guardMu sync.Mutex // serializes snapshot+install like refreshRolePublishers

	mu          sync.Mutex
	clients     map[string]*chaosClient
	inChannel   map[string]bool
	serverMuted map[string]bool
	logs        []string
	// external participants (a real browser in the integration test) receive
	// server offers through their own signaling.
	external map[string]func(offer string) error
}

func newChaosHarness(t *testing.T, cfg chaosConfig, seed int64) *chaosHarness {
	t.Helper()
	h := &chaosHarness{
		t:           t,
		cfg:         cfg,
		rng:         rand.New(rand.NewSource(seed)),
		faultRng:    rand.New(rand.NewSource(seed ^ 0x5eed)),
		clients:     map[string]*chaosClient{},
		inChannel:   map[string]bool{},
		serverMuted: map[string]bool{},
		external:    map[string]func(string) error{},
	}
	e, err := NewWithNetwork(zap.NewNop(), nil, false, NetworkConfig{UDPAddr: "127.0.0.1:0", ExternalIPs: []string{"127.0.0.1"}})
	if err != nil {
		t.Fatal(err)
	}
	e.iceServers = nil
	h.e = e
	h.r = NewRouter(zap.NewNop())
	h.v = NewVoice(e, h.r, zap.NewNop())
	h.v.renegDebounce = 20 * time.Millisecond
	h.v.renegRateLimit = 150 * time.Millisecond
	h.v.renegAnswer = time.Second
	h.v.SetHandlers(func(id string) bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		return !h.serverMuted[id]
	}, nil)
	// Production refreshes the publisher guard from the live session list
	// before every client offer and inside every server offer's guard.
	h.r.SetPublisherGuard(func(PublisherAccess) bool { return false })
	h.v.SetOfferGuard(func(_ string, send func() error) error {
		h.refreshGuard()
		return send()
	})
	h.v.SetOfferSender(func(id, sdp string) error {
		h.mu.Lock()
		external := h.external[id]
		h.mu.Unlock()
		if external != nil {
			return external(sdp)
		}
		c := h.client(id)
		if c == nil {
			return errors.New("client not connected")
		}
		if h.dropOffers.Load() || h.chance(h.cfg.offerDrop) {
			h.logf("fault: offer to %s lost", id)
			return nil // lost between server and frontend: never answered
		}
		go c.deliverServerOffer(sdp)
		return nil
	})
	if cfg.socketFault > 0 {
		fault := func(datagram []byte) error {
			if isSRTPDatagram(datagram) && h.chance(cfg.socketFault) {
				h.faults.Add(1)
				return io.ErrClosedPipe
			}
			return nil
		}
		mediaSocketFault.Store(&fault)
	}
	t.Cleanup(func() {
		mediaSocketFault.Store(nil)
		h.mu.Lock()
		clients := make([]*chaosClient, 0, len(h.clients))
		for _, c := range h.clients {
			clients = append(clients, c)
		}
		h.mu.Unlock()
		for _, c := range clients {
			c.closeSession()
			_ = h.v.ClosePeer(c.id)
		}
		_ = e.Close()
		t.Logf("server socket faults injected: %d; pion client receive stalls healed: %d", h.faults.Load(), h.clientStalls.Load())
	})
	return h
}

// isSRTPDatagram reports an RTP/RTCP datagram per RFC 7983 demultiplexing
// (STUN and DTLS are left alone: their own retransmissions cover loss, and
// the injected error models a socket closed under in-flight media).
func isSRTPDatagram(b []byte) bool {
	return len(b) > 0 && b[0] >= 128 && b[0] <= 191
}

func (h *chaosHarness) logf(format string, args ...any) {
	line := fmt.Sprintf("%s "+format, append([]any{time.Now().Format("15:04:05.000")}, args...)...)
	h.mu.Lock()
	h.logs = append(h.logs, line)
	if len(h.logs) > 400 {
		h.logs = h.logs[len(h.logs)-400:]
	}
	h.mu.Unlock()
}

func (h *chaosHarness) chance(p float64) bool {
	if p <= 0 {
		return false
	}
	h.faultMu.Lock()
	defer h.faultMu.Unlock()
	return h.faultRng.Float64() < p
}

func (h *chaosHarness) jitter() time.Duration {
	if h.cfg.jitter <= 0 {
		return 0
	}
	h.faultMu.Lock()
	defer h.faultMu.Unlock()
	return time.Duration(h.faultRng.Int63n(int64(h.cfg.jitter)))
}

func (h *chaosHarness) parallel(n int, fn func(i int)) {
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			fn(i)
		}()
	}
	wg.Wait()
}

func (h *chaosHarness) client(id string) *chaosClient {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.clients[id]
}

func (h *chaosHarness) present(id string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.clients[id] != nil && h.inChannel[id]
}

func (h *chaosHarness) presentCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for id := range h.clients {
		if h.inChannel[id] {
			n++
		}
	}
	return n
}

// refreshGuard emulates TCPServer.refreshRolePublishers: snapshot the live
// sessions, then install the guard, atomically.
func (h *chaosHarness) refreshGuard() {
	h.guardMu.Lock()
	defer h.guardMu.Unlock()
	h.mu.Lock()
	live := make(map[string]bool, len(h.clients))
	for id := range h.clients {
		live[id] = true
	}
	for id := range h.external {
		live[id] = true
	}
	h.mu.Unlock()
	h.r.SetPublisherGuard(func(pair PublisherAccess) bool {
		return live[pair.PublisherID] && live[pair.SubscriberID] &&
			pair.ChannelID > 0 && pair.ChannelID == pair.SubscriberChannelID
	})
}

const chaosChannel = 1

func (h *chaosHarness) join(id string) {
	c := &chaosClient{h: h, id: id, recv: map[string]int64{}}
	h.mu.Lock()
	h.clients[id] = c
	h.mu.Unlock()
	joinFirst := h.chance(0.5)
	if joinFirst {
		h.joinChannel(id)
	}
	if err := c.connect(); err != nil {
		h.logf("join %s: connect: %v", id, err)
	}
	if !joinFirst {
		h.joinChannel(id)
	}
}

func (h *chaosHarness) joinChannel(id string) {
	h.mu.Lock()
	h.inChannel[id] = true
	h.mu.Unlock()
	h.refreshGuard()
	h.v.JoinChannel(id, chaosChannel)
}

func (h *chaosHarness) leave(id string) {
	c := h.client(id)
	h.mu.Lock()
	delete(h.clients, id)
	delete(h.inChannel, id)
	h.mu.Unlock()
	h.v.LeaveChannel(id, chaosChannel)
	_ = h.v.ClosePeer(id)
	if c != nil {
		c.closeSession()
	}
	h.refreshGuard()
}

// reconnect replaces the client's peer connection under the same client ID,
// as the desktop does after a tab switch or transport loss. With rejoin the
// old session is closed on the server first (disconnect + login again);
// without it the new DTLS identity replaces the peer in place (59).
func (h *chaosHarness) reconnect(id string, rejoin bool) {
	c := h.client(id)
	if c == nil {
		return
	}
	c.closeSession()
	if rejoin {
		h.leave(id)
		h.join(id)
		return
	}
	if err := c.connect(); err != nil {
		h.logf("reconnect %s: %v", id, err)
	}
}

func (h *chaosHarness) muteStorm(id string, flips int, final bool) {
	c := h.client(id)
	if c == nil {
		return
	}
	for i := 0; i < flips; i++ {
		c.muted.Store(!c.muted.Load())
		time.Sleep(time.Duration(5+h.jitter()/time.Millisecond) * time.Millisecond)
	}
	c.muted.Store(final)
}

func (h *chaosHarness) setServerMuted(id string, muted bool) {
	h.mu.Lock()
	h.serverMuted[id] = muted
	h.mu.Unlock()
}

// verify waits until every listener has received fresh packets from every
// speaker it should hear.
func (h *chaosHarness) verify(label string) {
	h.t.Helper()
	h.verifyOnce(label, 2)
}

// serverOutput reports the SSRC and the count of packets the server's egress
// actually wrote for one (listener, speaker) microphone output.
func (h *chaosHarness) serverOutput(listener, speaker string) (uint32, uint64, bool) {
	h.r.mu.RLock()
	defer h.r.mu.RUnlock()
	t := h.r.pubTracks[listener][speaker]
	if t == nil || t.audio[SlotMic] == nil || t.audio[SlotMic].egress == nil || !t.audio[SlotMic].egress.Active() {
		return 0, 0, false
	}
	e := t.audio[SlotMic].egress
	return e.outputSSRC.Load(), e.written.Load(), true
}

// clientDeclares reports whether the listener's applied remote description
// announces ssrc for the speaker's microphone.
func (c *chaosClient) clientDeclares(speaker string, ssrc uint32) bool {
	pc, _ := c.current()
	if pc == nil || pc.RemoteDescription() == nil {
		return false
	}
	sdp := pc.RemoteDescription().SDP
	return strings.Contains(sdp, fmt.Sprintf("a=ssrc:%d msid:noxa-%s %s", ssrc, speaker, speaker))
}

func (h *chaosHarness) verifyOnce(label string, clientHeals int) {
	h.t.Helper()
	type pair struct{ listener, speaker string }
	h.mu.Lock()
	var listeners []*chaosClient
	speakers := map[string]bool{}
	for id, c := range h.clients {
		if !h.inChannel[id] {
			continue
		}
		listeners = append(listeners, c)
		if !c.muted.Load() && !h.serverMuted[id] {
			speakers[id] = true
		}
	}
	h.mu.Unlock()
	var pairs []pair
	baseline := map[pair]int64{}
	written := map[pair]uint64{}
	for _, l := range listeners {
		for s := range speakers {
			if s != l.id {
				p := pair{l.id, s}
				pairs = append(pairs, p)
				baseline[p] = l.received(s)
				_, written[p], _ = h.serverOutput(l.id, s)
			}
		}
	}
	const fresh = 3
	deadline := time.Now().Add(h.cfg.bound)
	var missing []pair
	for {
		missing = missing[:0]
		for _, p := range pairs {
			if h.client(p.listener).received(p.speaker) < baseline[p]+fresh {
				missing = append(missing, p)
			}
		}
		if len(missing) == 0 {
			break
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	for _, l := range listeners {
		if bad := l.misattributed.Load(); bad > 0 {
			h.t.Errorf("%s: listener %s attributed %d packets to the wrong publisher: %s", label, l.id, bad, l.firstMisattribution())
		}
	}
	if len(missing) == 0 {
		return
	}
	// Separate server failures from receive stalls inside the Pion test
	// client. If the server kept writing SRTP for exactly the SSRC the
	// listener's applied SDP declares for that speaker, the server did its
	// job; the Pion client occasionally never surfaces a receiver whose media
	// section was reused after being inactive (browsers do not share that
	// receiver implementation). Such listeners rebuild their peer connection
	// once, as the desktop does for a dead receiver, and must then recover.
	clientOnly := clientHeals > 0
	stalled := map[string]bool{}
	for _, p := range missing {
		ssrc, now, ok := h.serverOutput(p.listener, p.speaker)
		if !ok || now < written[p]+fresh || !h.client(p.listener).clientDeclares(p.speaker, ssrc) {
			clientOnly = false
			break
		}
		stalled[p.listener] = true
	}
	if clientOnly {
		for listener := range stalled {
			h.clientStalls.Add(1)
			h.logf("pion client receive stall at %s (server delivering); rebuilding its peer", listener)
			h.reconnect(listener, false)
		}
		h.verifyOnce(label+" (after client receive-stall rebuild)", clientHeals-1)
		return
	}
	sort.Slice(missing, func(i, j int) bool {
		return missing[i].listener+missing[i].speaker < missing[j].listener+missing[j].speaker
	})
	var b strings.Builder
	for _, p := range missing {
		fmt.Fprintf(&b, "\n  %s cannot hear %s: %s", p.listener, p.speaker, h.diagnose(p.listener, p.speaker))
	}
	h.mu.Lock()
	logs := strings.Join(h.logs, "\n  ")
	h.mu.Unlock()
	h.t.Fatalf("%s: %d of %d pairs silent after %v:%s\nrecent events:\n  %s", label, len(missing), len(pairs), h.cfg.bound, b.String(), logs)
}

func (h *chaosHarness) diagnose(listener, speaker string) string {
	var parts []string
	h.r.mu.RLock()
	t := h.r.pubTracks[listener][speaker]
	switch {
	case t == nil:
		parts = append(parts, "no server pair")
	case t.audio[SlotMic] == nil:
		parts = append(parts, "pair without mic slot")
	case t.audio[SlotMic].egress != nil && !t.audio[SlotMic].egress.Active():
		parts = append(parts, "egress STOPPED")
	default:
		s := t.audio[SlotMic]
		detail := "server pair ok"
		if s.egress != nil {
			s.egress.mu.RLock()
			retry := time.Until(s.egress.retryAfter).Round(time.Millisecond)
			s.egress.mu.RUnlock()
			detail += fmt.Sprintf(" ssrc=%d failures=%d retryIn=%v admitted=%d written=%d", s.egress.outputSSRC.Load(), s.egress.failures.Load(), retry, s.egress.admitted.Load(), s.egress.written.Load())
		}
		if pc := h.r.pubPeers[listener]; pc != nil {
			for _, tr := range pc.pc.GetTransceivers() {
				if tr.Sender() == s.sender {
					detail += " mid=" + tr.Mid() + " dir=" + tr.Direction().String()
				}
			}
			if s.egress != nil {
				outbound := 0
				for _, raw := range pc.pc.GetStats() {
					if out, ok := raw.(pion.OutboundRTPStreamStats); ok {
						outbound++
						if uint32(out.SSRC) == s.egress.outputSSRC.Load() {
							detail += fmt.Sprintf(" packetsSent=%d", out.PacketsSent)
						}
					}
				}
				detail += fmt.Sprintf(" outboundStats=%d senderTrack=%v", outbound, s.sender.Track() != nil)
			}
		}
		parts = append(parts, detail)
	}
	_, claimed := h.r.slotClaims[speaker][SlotMic]
	parts = append(parts, fmt.Sprintf("speaker mic claimed=%v", claimed))
	h.r.mu.RUnlock()
	h.v.renegMu.Lock()
	if st := h.v.reneg[listener]; st != nil {
		parts = append(parts, fmt.Sprintf("reneg pending=%v unanswered=%d", st.pending, st.unanswered))
	}
	h.v.renegMu.Unlock()
	if w := h.e.PeerConnection(listener); w != nil {
		parts = append(parts, "server pc "+w.pc.SignalingState().String()+"/"+w.pc.ConnectionState().String())
	} else {
		parts = append(parts, "no server pc")
	}
	if c := h.client(listener); c != nil {
		parts = append(parts, c.describe(speaker))
		if os.Getenv("NOXA_VOICE_CHAOS_SDP") != "" {
			mid := ""
			h.r.mu.RLock()
			if t := h.r.pubTracks[listener][speaker]; t != nil && t.audio[SlotMic] != nil {
				if pc := h.r.pubPeers[listener]; pc != nil {
					for _, tr := range pc.pc.GetTransceivers() {
						if tr.Sender() == t.audio[SlotMic].sender {
							mid = tr.Mid()
						}
					}
				}
			}
			h.r.mu.RUnlock()
			if w := h.e.PeerConnection(listener); w != nil && mid != "" {
				if d := w.pc.LocalDescription(); d != nil {
					parts = append(parts, "\n    server local: "+sdpSection(d.SDP, mid))
				}
				if d := w.pc.RemoteDescription(); d != nil {
					parts = append(parts, "\n    server remote: "+sdpSection(d.SDP, mid))
				}
			}
			if pc, _ := c.current(); pc != nil && mid != "" {
				if d := pc.RemoteDescription(); d != nil {
					parts = append(parts, "\n    client remote: "+sdpSection(d.SDP, mid))
				}
				parts = append(parts, "\n    client history:"+c.historyFor(mid))
			}
		}
	}
	return strings.Join(parts, "; ")
}

func sdpSection(sdp, mid string) string {
	for _, section := range strings.Split(sdp, "\nm=") {
		if strings.Contains(section, "a=mid:"+mid+"\r") {
			var keep []string
			for _, line := range strings.Split(section, "\r\n") {
				if strings.HasPrefix(line, "audio") || strings.HasPrefix(line, "a=send") || strings.HasPrefix(line, "a=recv") ||
					strings.HasPrefix(line, "a=inactive") || strings.HasPrefix(line, "a=msid") || strings.HasPrefix(line, "a=ssrc:") && strings.Contains(line, "cname") {
					keep = append(keep, line)
				}
			}
			return strings.Join(keep, " | ")
		}
	}
	return "(mid absent)"
}

// ---------------------------------------------------------------------------
// Simulated desktop client
// ---------------------------------------------------------------------------

type chaosClient struct {
	h  *chaosHarness
	id string

	negMu sync.Mutex // one negotiation at a time, like queuePeerNegotiation

	mu           sync.Mutex
	pc           *pion.PeerConnection
	mux          interface{ Close() error }
	done         chan struct{}
	pendingOffer string
	gen          int

	muted atomic.Bool

	recvMu        sync.Mutex
	recv          map[string]int64
	misattributed atomic.Int64
	misattrDetail string
	bound         map[uint32]string // remote SSRC -> publisher, set by onTrack
	history       []string
}

// record keeps the remote descriptions this client applied (diagnostics).
func (c *chaosClient) record(kind, sdp string) {
	c.recvMu.Lock()
	c.history = append(c.history, time.Now().Format("15:04:05.000")+" "+kind+"\x00"+sdp)
	if len(c.history) > 40 {
		c.history = c.history[len(c.history)-40:]
	}
	c.recvMu.Unlock()
}

func (c *chaosClient) historyFor(mid string) string {
	c.recvMu.Lock()
	defer c.recvMu.Unlock()
	var b strings.Builder
	for _, entry := range c.history {
		head, sdp, _ := strings.Cut(entry, "\x00")
		fmt.Fprintf(&b, "\n      %s: %s", head, sdpSection(sdp, mid))
	}
	return b.String()
}

func (c *chaosClient) received(speaker string) int64 {
	c.recvMu.Lock()
	defer c.recvMu.Unlock()
	return c.recv[speaker]
}

func (c *chaosClient) firstMisattribution() string {
	c.recvMu.Lock()
	defer c.recvMu.Unlock()
	return c.misattrDetail
}

func (c *chaosClient) current() (*pion.PeerConnection, chan struct{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.pc, c.done
}

func (c *chaosClient) closeSession() {
	c.mu.Lock()
	pc, mux, done := c.pc, c.mux, c.done
	c.pc, c.mux, c.done = nil, nil, nil
	c.mu.Unlock()
	if done != nil {
		close(done)
	}
	if pc != nil {
		_ = pc.Close()
	}
	if mux != nil {
		_ = mux.Close()
	}
}

func (c *chaosClient) connect() error {
	h := c.h
	conn, err := (&net.ListenConfig{}).ListenPacket(context.Background(), "udp4", "127.0.0.1:0")
	if err != nil {
		return err
	}
	mux := pion.NewICEUDPMux(nil, &impairedPacketConn{PacketConn: conn, h: h})
	settings := pion.SettingEngine{}
	settings.SetICEUDPMux(mux)
	settings.SetNetworkTypes([]pion.NetworkType{pion.NetworkTypeUDP4})
	settings.SetIncludeLoopbackCandidate(true)
	media := &pion.MediaEngine{}
	if err := registerCodecs(media, false); err != nil {
		_ = mux.Close()
		return err
	}
	pc, err := pion.NewAPI(pion.WithMediaEngine(media), pion.WithSettingEngine(settings)).NewPeerConnection(pion.Configuration{})
	if err != nil {
		_ = mux.Close()
		return err
	}
	c.mu.Lock()
	c.gen++
	gen := c.gen
	c.mu.Unlock()
	track, err := pion.NewTrackLocalStaticRTP(pion.RTPCodecCapability{MimeType: pion.MimeTypeOpus, ClockRate: 48000, Channels: 2},
		fmt.Sprintf("mic-%s-%d", c.id, gen), "local-"+c.id)
	if err != nil {
		_ = pc.Close()
		_ = mux.Close()
		return err
	}
	if _, err := pc.AddTransceiverFromTrack(track, pion.RTPTransceiverInit{Direction: pion.RTPTransceiverDirectionSendrecv}); err != nil {
		_ = pc.Close()
		_ = mux.Close()
		return err
	}
	done := make(chan struct{})
	pc.OnICECandidate(func(candidate *pion.ICECandidate) {
		if candidate == nil {
			return
		}
		if cur, _ := c.current(); cur != pc {
			return
		}
		init := candidate.ToJSON()
		mid := ""
		if init.SDPMid != nil {
			mid = *init.SDPMid
		}
		var index uint16
		if init.SDPMLineIndex != nil {
			index = *init.SDPMLineIndex
		}
		_ = h.v.AddICECandidate(c.id, init.Candidate, mid, index)
	})
	pc.OnTrack(func(remote *pion.TrackRemote, receiver *pion.RTPReceiver) { c.onTrack(pc, remote, receiver) })
	c.mu.Lock()
	c.pc, c.mux, c.done = pc, mux, done
	c.pendingOffer = ""
	c.mu.Unlock()
	go c.speak(pc, track, done)
	return c.offer(pc, false)
}

func (c *chaosClient) reoffer(iceRestart bool) {
	pc, _ := c.current()
	if pc == nil {
		return
	}
	if err := c.offer(pc, iceRestart); err != nil {
		c.h.logf("%s reoffer(ice=%v): %v", c.id, iceRestart, err)
	}
}

// offer mirrors negotiateOffer in video-negotiation.js, including the
// collision path that consumes the crossing server offer and retries.
func (c *chaosClient) offer(pc *pion.PeerConnection, iceRestart bool) error {
	c.negMu.Lock()
	defer c.negMu.Unlock()
	h := c.h
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if cur, _ := c.current(); cur != pc {
			return errors.New("session replaced")
		}
		offer, err := pc.CreateOffer(&pion.OfferOptions{ICERestart: iceRestart})
		if err != nil {
			return err
		}
		// A browser applies its offer first and rolls it back on collision.
		// Pion cannot roll back a local offer, so the simulated client applies
		// its offer only once the server accepted it: same observable
		// signaling, no wedged have-local-offer state in the test client.
		h.refreshGuard()
		answer, err := h.v.HandleOffer(c.id, offer.SDP, func(candidate, mid string, index uint16) {
			// Like main.js, queue server candidates behind the negotiation in
			// flight: a candidate of a restarted ICE generation is useless (and
			// dropped) before the answer carrying its credentials is applied.
			go func() {
				c.negMu.Lock()
				defer c.negMu.Unlock()
				if cur, _ := c.current(); cur == pc && pc.RemoteDescription() != nil {
					_ = pc.AddICECandidate(pion.ICECandidateInit{Candidate: candidate, SDPMid: &mid, SDPMLineIndex: &index})
				}
			}()
		})
		if err == nil {
			if err := pc.SetLocalDescription(offer); err != nil {
				return fmt.Errorf("apply accepted offer: %w", err)
			}
			c.record("answer", answer)
			if err := pc.SetRemoteDescription(pion.SessionDescription{Type: pion.SDPTypeAnswer, SDP: answer}); err != nil {
				return fmt.Errorf("apply answer: %w", err)
			}
			return nil
		}
		lastErr = err
		if !errors.Is(err, ErrOfferCollision) {
			return err
		}
		h.logf("%s offer collided (attempt %d)", c.id, attempt)
		for wait := 0; wait < 20; wait++ {
			c.mu.Lock()
			pending := c.pendingOffer != ""
			c.mu.Unlock()
			if pending {
				break
			}
			time.Sleep(25 * time.Millisecond)
		}
		if !c.applyPendingOfferLocked(pc) {
			return err
		}
	}
	return lastErr
}

func (c *chaosClient) deliverServerOffer(sdp string) {
	time.Sleep(c.h.jitter())
	c.mu.Lock()
	pc := c.pc
	c.pendingOffer = sdp
	c.mu.Unlock()
	if pc == nil {
		return
	}
	c.negMu.Lock()
	defer c.negMu.Unlock()
	c.applyPendingOfferLocked(pc)
}

func iceUfrag(sdp string) string {
	for _, line := range strings.Split(sdp, "\n") {
		if strings.HasPrefix(line, "a=ice-ufrag:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "a=ice-ufrag:"))
		}
	}
	return ""
}

const pionReceiverSettle = 50 * time.Millisecond

// applyPendingOfferLocked mirrors applyPendingRemoteOffer: offers for a
// superseded transport (other ICE credentials) are dropped unanswered.
func (c *chaosClient) applyPendingOfferLocked(pc *pion.PeerConnection) bool {
	c.mu.Lock()
	sdp := c.pendingOffer
	c.pendingOffer = ""
	current := c.pc == pc
	c.mu.Unlock()
	if sdp == "" || !current {
		return false
	}
	remote := pc.RemoteDescription()
	if remote == nil || iceUfrag(remote.SDP) != iceUfrag(sdp) {
		c.h.logf("%s dropped server offer for superseded transport", c.id)
		return false
	}
	c.record("offer", sdp)
	if err := pc.SetRemoteDescription(pion.SessionDescription{Type: pion.SDPTypeOffer, SDP: sdp}); err != nil {
		c.h.logf("%s apply server offer: %v", c.id, err)
		return false
	}
	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		c.h.logf("%s create answer: %v", c.id, err)
		return false
	}
	if err := pc.SetLocalDescription(answer); err != nil {
		c.h.logf("%s set answer: %v", c.id, err)
		return false
	}
	// Pion (unlike a browser) opens the new receivers' SRTP streams in an
	// asynchronous operation after SetLocalDescription returns. Media the
	// server sends the moment it applies our answer can otherwise take Pion's
	// undeclared-SSRC path, which may leave the receiver reading a closed
	// stream for good: a test-client artifact, not server behaviour.
	time.Sleep(pionReceiverSettle)
	if err := c.h.v.HandleAnswer(c.id, answer.SDP); err != nil {
		c.h.logf("%s server rejected answer: %v", c.id, err)
	} else if os.Getenv("NOXA_VOICE_CHAOS_SDP") != "" {
		c.h.logf("%s answered server offer", c.id)
	}
	return true
}

// speak sends Opus packets whose payload names the publisher, so every
// listener can check the attribution it derived from SDP.
func (c *chaosClient) speak(pc *pion.PeerConnection, track *pion.TrackLocalStaticRTP, done chan struct{}) {
	ticker := time.NewTicker(c.h.cfg.packetEvery)
	defer ticker.Stop()
	payload := append([]byte{0xf8}, []byte("noxa:"+c.id)...)
	var seq uint16
	var ts uint32
	for {
		select {
		case <-done:
			return
		case <-ticker.C:
		}
		seq++
		ts += 960
		if c.muted.Load() || pc.ConnectionState() != pion.PeerConnectionStateConnected {
			continue
		}
		_ = track.WriteRTP(&rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: 111, SequenceNumber: seq, Timestamp: ts}, Payload: payload})
	}
}

// remoteMicIDs is the Go twin of remoteTrackIDs (media-track-id.js): the
// current remote description's a=msid track ID per active mid.
func remoteMicIDs(sdp string) map[string]string {
	out := map[string]string{}
	mid, trackID, active := "", "", false
	flush := func() {
		if mid != "" {
			if active {
				out[mid] = trackID
			} else {
				out[mid] = ""
			}
		}
	}
	for _, raw := range strings.Split(sdp, "\n") {
		line := strings.TrimRight(raw, "\r")
		switch {
		case strings.HasPrefix(line, "m="):
			flush()
			fields := strings.Fields(line[2:])
			mid, trackID = "", ""
			active = len(fields) > 1 && (fields[0] == "audio" || fields[0] == "video") && fields[1] != "0"
		case strings.HasPrefix(line, "a=mid:"):
			mid = line[len("a=mid:"):]
		case strings.HasPrefix(line, "a=msid:"):
			if parts := strings.Fields(line[len("a=msid:"):]); len(parts) > 1 {
				trackID = parts[1]
			}
		case line == "a=inactive" || line == "a=recvonly":
			active = false
		}
	}
	flush()
	return out
}

func (c *chaosClient) onTrack(pc *pion.PeerConnection, remote *pion.TrackRemote, receiver *pion.RTPReceiver) {
	if remote.Kind() != pion.RTPCodecTypeAudio {
		return
	}
	mid := ""
	for _, transceiver := range pc.GetTransceivers() {
		if transceiver.Receiver() == receiver {
			mid = transceiver.Mid()
			break
		}
	}
	description := pc.RemoteDescription()
	if description == nil {
		return
	}
	mapped := remoteMicIDs(description.SDP)[mid]
	publisher, slot, _ := strings.Cut(mapped, slotSep)
	if slot != "" {
		return
	}
	c.recvMu.Lock()
	if c.bound == nil {
		c.bound = map[uint32]string{}
	}
	c.bound[uint32(remote.SSRC())] = publisher
	c.recvMu.Unlock()
	go func() {
		for {
			pkt, _, err := remote.ReadRTP()
			if err != nil {
				return
			}
			if len(pkt.Payload) == 0 || pkt.Payload[0]>>3 != 31 || pkt.Payload[0]&3 != 0 {
				continue // not the decodable single-frame Opus packet we send
			}
			sender, marked := strings.CutPrefix(string(pkt.Payload[1:]), "noxa:")
			if !marked {
				c.h.mu.Lock()
				external := c.h.external[publisher] != nil
				c.h.mu.Unlock()
				if external {
					continue // real Opus from a browser participant
				}
			}
			c.recvMu.Lock()
			if sender != publisher {
				if c.misattributed.Add(1) == 1 {
					c.misattrDetail = fmt.Sprintf("mid %s mapped to %q (pion id %q) carried %q", mid, publisher, remote.ID(), sender)
				}
			} else {
				c.recv[sender]++
			}
			c.recvMu.Unlock()
		}
	}()
}

func (c *chaosClient) describe(speaker string) string {
	pc, _ := c.current()
	if pc == nil {
		return "client has no pc"
	}
	state := "client pc " + pc.SignalingState().String() + "/" + pc.ConnectionState().String()
	if remote := pc.RemoteDescription(); remote != nil {
		var mids []string
		for mid, id := range remoteMicIDs(remote.SDP) {
			if id == speaker {
				mids = append(mids, mid)
			}
		}
		state += fmt.Sprintf("; client SDP announces speaker at mids %v", mids)
		for _, tr := range pc.GetTransceivers() {
			for _, mid := range mids {
				if tr.Mid() != mid {
					continue
				}
				var ssrcs []uint32
				for _, track := range tr.Receiver().Tracks() {
					ssrcs = append(ssrcs, uint32(track.SSRC()))
				}
				state += fmt.Sprintf(" (client mid %s dir=%s receiver ssrcs=%v)", mid, tr.Direction(), ssrcs)
				c.recvMu.Lock()
				for _, ssrc := range ssrcs {
					publisher, ok := c.bound[ssrc]
					state += fmt.Sprintf(" ontrack[%d]=%v:%q", ssrc, ok, publisher)
				}
				c.recvMu.Unlock()
				for _, raw := range pc.GetStats() {
					if in, ok := raw.(pion.InboundRTPStreamStats); ok {
						for _, ssrc := range ssrcs {
							if uint32(in.SSRC) == ssrc {
								state += fmt.Sprintf(" packetsReceived=%d", in.PacketsReceived)
							}
						}
					}
				}
			}
		}
	}
	return state + fmt.Sprintf("; packets so far=%d", c.received(speaker))
}

// impairedPacketConn drops, delays and thereby reorders client datagrams.
type impairedPacketConn struct {
	net.PacketConn
	h *chaosHarness
}

func (c *impairedPacketConn) WriteTo(b []byte, addr net.Addr) (int, error) {
	if c.h.chance(c.h.cfg.loss) {
		return len(b), nil
	}
	if d := c.h.jitter(); d > time.Millisecond {
		buf := append([]byte(nil), b...)
		time.AfterFunc(d, func() { _, _ = c.PacketConn.WriteTo(buf, addr) })
		return len(b), nil
	}
	return c.PacketConn.WriteTo(b, addr)
}

func (c *impairedPacketConn) ReadFrom(b []byte) (int, net.Addr, error) {
	for {
		n, addr, err := c.PacketConn.ReadFrom(b)
		if err != nil || !c.h.chance(c.h.cfg.loss) {
			return n, addr, err
		}
	}
}
