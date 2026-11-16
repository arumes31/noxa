package webrtc

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/rtcp"
	pion "github.com/pion/webrtc/v4"
)

var nativeScreenKind = timingKind{name: "native-screen", slot: SlotScreen, trackID: "native-screen", codec: timingH264, video: "h264"}

type nativeFixture struct {
	*timingFixture
	voice     *Voice
	browser   *countingRTCPWriter
	publisher *pion.PeerConnection
	source    *timingSource
	plis      atomic.Int64
}

// newNativeFixture establishes "pub"'s browser peer through Voice, then a
// native media peer that sends an H.264 screen track, as the desktop does.
func newNativeFixture(t *testing.T) *nativeFixture {
	t.Helper()
	engine, err := NewWithNetwork(testLogger(), nil, false, NetworkConfig{UDPAddr: "127.0.0.1:0", ExternalIPs: []string{"127.0.0.1"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = engine.Close() })
	engine.iceServers = nil
	router := NewRouter(nil)
	f := &nativeFixture{timingFixture: &timingFixture{engine: engine, router: router, done: make(chan struct{}), viewers: make(map[string]*timingViewerState)}}
	t.Cleanup(func() { close(f.done) })
	f.voice = NewVoice(engine, router, nil)
	router.SetRenegotiateHook(f.renegotiate)
	router.JoinChannel(1, "pub")
	establishVoiceSession(t, f.voice, newClientPC(t), "pub")
	// PLIs for the native track must never reach the browser peer.
	f.browser = &countingRTCPWriter{}
	router.mu.Lock()
	router.rtcpWriters["pub"] = f.browser
	router.mu.Unlock()

	generation, err := router.PublishVideoWithFormats("pub", SlotScreen, 0, true, "source", []string{"vp8", "h264"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.publication = []timingPublication{{slot: SlotScreen, generation: generation, codecs: []string{"vp8", "h264"}}}

	f.publisher = newTimingPublisher(t, nativeScreenKind)
	track, err := pion.NewTrackLocalStaticRTP(timingH264.RTPCodecCapability, nativeScreenKind.trackID, "native")
	if err != nil {
		t.Fatal(err)
	}
	sender, err := f.publisher.AddTrack(track)
	if err != nil {
		t.Fatal(err)
	}
	f.source = &timingSource{kind: nativeScreenKind, track: track, sender: sender, pc: f.publisher, base: 0x10000000, start: time.Now()}
	go func() {
		for {
			packets, _, err := sender.ReadRTCP()
			if err != nil {
				return
			}
			for _, packet := range packets {
				if pli, ok := packet.(*rtcp.PictureLossIndication); ok && pli.MediaSSRC == f.source.ssrc() {
					f.plis.Add(1)
				}
			}
		}
	}()
	offer, err := f.publisher.CreateOffer(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := timingGather(f.publisher, offer); err != nil {
		t.Fatal(err)
	}
	if _, err := f.voice.HandleNativeOffer("pub", f.publisher.LocalDescription().SDP, map[string]string{nativeScreenKind.trackID: SlotScreen}, nil); err != nil {
		t.Fatal(err)
	}
	native := engine.PeerConnection(NativePeerID("pub"))
	if native == nil {
		t.Fatal("native peer not registered")
	}
	select {
	case <-pion.GatheringCompletePromise(native.pc):
	case <-time.After(5 * time.Second):
		t.Fatal("native answer gather timeout")
	}
	if err := f.publisher.SetRemoteDescription(*native.pc.LocalDescription()); err != nil {
		t.Fatal(err)
	}
	timingWait(t, "native transport", 10*time.Second, func() bool {
		return f.publisher.ConnectionState() == pion.PeerConnectionStateConnected
	})
	go f.source.run(t, f.done)
	timingWait(t, "native video codec", 10*time.Second, func() bool {
		router.mu.RLock()
		defer router.mu.RUnlock()
		_, ok := router.videoCodecs[videoSourceRef{"pub", SlotScreen}]
		return ok
	})
	return f
}

func TestNativePeerPublishesScreenAsOwningClient(t *testing.T) {
	f := newNativeFixture(t)
	viewer := f.addViewer(t, "viewer")
	timingWait(t, "screen at the viewer", 10*time.Second, func() bool {
		return viewer.count(slotTrackID("pub", SlotScreen)) > 0
	})
	f.router.mu.RLock()
	ssrc := f.router.videoSources["pub"][SlotScreen][""]
	f.router.mu.RUnlock()
	if ssrc != f.source.ssrc() {
		t.Fatalf("screen source ssrc %d, want the native track's %d", ssrc, f.source.ssrc())
	}
	streams := f.voice.VideoPublications("viewer")
	if len(streams) != 1 || streams[0].PublisherID != "pub" || streams[0].Slot != SlotScreen {
		t.Fatalf("viewer publications = %+v", streams)
	}
	if owner := f.voice.VideoPublications("pub"); len(owner) != 1 || owner[0].UploadActive == nil || !*owner[0].UploadActive {
		t.Fatalf("owner does not see viewer demand: %+v", owner)
	}

	// Keyframe requests for the native source go to the native peer only.
	timingWait(t, "PLI on the native peer", 10*time.Second, func() bool {
		f.router.RequestKeyframe("pub", SlotScreen, "")
		return f.plis.Load() > 0
	})
	if n, _ := f.browser.counts(); n != 0 {
		t.Fatalf("browser peer received %d PLIs for the native track", n)
	}
	f.router.mu.RLock()
	subscribed := len(f.router.pubTracks[NativePeerID("pub")]) != 0 || f.router.pubPeers[NativePeerID("pub")] != nil
	f.router.mu.RUnlock()
	if subscribed {
		t.Fatal("native peer became a subscriber")
	}

	// A browser re-offer that does not mention the screen keeps it flowing.
	f.router.SetTrackSlots("pub", map[string]string{"cam-track": SlotCam})
	before := viewer.count(slotTrackID("pub", SlotScreen))
	timingWait(t, "screen after browser re-declaration", 5*time.Second, func() bool {
		return viewer.count(slotTrackID("pub", SlotScreen)) > before+10
	})

	if err := f.voice.ClosePeer("pub"); err != nil {
		t.Fatal(err)
	}
	if f.voice.engine.PeerConnection(NativePeerID("pub")) != nil {
		t.Fatal("native peer survived the session")
	}
	f.router.mu.RLock()
	slots := f.router.nativeSlots["pub"]
	f.router.mu.RUnlock()
	if slots != nil {
		t.Fatalf("native declarations survived the session: %v", slots)
	}
}

func TestNativePeerClosesWithBrowserPeer(t *testing.T) {
	for name, end := range map[string]func(*nativeFixture, *testing.T){
		"browser rebuild": func(f *nativeFixture, t *testing.T) { establishVoiceSession(t, f.voice, newClientPC(t), "pub") },
		"channel leave":   func(f *nativeFixture, _ *testing.T) { f.voice.LeaveChannel("pub", 1) },
		"channel move":    func(f *nativeFixture, _ *testing.T) { f.voice.JoinChannel("pub", 2) },
		"screen revoked":  func(f *nativeFixture, _ *testing.T) { f.voice.RevokeVideo("pub", SlotScreen) },
		"explicit close":  func(f *nativeFixture, _ *testing.T) { _ = f.voice.CloseNativePeer("pub") },
	} {
		t.Run(name, func(t *testing.T) {
			f := newNativeFixture(t)
			end(f, t)
			if f.voice.engine.PeerConnection(NativePeerID("pub")) != nil {
				t.Fatal("native peer still open")
			}
			timingWait(t, "native source released", 5*time.Second, func() bool {
				f.router.mu.RLock()
				defer f.router.mu.RUnlock()
				return f.router.videoSources["pub"][SlotScreen][""] != f.source.ssrc()
			})
		})
	}
}

func nativeTestOffer(t *testing.T, tracks map[string]pion.RTPCodecCapability) string {
	t.Helper()
	pc := newTimingPublisher(t, nativeScreenKind)
	for id, codec := range tracks {
		track, err := pion.NewTrackLocalStaticRTP(codec, id, "native")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pc.AddTrack(track); err != nil {
			t.Fatal(err)
		}
	}
	offer, err := pc.CreateOffer(nil)
	if err != nil {
		t.Fatal(err)
	}
	return offer.SDP
}

func TestNativeOfferAdmitsOnlyDeclaredScreenTracks(t *testing.T) {
	screen := nativeTestOffer(t, map[string]pion.RTPCodecCapability{"s": timingH264.RTPCodecCapability, "a": timingOpus.RTPCodecCapability})
	cases := map[string]struct {
		sdp   string
		slots map[string]string
		ok    bool
	}{
		"screen and screenaudio": {screen, map[string]string{"s": SlotScreen, "a": SlotScreenAudio}, true},
		"mic slot":               {screen, map[string]string{"s": SlotScreen, "a": SlotMic}, false},
		"cam slot":               {screen, map[string]string{"s": SlotCam, "a": SlotScreenAudio}, false},
		"undeclared track":       {screen, map[string]string{"s": SlotScreen}, false},
		"kind mismatch":          {screen, map[string]string{"s": SlotScreenAudio, "a": SlotScreen}, false},
	}
	for name, c := range cases {
		err := checkNativeOffer(c.sdp, c.slots)
		if c.ok != (err == nil) || err != nil && !errors.Is(err, ErrNativeSlot) {
			t.Errorf("%s: err = %v", name, err)
		}
	}

	e, err := New(testLogger(), nil, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close() })
	v := NewVoice(e, NewRouter(nil), nil)
	if _, err := v.HandleNativeOffer("pub", screen, map[string]string{"s": SlotScreen, "a": SlotScreenAudio}, nil); !errors.Is(err, ErrNoPeer) {
		t.Fatalf("native offer without a browser peer: %v", err)
	}
	if _, err := v.HandleNativeOffer("pub", screen, map[string]string{"s": SlotScreen, "a": SlotMic}, nil); !errors.Is(err, ErrNativeSlot) {
		t.Fatalf("native microphone offer: %v", err)
	}
	if e.PeerCount() != 0 {
		t.Fatal("rejected offers created a peer")
	}
}

// The newest declaration of a slot wins across the two peers; within one
// peer, a live declared track keeps its slot.
func TestNativeAndBrowserScreenClaims(t *testing.T) {
	r := NewRouter(nil)
	native := &PeerConnectionWrapper{}
	key := SlotScreen + slotSep
	r.SetTrackSlots("pub", map[string]string{"browser-screen": SlotScreen})
	browser, ok := r.claimSlotFrom("pub", key, "browser-screen", nil)
	if !ok {
		t.Fatal("browser screen rejected")
	}
	if _, ok := r.claimSlotFrom("pub", key, "native-screen", native); ok {
		t.Fatal("undeclared native track stole the screen")
	}
	r.SetNativeTrackSlots("pub", map[string]string{"native-screen": SlotScreen})
	nativeToken, ok := r.claimSlotFrom("pub", key, "native-screen", native)
	if !ok || r.ownsSlot("pub", key, browser) {
		t.Fatal("native declaration did not replace the browser screen")
	}
	r.mu.RLock()
	writer := r.rtcpWriterLocked("pub", SlotScreen, "")
	r.mu.RUnlock()
	if writer != RTCPWriter(native) {
		t.Fatal("native screen PLIs not routed to the native peer")
	}
	r.SetTrackSlots("pub", map[string]string{"browser-screen-2": SlotScreen})
	if _, ok := r.claimSlotFrom("pub", key, "browser-screen-2", nil); !ok || r.ownsSlot("pub", key, nativeToken) {
		t.Fatal("newer browser declaration did not replace the native screen")
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if len(r.nativeSlots["pub"]) != 0 {
		t.Fatalf("superseded native declaration kept: %v", r.nativeSlots["pub"])
	}
}
