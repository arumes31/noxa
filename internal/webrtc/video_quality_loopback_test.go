package webrtc

import (
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
	offer, err := peer.pc.CreateOffer(nil)
	if err != nil {
		t.Fatal(err)
	}
	gather(peer.pc, offer)
	if err := receiver.SetRemoteDescription(*peer.pc.LocalDescription()); err != nil {
		t.Fatal(err)
	}
	answer, err := receiver.CreateAnswer(nil)
	if err != nil {
		t.Fatal(err)
	}
	gather(receiver, answer)
	if err := peer.pc.SetRemoteDescription(*receiver.LocalDescription()); err != nil {
		t.Fatal(err)
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
}
