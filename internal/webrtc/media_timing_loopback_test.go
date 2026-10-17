package webrtc

import (
	"errors"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	pion "github.com/pion/webrtc/v4"

	"noxa/internal/mediacc/ntp"
)

// The publisher's clock runs one hour ahead of the server. Receivers must see
// the publisher's capture-time mapping, not the server's forwarding time.
const timingTestClockOffset = time.Hour

type timingKind struct {
	name    string
	slot    string
	trackID string
	codec   pion.RTPCodecParameters
	video   string // "", "h264" or "h265"
}

var (
	timingOpus = pion.RTPCodecParameters{RTPCodecCapability: pion.RTPCodecCapability{MimeType: pion.MimeTypeOpus, ClockRate: 48000, Channels: 2, SDPFmtpLine: "minptime=10;useinbandfec=1"}, PayloadType: 111}
	timingH264 = pion.RTPCodecParameters{RTPCodecCapability: pion.RTPCodecCapability{MimeType: pion.MimeTypeH264, ClockRate: 90000, SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42001f"}, PayloadType: 102}
	timingH265 = pion.RTPCodecParameters{RTPCodecCapability: pion.RTPCodecCapability{MimeType: pion.MimeTypeH265, ClockRate: 90000, SDPFmtpLine: "profile-id=1;tier-flag=0;level-id=93;tx-mode=SRST"}, PayloadType: 116}
)

var timingKinds = []timingKind{
	{name: "mic-opus", slot: SlotMic, trackID: "mic-track", codec: timingOpus},
	{name: "screen-audio-opus", slot: SlotScreenAudio, trackID: "screen-audio-track", codec: timingOpus},
	{name: "screen-h264", slot: SlotScreen, trackID: "screen-track", codec: timingH264, video: "h264"},
	{name: "screen-h265", slot: SlotScreen, trackID: "screen-track", codec: timingH265, video: "h265"},
}

func timingMediaEngine(t *testing.T, codecs ...pion.RTPCodecParameters) *pion.MediaEngine {
	t.Helper()
	me := &pion.MediaEngine{}
	if err := me.RegisterCodec(timingOpus, pion.RTPCodecTypeAudio); err != nil {
		t.Fatal(err)
	}
	// VP8 is the server's fallback binding for an unwatched camera slot.
	vp8 := pion.RTPCodecParameters{RTPCodecCapability: pion.RTPCodecCapability{MimeType: pion.MimeTypeVP8, ClockRate: 90000}, PayloadType: 96}
	for _, codec := range append([]pion.RTPCodecParameters{vp8}, codecs...) {
		if err := me.RegisterCodec(codec, pion.RTPCodecTypeVideo); err != nil {
			t.Fatal(err)
		}
		if err := me.RegisterCodec(pion.RTPCodecParameters{RTPCodecCapability: pion.RTPCodecCapability{MimeType: pion.MimeTypeRTX, ClockRate: 90000, SDPFmtpLine: "apt=" + strconv.Itoa(int(codec.PayloadType))}, PayloadType: codec.PayloadType + 1}, pion.RTPCodecTypeVideo); err != nil {
			t.Fatal(err)
		}
	}
	return me
}

func timingLoopbackSettings() pion.SettingEngine {
	settings := pion.SettingEngine{}
	settings.SetIncludeLoopbackCandidate(true)
	settings.SetIPFilter(func(ip net.IP) bool { return ip.IsLoopback() })
	settings.SetNetworkTypes([]pion.NetworkType{pion.NetworkTypeUDP4})
	return settings
}

// newTimingPublisher has NO SR interceptor: the test writes the publisher's
// SRs itself so their NTP<->RTP mapping is known exactly.
func newTimingPublisher(t *testing.T, kinds ...timingKind) *pion.PeerConnection {
	t.Helper()
	var videos []pion.RTPCodecParameters
	for _, k := range kinds {
		if k.video != "" {
			videos = append(videos, k.codec)
		}
	}
	api := pion.NewAPI(pion.WithMediaEngine(timingMediaEngine(t, videos...)), pion.WithInterceptorRegistry(&interceptor.Registry{}), pion.WithSettingEngine(timingLoopbackSettings()))
	pc, err := api.NewPeerConnection(pion.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pc.Close() })
	return pc
}

// newTimingViewer is a regular pion receiver with default interceptors.
func newTimingViewer(t *testing.T) *pion.PeerConnection {
	t.Helper()
	me := timingMediaEngine(t, timingH264, timingH265)
	registry := &interceptor.Registry{}
	if err := pion.RegisterDefaultInterceptors(me, registry); err != nil {
		t.Fatal(err)
	}
	pc, err := pion.NewAPI(pion.WithMediaEngine(me), pion.WithInterceptorRegistry(registry), pion.WithSettingEngine(timingLoopbackSettings())).NewPeerConnection(pion.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pc.Close() })
	return pc
}

