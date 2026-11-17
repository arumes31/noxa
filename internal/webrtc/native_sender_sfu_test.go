package webrtc

import (
	"bytes"
	"sync"
	"testing"
	"time"

	"github.com/pion/rtcp"
	pion "github.com/pion/webrtc/v4"

	"noxa/internal/framecrypt"
	"noxa/internal/nativesender"
	"noxa/internal/nativesender/sendertest"
	"noxa/internal/netproto"
)

// sfuSignaling hands the desktop native sender's offer to the SFU exactly as
// the server's native-peer handler does and returns the gathered answer.
type sfuSignaling struct {
	t      *testing.T
	voice  *Voice
	engine *Engine
}

func (s sfuSignaling) Offer(offer netproto.WebRTCOffer) (netproto.WebRTCAnswer, error) {
	if offer.Peer != netproto.PeerNative || offer.MediaE2EE != netproto.MediaE2EEVersion || len(offer.Tracks) != 2 || offer.Tracks[0].Slot != SlotScreen || offer.Tracks[1].Slot != SlotScreenAudio {
		s.t.Errorf("native offer metadata: %+v", offer)
	}
	slots := map[string]string{}
	for _, track := range offer.Tracks {
		slots[track.TrackID] = track.Slot
	}
	if _, err := s.voice.HandleNativeOffer("pub", offer.SDP, slots, nil); err != nil {
		return netproto.WebRTCAnswer{}, err
	}
	native := s.engine.PeerConnection(NativePeerID("pub"))
	select {
	case <-pion.GatheringCompletePromise(native.pc):
	case <-time.After(5 * time.Second):
		s.t.Fatal("native answer gather timeout")
	}
	return netproto.WebRTCAnswer{SDP: native.pc.LocalDescription().SDP, Peer: netproto.PeerNative}, nil
}

func (sfuSignaling) Candidate(netproto.ICECandidate) error { return nil }

