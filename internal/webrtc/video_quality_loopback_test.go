package webrtc

import (
	"strings"
	"testing"
	"time"

	pion "github.com/pion/webrtc/v4"
)

// Route independent camera/screen layers from two publishers through the
// production egress and real ICE/DTLS/SRTP, including a live layer switch.
func TestStreamVideoQualityLoopbackIndependentShares(t *testing.T) {
	engine, err := NewWithNetwork(testLogger(), []string{"stun:127.0.0.1:9"}, false, NetworkConfig{UDPAddr: "127.0.0.1:0", ExternalIPs: []string{"127.0.0.1"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = engine.Close() })
	engine.iceServers = nil
	peer, err := engine.NewPeerConnection("viewer")
	if err != nil {
		t.Fatal(err)
	}
	router := NewRouter(nil)
	for _, id := range []string{"a", "b", "viewer"} {
		router.JoinChannel(1, id)
	}
	for _, id := range []string{"a", "b"} {
		router.SetTrackSlots(id, map[string]string{"desktop": SlotScreen})
	}
	if err := router.AttachPeer("viewer", peer); err != nil {
		t.Fatal(err)
	}
	router.PrepareSubscriber("viewer")
	type source struct {
		publisher, slot, quality string
		generation               uint64
		base                     uint32
	}
	sources := []source{{"a", SlotScreen, "high", 0, 100}, {"b", SlotScreen, "high", 0, 200}, {"a", SlotCam, "low", 0, 300}, {"b", SlotCam, "mid", 0, 400}}
	session := router.VideoWatchSession("viewer")
	for i := range sources {
		s := &sources[i]
		s.generation = testVideoPublication(t, router, s.publisher, "viewer", s.slot)
		for j, rid := range []string{"f", "h", "q"} {
			registerVideoSource(router, s.publisher, s.slot, rid, s.base+uint32(j))
		}
		if err := router.SetStreamVideoQuality("viewer", s.publisher, s.slot, s.generation, session, s.quality); err != nil {
			t.Fatal(err)
		}
	}
	settings := pion.SettingEngine{}
	settings.SetIncludeLoopbackCandidate(true)
	settings.SetInterfaceFilter(usableICEInterface)
	receiver, err := pion.NewAPI(pion.WithSettingEngine(settings)).NewPeerConnection(pion.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = receiver.Close() })
	type receivedPacket struct {
		id    string
		layer byte
	}
	received := make(chan receivedPacket, 256)
	receiver.OnTrack(func(track *pion.TrackRemote, _ *pion.RTPReceiver) {
		for {
			packet, _, err := track.ReadRTP()
			if err != nil {
				return
			}
			if len(packet.Payload) == 0 {
				continue
			}
			select {
			case received <- receivedPacket{track.ID(), packet.Payload[len(packet.Payload)-1]}:
			case <-t.Context().Done():
				return
			}
		}
	})
	gather := func(pc *pion.PeerConnection, description pion.SessionDescription) {
		t.Helper()
		done := pion.GatheringCompletePromise(pc)
		if err := pc.SetLocalDescription(description); err != nil {
			t.Fatal(err)
		}
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("gather timeout")
		}
	}
	assertUnready := func(s source) {
		t.Helper()
		output := pubTrackFor(router, "viewer", s.publisher).video[s.slot]
		before, epoch := output.videoContinuity, output.watchEpoch
		for sequence := uint16(1); sequence <= 1100; sequence++ {
			packet := continuityPacket(s.base, sequence, uint32(sequence)*3000, sequence, true)
			if router.ForwardVideo(s.publisher, s.slot, "f", packet) != 0 {
				t.Fatal("video admitted before transport and sender binding were ready")
			}
		}
		if output.videoContinuity != before || output.watchEpoch != epoch || output.videoSource.Load() != 0 || len(output.egress.tickets) != 0 {
			t.Fatal("undelivered video committed continuity or retained media tickets")
		}
		output.egress.mu.RLock()
		pacer := output.egress.pacer
		output.egress.mu.RUnlock()
		if pacer != nil {
			state := pacer.diagnosticSnapshot(time.Now())
			if state.QueuedPackets != 0 || state.SentPackets != 0 || pacer.videoStarted.Load() {
				t.Fatalf("unconnected video queued or consumed its probe: %+v", state)
			}
		}
	}
	assertUnready(sources[0]) // Before sender binding, not only before ICE.
	withoutCandidates := func(description pion.SessionDescription) (pion.SessionDescription, []pion.ICECandidateInit) {
		var kept []string
		var candidates []pion.ICECandidateInit
		for _, line := range strings.Split(description.SDP, "\r\n") {
			if strings.HasPrefix(line, "a=candidate:") {
				candidates = append(candidates, pion.ICECandidateInit{Candidate: strings.TrimPrefix(line, "a=")})
			} else if line != "a=end-of-candidates" {
				kept = append(kept, line)
			}
		}
		description.SDP = strings.Join(kept, "\r\n")
		return description, candidates
	}
	offer, err := peer.pc.CreateOffer(nil)
	if err != nil {
		t.Fatal(err)
	}
	gather(peer.pc, offer)
	strippedOffer, serverCandidates := withoutCandidates(*peer.pc.LocalDescription())
	if err := receiver.SetRemoteDescription(strippedOffer); err != nil {
		t.Fatal(err)
	}
	answer, err := receiver.CreateAnswer(nil)
	if err != nil {
		t.Fatal(err)
	}
	gather(receiver, answer)
	strippedAnswer, receiverCandidates := withoutCandidates(*receiver.LocalDescription())
	if err := peer.pc.SetRemoteDescription(strippedAnswer); err != nil {
		t.Fatal(err)
	}
	assertUnready(sources[0]) // Bound senders must still wait for ICE/DTLS.
	connected := make(chan struct{}, 1)
	peer.pc.OnConnectionStateChange(func(state pion.PeerConnectionState) {
		if state == pion.PeerConnectionStateConnected {
			select {
			case connected <- struct{}{}:
			default:
			}
		}
	})
	for _, candidate := range serverCandidates {
		if err := receiver.AddICECandidate(candidate); err != nil {
			t.Fatal(err)
		}
	}
	for _, candidate := range receiverCandidates {
		if err := peer.pc.AddICECandidate(candidate); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case <-connected:
	case <-time.After(5 * time.Second):
		t.Fatal("transport did not connect after candidates were released")
	}
	if router.ForwardVideo("a", SlotScreen, "f", continuityPacket(100, 1101, 3303000, 1101, false)) != 0 {
		t.Fatal("post-connect delta frame reused an unseen keyframe")
	}

	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	deadline := time.NewTimer(8 * time.Second)
	defer deadline.Stop()
	var seq uint16
	for phase := range 2 {
		seen := make(map[string]int)
		if phase == 1 {
			if err := router.SetStreamVideoQuality("viewer", "a", SlotScreen, sources[0].generation, session, "low"); err != nil {
				t.Fatal(err)
			}
			sources[0].quality = "low"
		}
		for len(seen) != len(sources) || seen[slotTrackID("a", SlotScreen)] < 3 || seen[slotTrackID("b", SlotScreen)] < 3 {
			select {
			case got := <-received:
				for _, s := range sources {
					if got.id == slotTrackID(s.publisher, s.slot) {
						want := qualityToRID[s.quality][0]
						if got.layer == want {
							seen[got.id]++
						} else if phase == 0 || s.publisher != "a" || s.slot != SlotScreen || seen[got.id] > 0 {
							t.Fatalf("%s received layer %q, want %q", got.id, got.layer, want)
						}
					}
				}
			case <-tick.C:
				seq++
				for _, s := range sources {
					for i, rid := range []string{"f", "h", "q"} {
						pkt := continuityPacket(s.base+uint32(i), seq, uint32(seq)*3000, seq, true)
						pkt.Version, pkt.PayloadType, pkt.Marker = 2, 96, true
						pkt.Payload = append(pkt.Payload, rid[0])
						router.ForwardVideo(s.publisher, s.slot, rid, pkt)
					}
				}
			case <-deadline.C:
				t.Fatalf("phase %d: streams did not reach receiver: %v (ICE %s)", phase, seen, receiver.ConnectionState())
			}
		}
	}
	checkedOutputs := 0
	for _, publication := range router.VideoOperatorDiagnostics().Publications {
		for _, viewer := range publication.Viewers {
			if viewer.ClientID != "viewer" {
				continue
			}
			checkedOutputs++
			if viewer.Pacer == nil || !viewer.OutputActive || viewer.OutputSSRC == 0 || viewer.Pacer.SentPackets == 0 {
				t.Fatalf("real negotiated output was not linked to its pacer: %+v", viewer)
			}
		}
	}
	if checkedOutputs != len(sources) {
		t.Fatalf("checked %d negotiated outputs, want %d", checkedOutputs, len(sources))
	}

	// A connected voice/video peer can acquire a new screen sender that has not
	// yet been negotiated. Connection state alone must not admit its keyframe.
	router.JoinChannel(1, "c")
	router.SetTrackSlots("c", map[string]string{"desktop": SlotScreen})
	late := source{publisher: "c", slot: SlotScreen, quality: "high", base: 500}
	late.generation = testVideoPublication(t, router, "c", "viewer", SlotScreen)
	registerVideoSource(router, "c", SlotScreen, "f", late.base)
	if peer.pc.ConnectionState() != pion.PeerConnectionStateConnected {
		t.Fatal("existing transport unexpectedly disconnected")
	}
	assertUnready(late)
	offer, err = peer.pc.CreateOffer(nil)
	if err != nil {
		t.Fatal(err)
	}
	gather(peer.pc, offer)
	if err := receiver.SetRemoteDescription(*peer.pc.LocalDescription()); err != nil {
		t.Fatal(err)
	}
	answer, err = receiver.CreateAnswer(nil)
	if err != nil {
		t.Fatal(err)
	}
	gather(receiver, answer)
	if err := peer.pc.SetRemoteDescription(*receiver.LocalDescription()); err != nil {
		t.Fatal(err)
	}
	if router.ForwardVideo("c", SlotScreen, "f", continuityPacket(late.base, 1101, 3303000, 1101, false)) != 0 {
		t.Fatal("newly bound screen accepted a delta without its first keyframe")
	}
	for {
		select {
		case got := <-received:
			if got.id == slotTrackID("c", SlotScreen) {
				return
			}
		case <-tick.C:
			seq++
			packet := continuityPacket(late.base, seq, uint32(seq)*3000, seq, true)
			packet.Version, packet.PayloadType, packet.Marker = 2, 96, true
			if router.ForwardVideo("c", SlotScreen, "f", packet) != 1 {
				t.Fatal("fresh keyframe was not admitted after screen binding")
			}
		case <-deadline.C:
			t.Fatal("fresh keyframe did not reach the newly bound screen receiver")
		}
	}
}