func timingGather(pc *pion.PeerConnection, description pion.SessionDescription) error {
	done := pion.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(description); err != nil {
		return err
	}
	select {
	case <-done:
		return nil
	case <-time.After(5 * time.Second):
		return errors.New("gather timeout")
	}
}

// negotiateTimingPublisher runs the publisher's offer against the server peer.
func negotiateTimingPublisher(t *testing.T, server *PeerConnectionWrapper, publisher *pion.PeerConnection) {
	t.Helper()
	offer, err := publisher.CreateOffer(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := timingGather(publisher, offer); err != nil {
		t.Fatal(err)
	}
	if err := server.pc.SetRemoteDescription(*publisher.LocalDescription()); err != nil {
		t.Fatal(err)
	}
	answer, err := server.pc.CreateAnswer(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := timingGather(server.pc, answer); err != nil {
		t.Fatal(err)
	}
	if err := publisher.SetRemoteDescription(*server.pc.LocalDescription()); err != nil {
		t.Fatal(err)
	}
}

type timingSR struct {
	trackID string
	report  rtcp.SenderReport
	at      time.Time
}

type timingViewerState struct {
	pc        *pion.PeerConnection
	server    *PeerConnectionWrapper
	negotiate sync.Mutex
	mu        sync.Mutex
	reports   []timingSR
	packets   map[string]*atomic.Int64
	ssrcs     map[string]uint32
	first     map[string]rtp.Header
}

func (v *timingViewerState) count(trackID string) int64 {
	v.mu.Lock()
	c := v.packets[trackID]
	v.mu.Unlock()
	if c == nil {
		return 0
	}
	return c.Load()
}

func (v *timingViewerState) srs(trackID string) []timingSR {
	v.mu.Lock()
	defer v.mu.Unlock()
	var out []timingSR
	for _, r := range v.reports {
		if r.trackID == trackID {
			out = append(out, r)
		}
	}
	return out
}

func attachTimingViewer(t *testing.T, engine *Engine, router *Router, id string) *timingViewerState {
	t.Helper()
	server, err := engine.NewPeerConnection(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := router.AttachPeer(id, server); err != nil {
		t.Fatal(err)
	}
	state := &timingViewerState{pc: newTimingViewer(t), server: server, packets: make(map[string]*atomic.Int64), ssrcs: make(map[string]uint32), first: make(map[string]rtp.Header)}
	state.pc.OnTrack(func(track *pion.TrackRemote, receiver *pion.RTPReceiver) {
		id := track.ID()
		counter := new(atomic.Int64)
		state.mu.Lock()
		state.packets[id] = counter
		state.ssrcs[id] = uint32(track.SSRC())
		state.mu.Unlock()
		go func() {
			for {
				packets, _, err := receiver.ReadRTCP()
				if err != nil {
					return
				}
				for _, packet := range packets {
					if sr, ok := packet.(*rtcp.SenderReport); ok && sr.SSRC == uint32(track.SSRC()) {
						state.mu.Lock()
						state.reports = append(state.reports, timingSR{trackID: id, report: *sr, at: time.Now()})
						state.mu.Unlock()
					}
				}
			}
		}()
		for {
			packet, _, err := track.ReadRTP()
			if err != nil {
				return
			}
			state.mu.Lock()
			if _, ok := state.first[id]; !ok {
				state.first[id] = packet.Header
			}
			state.mu.Unlock()
			counter.Add(1)
		}
	})
	return state
}

func negotiateTimingViewer(t *testing.T, viewer *timingViewerState) {
	t.Helper()
	if err := negotiateStartupTestPeerError(viewer.server, viewer.pc); err != nil {
		t.Fatal(err)
	}
}

func timingWait(t *testing.T, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// timingSource sends one publisher track with timestamps derived from a
// capture clock, plus SRs carrying that clock's NTP<->RTP mapping.
type timingSource struct {
	kind   timingKind
	track  *pion.TrackLocalStaticRTP
	sender *pion.RTPSender
	pc     *pion.PeerConnection
	base   uint32
	start  time.Time
	sent   atomic.Int64
	stopSR atomic.Bool
	pause  atomic.Bool
	// manualKeys sends H.264 delta frames except when forceKey is set.
	manualKeys bool
	forceKey   atomic.Bool
}

func (s *timingSource) rate() uint32 { return s.kind.codec.ClockRate }

func (s *timingSource) timestampAt(at time.Time) uint32 {
	return s.base + uint32(rtpTicks(at.Sub(s.start), s.rate()))
}

func (s *timingSource) ssrc() uint32 {
	return uint32(s.sender.GetParameters().Encodings[0].SSRC)
}

// expectedRTP is the source timestamp the publisher mapping assigns to a
// publisher-clock NTP time.
func (s *timingSource) expectedRTP(ntpTime uint64) uint32 {
	serverTime := ntp.ToTime(ntpTime).Add(-timingTestClockOffset)
	return s.timestampAt(serverTime)
}

func (s *timingSource) payload(seq uint16) [][]byte {
	switch s.kind.video {
	case "h264":
		if s.manualKeys && !s.forceKey.Swap(false) {
			return [][]byte{h264TestDelta}
		}
		return [][]byte{h264TestSTAP(h264TestSPS(640, 360), h264TestPPS, h264TestIDR)}
	case "h265":
		return [][]byte{hevcKey(19)}
	default:
		return [][]byte{{0xf8, 0xff, 0xfe}}
	}
}

func (s *timingSource) run(t *testing.T, done <-chan struct{}) {
	interval := 20 * time.Millisecond
	if s.kind.video != "" {
		interval = 33 * time.Millisecond
	}
	tick := time.NewTicker(interval)
	defer tick.Stop()
	reportTick := time.NewTicker(250 * time.Millisecond)
	defer reportTick.Stop()
	var seq uint16
	for {
		select {
		case <-done:
			return
		case now := <-tick.C:
			if s.pause.Load() {
				continue
			}
			// Capture happened 80 ms before the send: SRs are about capture.
			ts := s.timestampAt(now.Add(-80 * time.Millisecond))
			for _, payload := range s.payload(seq) {
				seq++
				if err := s.track.WriteRTP(&rtp.Packet{Header: rtp.Header{Version: 2, SequenceNumber: seq, Timestamp: ts, Marker: true}, Payload: payload}); err != nil {
					return
				}
				s.sent.Add(1)
			}
		case now := <-reportTick.C:
			if s.stopSR.Load() || s.sent.Load() == 0 {
				continue
			}
			sr := &rtcp.SenderReport{SSRC: s.ssrc(), NTPTime: ntp.ToNTP(now.Add(timingTestClockOffset)), RTPTime: s.timestampAt(now), PacketCount: uint32(s.sent.Load())}
			_ = s.pc.WriteRTCP([]rtcp.Packet{sr})
		}
	}
}

type timingFixture struct {
	engine      *Engine
	router      *Router
	publisher   *pion.PeerConnection
	server      *PeerConnectionWrapper
	sources     []*timingSource
	publication []timingPublication
	done        chan struct{}
	allowed     atomic.Bool
	mu          sync.Mutex
	viewers     map[string]*timingViewerState
}

type timingOptions struct {
	manualKeys bool
	innermost  []interceptor.Factory
}

func newTimingFixture(t *testing.T, kinds ...timingKind) *timingFixture {
	t.Helper()
	return newTimingFixtureOptions(t, timingOptions{}, kinds...)
}

func newTimingFixtureOptions(t *testing.T, opts timingOptions, kinds ...timingKind) *timingFixture {
	t.Helper()
	engine, err := NewWithNetwork(testLogger(), nil, false, NetworkConfig{UDPAddr: "127.0.0.1:0", ExternalIPs: []string{"127.0.0.1"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = engine.Close() })
	engine.iceServers = nil
	if len(opts.innermost) > 0 {
		me, ir, err := newEngineMediaObserved(false, VideoBounds{}, engine.egress, engine.upstreamFeedback, opts.innermost...)
		if err != nil {
			t.Fatal(err)
		}
		engine.api = pion.NewAPI(pion.WithMediaEngine(me), pion.WithInterceptorRegistry(ir), pion.WithSettingEngine(engine.settingEngine))
	}
	router := NewRouter(nil)
	router.JoinChannel(1, "pub")
	f := &timingFixture{engine: engine, router: router, done: make(chan struct{}), viewers: make(map[string]*timingViewerState)}
	router.SetRenegotiateHook(f.renegotiate)
	f.allowed.Store(true)
	// Authority can be revoked per test while every connection persists.
	router.SetMediaGuard(func(delivery MediaDelivery, write func() error) error {
		if !f.allowed.Load() && delivery.SenderID == "pub" {
			return nil
		}
		return write()
	})
	slots := map[string]string{}
	for _, k := range kinds {
		if k.slot != SlotMic {
			slots[k.trackID] = k.slot
		}
	}
	router.SetTrackSlots("pub", slots)
	server, err := engine.NewPeerConnection("pub")
	if err != nil {
		t.Fatal(err)
	}
	if err := router.AttachPeer("pub", server); err != nil {
		t.Fatal(err)
	}
	f.server, f.publisher = server, newTimingPublisher(t, kinds...)
	for _, k := range kinds {
		// Screen audio is delivered to viewers of the screen publication.
		slot, codecs, level := k.slot, []string{"vp8"}, uint8(0)
		switch {
		case k.slot == SlotScreenAudio:
			slot = SlotScreen
		case k.video == "h264":
			codecs = append(codecs, "h264")
		case k.video == "h265":
			codecs, level = append(codecs, "h265"), 93
		default:
			continue
		}
		generation, err := router.PublishVideoWithFormats("pub", slot, 0, true, "source", codecs, level)
		if err != nil {
			t.Fatal(err)
		}
		f.publication = append(f.publication, timingPublication{slot: slot, generation: generation, codecs: codecs, level: level})
	}
	for i, k := range kinds {
		track, err := pion.NewTrackLocalStaticRTP(k.codec.RTPCodecCapability, k.trackID, "pub-"+k.slot)
		if err != nil {
			t.Fatal(err)
		}
		sender, err := f.publisher.AddTrack(track)
		if err != nil {
			t.Fatal(err)
		}
		go func() {
			for {
				if _, _, err := sender.ReadRTCP(); err != nil {
					return
				}
			}
		}()
		f.sources = append(f.sources, &timingSource{kind: k, track: track, sender: sender, pc: f.publisher, base: 0x10000000 * uint32(i+1), start: time.Now(), manualKeys: opts.manualKeys})
	}
	negotiateTimingPublisher(t, server, f.publisher)
	timingWait(t, "publisher transport", 10*time.Second, func() bool {
		return f.publisher.ConnectionState() == pion.PeerConnectionStateConnected
	})
	t.Cleanup(func() {
		select {
		case <-f.done:
		default:
			close(f.done)
		}
	})
	for _, s := range f.sources {
		go s.run(t, f.done)
	}
	for _, k := range kinds {
		if k.video == "" {
			continue
		}
		timingWait(t, "publisher video codec", 10*time.Second, func() bool {
			router.mu.RLock()
			defer router.mu.RUnlock()
			_, ok := router.videoCodecs[videoSourceRef{"pub", k.slot}]
			return ok
		})
	}
	return f
}

type timingPublication struct {
	slot       string
	generation uint64
	codecs     []string
	level      uint8
}

func (f *timingFixture) watch(t *testing.T, viewer string, active bool, revision uint64) {
	t.Helper()
	for _, p := range f.publication {
		if _, err := f.router.WatchVideoWithFormats(viewer, "pub", p.slot, p.generation, revision, f.router.VideoWatchSession(viewer), active, p.codecs, p.level); err != nil {
			t.Fatal(err)
		}
	}
}

func (f *timingFixture) addViewer(t *testing.T, id string) *timingViewerState {
	t.Helper()
	f.router.JoinChannel(1, id)
	f.watch(t, id, true, 1)
	viewer := attachTimingViewer(t, f.engine, f.router, id)
	f.router.PrepareSubscriber(id)
	negotiateTimingViewer(t, viewer)
	timingWait(t, id+" transport", 10*time.Second, func() bool {
		return viewer.pc.ConnectionState() == pion.PeerConnectionStateConnected
	})
	f.mu.Lock()
	f.viewers[id] = viewer
	f.mu.Unlock()
	return viewer
}

// renegotiate follows server-side output rebuilds (e.g. an HEVC binding
// that falls back while unwatched), as the signaling layer does in production.
func (f *timingFixture) renegotiate(subscriber string) {
	f.mu.Lock()
	viewer := f.viewers[subscriber]
	f.mu.Unlock()
	if viewer == nil {
		return
	}
	go func() {
		viewer.negotiate.Lock()
		defer viewer.negotiate.Unlock()
		_ = negotiateStartupTestPeerError(viewer.server, viewer.pc)
	}()
}

func outputTrackID(kind timingKind) string { return slotTrackID("pub", kind.slot) }

// assertPublisherMapping checks every SR the viewer received for the source
// lies on the publisher's own NTP<->RTP line (shifted by the outbound offset)
// and carries the publisher's clock, not the server's.
func assertPublisherMapping(t *testing.T, s *timingSource, reports []timingSR, offset uint32) {
	t.Helper()
	if len(reports) == 0 {
		t.Fatal("viewer received no sender reports")
	}
	tolerance := int32(s.rate() / 500) // 2 ms
	for _, r := range reports {
		got := r.report.RTPTime - offset
		want := s.expectedRTP(r.report.NTPTime)
		if diff := int32(got - want); diff > tolerance || diff < -tolerance {
			t.Fatalf("%s SR mapping off by %d ticks (%.1f ms): ntp=%x rtp=%d want rtp=%d", s.kind.name, diff, float64(diff)*1000/float64(s.rate()), r.report.NTPTime, got, want)
		}
		clock := ntp.ToTime(r.report.NTPTime).Sub(r.at)
		if clock < timingTestClockOffset-5*time.Second || clock > timingTestClockOffset+5*time.Second {
			t.Fatalf("%s SR NTP is not in the publisher clock domain (offset to receipt %v)", s.kind.name, clock)
		}
	}
}

// Every media kind keeps the publisher's capture-time mapping end to end over
// real ICE/DTLS/SRTP: publisher -> production engine/router -> receiver.
func TestSenderReportsPreservePublisherCaptureMapping(t *testing.T) {
	for _, kind := range timingKinds {
		t.Run(kind.name, func(t *testing.T) {
			f := newTimingFixture(t, kind)
			viewer := f.addViewer(t, "viewer")
			id := outputTrackID(kind)
			timingWait(t, "media at viewer", 10*time.Second, func() bool { return viewer.count(id) > 10 })
			timingWait(t, "sender reports at viewer", 10*time.Second, func() bool { return len(viewer.srs(id)) >= 2 })
			// Audio is untranslated and a first video watch keeps offset zero.
			assertPublisherMapping(t, f.sources[0], viewer.srs(id), 0)

			// Network latency is measured against the server's own SR send
			// times, so the one-hour publisher clock offset cannot leak in.
			ssrc := func() uint32 { viewer.mu.Lock(); defer viewer.mu.Unlock(); return viewer.ssrcs[id] }()
			timingWait(t, "RTT from receiver reports", 10*time.Second, func() bool {
				rtt, ok := viewer.server.egress.roundTrip(ssrc)
				return ok && rtt >= 0 && rtt < time.Second
			})
		})
	}
}

// Revoking the source while the receiver's connection persists stops its SRs.
func TestSenderReportsStopAfterSourceRevoked(t *testing.T) {
	for _, kind := range timingKinds {
		t.Run(kind.name, func(t *testing.T) {
			f := newTimingFixture(t, kind)
			viewer := f.addViewer(t, "viewer")
			id := outputTrackID(kind)
			timingWait(t, "sender reports at viewer", 10*time.Second, func() bool { return len(viewer.srs(id)) >= 1 })
			f.allowed.Store(false)
			revokedAt := time.Now()
			time.Sleep(2500 * time.Millisecond)
			for _, r := range viewer.srs(id) {
				if r.at.After(revokedAt.Add(200 * time.Millisecond)) {
					t.Fatalf("SR sent %v after the source was revoked", r.at.Sub(revokedAt))
				}
			}
			if viewer.pc.ConnectionState() != pion.PeerConnectionStateConnected {
				t.Fatal("receiver connection did not persist")
			}
			// Restoring authority resumes reports for the same connection.
			f.allowed.Store(true)
			before := len(viewer.srs(id))
			timingWait(t, "SRs after restore", 10*time.Second, func() bool { return len(viewer.srs(id)) > before })
		})
	}
}

// After a re-watch of the same source the outbound mapping is unchanged, so
// SRs from before and after the gap describe one line.
func TestSenderReportsRewatchKeepsMapping(t *testing.T) {
	for _, kind := range timingKinds[2:] {
		t.Run(kind.name, func(t *testing.T) {
			f := newTimingFixture(t, kind)
			viewer := f.addViewer(t, "viewer")
			id := outputTrackID(kind)
			timingWait(t, "sender reports at viewer", 10*time.Second, func() bool { return len(viewer.srs(id)) >= 1 })
			f.watch(t, "viewer", false, 2)
			unwatched := time.Now()
			time.Sleep(1500 * time.Millisecond)
			for _, r := range viewer.srs(id) {
				if r.at.After(unwatched.Add(200 * time.Millisecond)) {
					t.Fatalf("SR sent %v after the watch was revoked", r.at.Sub(unwatched))
				}
			}
			f.watch(t, "viewer", true, 3)
			rewatched := time.Now()
			timingWait(t, "SRs after re-watch", 10*time.Second, func() bool {
				for _, r := range viewer.srs(id) {
					if r.at.After(rewatched.Add(timingGenerationHoldoff)) {
						return true
					}
				}
				return false
			})
			assertPublisherMapping(t, f.sources[0], viewer.srs(id), 0)
		})
	}
}

// srBlockFactory blocks Sender Report writes of the n-th peer connection
// built by the engine (0-based) until release is closed.
type srBlockFactory struct {
	target  int64
	built   *atomic.Int64
	active  *atomic.Bool
	release chan struct{}
	blocked *atomic.Int64
}

func (f srBlockFactory) NewInterceptor(string) (interceptor.Interceptor, error) {
	index := f.built.Add(1) - 1
	return &srBlockInterceptor{factory: f, mine: index == f.target}, nil
}

type srBlockInterceptor struct {
	interceptor.NoOp
	factory srBlockFactory
	mine    bool
}

func (i *srBlockInterceptor) BindRTCPWriter(writer interceptor.RTCPWriter) interceptor.RTCPWriter {
	if !i.mine {
		return writer
	}
	return interceptor.RTCPWriterFunc(func(packets []rtcp.Packet, attrs interceptor.Attributes) (int, error) {
		for _, packet := range packets {
			if _, ok := packet.(*rtcp.SenderReport); ok && i.factory.active.Load() {
				i.factory.blocked.Add(1)
				<-i.factory.release
				break
			}
		}
		return writer.Write(packets, attrs)
	})
}

func timingGrowth(t *testing.T, viewer *timingViewerState, id string, window time.Duration) int64 {
	t.Helper()
	before := viewer.count(id)
	time.Sleep(window)
	return viewer.count(id) - before
}

// A receiver whose RTCP Sender Report write blocks must not stall video
// forwarding to other viewers, nor its own media.
func TestBlockedSenderReportWriteDoesNotStallOtherViewers(t *testing.T) {
	f := srBlockFactory{target: 1, built: new(atomic.Int64), active: new(atomic.Bool), release: make(chan struct{}), blocked: new(atomic.Int64)}
	kind := timingKinds[2] // H.264 screen
	fixture := newTimingFixtureOptions(t, timingOptions{innermost: []interceptor.Factory{f}}, kind)
	t.Cleanup(func() { close(f.release) })     // before the engine closes its peers
	stalled := fixture.addViewer(t, "stalled") // second peer built by the engine
	healthy := fixture.addViewer(t, "healthy")
	id := outputTrackID(kind)
	timingWait(t, "media at both viewers", 10*time.Second, func() bool { return stalled.count(id) > 10 && healthy.count(id) > 10 })
	timingWait(t, "SRs at both viewers", 10*time.Second, func() bool { return len(stalled.srs(id)) > 0 && len(healthy.srs(id)) > 0 })
	f.active.Store(true)
	timingWait(t, "a blocked SR write", 5*time.Second, func() bool { return f.blocked.Load() > 0 })
	beforeReports := len(healthy.srs(id))
	if got := timingGrowth(t, healthy, id, 1500*time.Millisecond); got < 20 {
		t.Fatalf("healthy viewer received only %d packets while another viewer's SR write was blocked", got)
	}
	if got := timingGrowth(t, stalled, id, 500*time.Millisecond); got < 5 {
		t.Fatalf("viewer with a blocked SR write stopped receiving media (%d packets)", got)
	}
	if len(healthy.srs(id)) <= beforeReports {
		t.Fatal("healthy viewer stopped receiving SRs")
	}
}

// blockingPLIWriter stalls keyframe requests to the publisher once armed.
type blockingPLIWriter struct {
	inner   RTCPWriter
	armed   atomic.Bool
	release chan struct{}
	blocked atomic.Int64
}

func (w *blockingPLIWriter) WriteRTCP(packets []rtcp.Packet) error {
	for _, packet := range packets {
		if _, ok := packet.(*rtcp.PictureLossIndication); ok && w.armed.Load() {
			w.blocked.Add(1)
			<-w.release
		}
	}
	return w.inner.WriteRTCP(packets)
}

// A viewer that needs a keyframe makes the fanout request one from the
// publisher. A slow publisher RTCP write must not stall the fanout to other
// viewers who are already decoding.
func TestBlockedKeyframeRequestDoesNotStallFanout(t *testing.T) {
	kind := timingKinds[2] // H.264 screen
	fixture := newTimingFixtureOptions(t, timingOptions{manualKeys: true}, kind)
	writer := &blockingPLIWriter{inner: fixture.server, release: make(chan struct{})}
	t.Cleanup(func() { close(writer.release) })
	fixture.router.mu.Lock()
	fixture.router.rtcpWriters["pub"] = writer
	fixture.router.mu.Unlock()
	healthy := fixture.addViewer(t, "healthy")
	id := outputTrackID(kind)
	source := fixture.sources[0]
	timingWait(t, "media at healthy viewer", 10*time.Second, func() bool {
		if healthy.count(id) == 0 {
			source.forceKey.Store(true)
		}
		return healthy.count(id) > 10
	})
	// The late viewer never gets a keyframe: its fanout keeps asking for one.
	late := fixture.addViewer(t, "late")
	writer.armed.Store(true)
	// The watch-start request blocks first (on the viewer's startup worker);
	// once the throttle window passes, the fanout itself asks again.
	timingWait(t, "a blocked fanout keyframe request", 5*time.Second, func() bool { return writer.blocked.Load() >= 2 })
	if got := timingGrowth(t, healthy, id, time.Second); got < 15 {
		t.Fatalf("healthy viewer received only %d packets while a keyframe request was blocked", got)
	}
	if late.count(id) != 0 {
		t.Fatal("late viewer decoded without a keyframe")
	}
}