// TestNativeSenderStreamsThroughEncryptedSFU publishes with the real desktop
// native sender (canonical AU + frame E2EE + pion) into the production
// engine/router under mandatory media E2EE: the encrypted H.264 inspector
// admits the stream, it is attributed to the owning client's screen slot,
// reaches a viewer, and SFU keyframe requests reach the native sender. Its
// encrypted Opus screenaudio track (same capture clock) reaches the viewer
// and decrypts with the publisher's "screenaudio" slot key.
func TestNativeSenderStreamsThroughEncryptedSFU(t *testing.T) {
	engine, err := NewWithMediaPolicy(testLogger(), nil, false, NetworkConfig{UDPAddr: "127.0.0.1:0", ExternalIPs: []string{"127.0.0.1"}}, VideoBounds{}, MediaPolicy{E2EE: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = engine.Close() })
	engine.iceServers = nil
	router := NewRouter(nil)
	f := &timingFixture{engine: engine, router: router, done: make(chan struct{}), viewers: make(map[string]*timingViewerState)}
	t.Cleanup(func() { close(f.done) })
	voice := NewVoice(engine, router, nil)
	router.SetRenegotiateHook(f.renegotiate)
	router.JoinChannel(1, "pub")
	establishVoiceSession(t, voice, newClientPC(t), "pub")
	generation, err := router.PublishVideoWithFormats("pub", SlotScreen, 0, true, "source", []string{"vp8", "h264"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.publication = []timingPublication{{slot: SlotScreen, generation: generation, codecs: []string{"vp8", "h264"}}}

	keys := framecrypt.NewEncryptor()
	if err := keys.SetKey(1, 2, bytes.Repeat([]byte{7}, 32)); err != nil {
		t.Fatal(err)
	}
	if err := keys.Activate(1); err != nil {
		t.Fatal(err)
	}
	audioKey := bytes.Repeat([]byte{9}, 32)
	audioKeys := framecrypt.NewEncryptor()
	if err := audioKeys.SetKey(1, 5, audioKey); err != nil {
		t.Fatal(err)
	}
	if err := audioKeys.Activate(1); err != nil {
		t.Fatal(err)
	}
	var keyMu sync.Mutex
	wantKey := true
	sender, err := nativesender.New(nativesender.Config{Signaling: sfuSignaling{t: t, voice: voice, engine: engine}, Keys: keys, AudioKeys: audioKeys,
		SettingEngine: sendertest.LoopbackSettings(), OnKeyframeRequest: func() { keyMu.Lock(); wantKey = true; keyMu.Unlock() }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sender.Close() })
	timingWait(t, "native transport", 10*time.Second, func() bool { return sender.ConnectionState() == pion.PeerConnectionStateConnected })

	sps, pps := h264TestSPS(1280, 720), h264TestPPSID(0, 0)
	sc := []byte{0, 0, 1}
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		start := time.Now()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			case <-time.After(20 * time.Millisecond):
			}
			_ = sender.WriteAudio(append([]byte{0xfc}, bytes.Repeat([]byte{byte(i)}, 160)...), start.Add(time.Duration(i)*20*time.Millisecond))
		}
	}()
	go func() {
		start := time.Now()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			case <-time.After(33 * time.Millisecond):
			}
			keyMu.Lock()
			key := wantKey || i%60 == 0
			wantKey = false
			keyMu.Unlock()
			payload := bytes.Repeat([]byte{byte(i), 0x5a, 0xa5}, 400)
			au := bytes.Join([][]byte{sc, {0x09, 0x10}, sc, {0x41, 0x9a}, payload}, nil)
			if key { // AUD + in-band parameters + IDR, as Media Foundation writes it
				au = bytes.Join([][]byte{sc, {0x09, 0x10}, sc, sps, sc, pps, sc, {0x65, 0x88}, payload}, nil)
			}
			_ = sender.WriteFrame(au, key, nil, start.Add(time.Duration(i)*33*time.Millisecond))
		}
	}()
	timingWait(t, "native video codec", 10*time.Second, func() bool {
		router.mu.RLock()
		defer router.mu.RUnlock()
		_, ok := router.videoCodecs[videoSourceRef{"pub", SlotScreen}]
		return ok
	})
	router.mu.RLock()
	ssrc := router.videoSources["pub"][SlotScreen][""]
	router.mu.RUnlock()
	if ssrc != sender.Stats().SSRC {
		t.Fatalf("screen source ssrc %d, want the native sender's %d", ssrc, sender.Stats().SSRC)
	}
	viewer := f.addViewer(t, "viewer")
	timingWait(t, "native screen at the viewer", 10*time.Second, func() bool {
		return viewer.count(slotTrackID("pub", SlotScreen)) > 50
	})
	timingWait(t, "native screen audio at the viewer", 10*time.Second, func() bool {
		return viewer.count(slotTrackID("pub", SlotScreenAudio)) > 50
	})
	viewer.mu.Lock()
	payload := viewer.last[slotTrackID("pub", SlotScreenAudio)]
	viewer.mu.Unlock()
	if plain := sendertest.DecryptOpus(t, audioKey, payload); len(plain) != 161 || plain[0] != 0xfc {
		t.Fatalf("screen audio at the viewer decrypted to %d bytes", len(plain))
	}
	// One capture clock: both sender report mappings imply the same origin.
	timingWait(t, "sender reports for screen and screen audio", 15*time.Second, func() bool {
		return len(viewer.srs(slotTrackID("pub", SlotScreen))) > 0 && len(viewer.srs(slotTrackID("pub", SlotScreenAudio))) > 0
	})
	latest := func(slot string) rtcp.SenderReport {
		reports := viewer.srs(slotTrackID("pub", slot))
		return reports[len(reports)-1].report
	}
	video, audio := latest(SlotScreen), latest(SlotScreenAudio)
	// Capture-clock origin difference: NTP distance minus RTP media distance.
	media := time.Duration((float64(video.RTPTime)/90000 - float64(audio.RTPTime)/48000) * float64(time.Second))
	if skew := ntpDuration(video.NTPTime, audio.NTPTime) - media; skew > 100*time.Millisecond || skew < -100*time.Millisecond {
		t.Errorf("screen/screen-audio sender reports disagree by %v", skew)
	} else {
		t.Logf("screen/screen-audio sender report skew %v", skew)
	}
	// transport-cc feedback from the SFU drives the send-side estimate (ladder input).
	initial := sender.Stats().EstimateBPS
	timingWait(t, "bandwidth estimate from transport-cc", 10*time.Second, func() bool {
		est := sender.Stats().EstimateBPS
		return est != initial
	})
	// A server-side close (session end, channel move, revocation) is only seen
	// as the transport ending; the desktop re-offers on Failed/Closed.
	closedAt := time.Now()
	if err := voice.CloseNativePeer("pub"); err != nil {
		t.Fatal(err)
	}
	timingWait(t, "native sender notices the server close", 15*time.Second, func() bool {
		state := sender.ConnectionState()
		return state == pion.PeerConnectionStateFailed || state == pion.PeerConnectionStateClosed
	})
	t.Logf("server close detected after %v", time.Since(closedAt).Round(100*time.Millisecond))
	if st := sender.Stats(); st.FramesDropped != 0 || st.KeyframesSent < 2 {
		t.Fatalf("sender stats: %+v", st)
	}
}
