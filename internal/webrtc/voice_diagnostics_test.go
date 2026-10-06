package webrtc

import (
	"testing"
	"time"

	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	pion "github.com/pion/webrtc/v4"
)

func audioReceiverReportFixture(t *testing.T) (*Router, *pubSlot) {
	t.Helper()
	engine, err := New(testLogger(), nil, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = engine.Close() })
	router := NewRouter(nil)
	attachFakePeer(t, engine, router, "listener")
	router.JoinChannel(1, "listener")
	router.JoinChannel(1, "speaker")
	tracks := pubTrackFor(router, "listener", "speaker")
	if tracks == nil || tracks.audio[SlotMic] == nil {
		t.Fatal("router did not bind the publisher's audio track")
	}
	return router, tracks.audio[SlotMic]
}

func TestVoiceIngressBindsPublicationAndMeasuresArrivalBursts(t *testing.T) {
	router, output := audioReceiverReportFixture(t)
	started := time.Now()
	router.slotClaims["speaker"] = map[string]slotClaim{SlotMic: {token: 41}}
	ingress := router.startAudioIngress("speaker", SlotMic, 41)
	for i, offset := range []time.Duration{0, 20 * time.Millisecond, 520 * time.Millisecond, 521 * time.Millisecond} {
		ingress.observe(&rtp.Packet{Header: rtp.Header{SSRC: 456, Timestamp: uint32(i) * 960}, Payload: []byte{1, 2}}, started.Add(offset))
	}
	paths := router.voiceMediaPaths("listener", started.Add(time.Second))
	if len(paths) != 1 || paths[0].OutputSSRC != uint32(output.sender.GetParameters().Encodings[0].SSRC) {
		t.Fatalf("wrong output binding: %+v", paths)
	}
	input := paths[0].Ingress
	if input == nil || input.SSRC != 456 || input.Publication != "41" || input.Packets != 4 || input.MaxGapMS != 500 || input.BurstPackets != 1 {
		t.Fatalf("wrong ingress observations: %+v", input)
	}
	router.slotClaims["speaker"][SlotMic] = slotClaim{token: 42}
	replacement := router.startAudioIngress("speaker", SlotMic, 42)
	router.startAudioIngress("speaker", SlotMic, 41)
	router.stopAudioIngress("speaker", SlotMic, ingress)
	replacement.observe(&rtp.Packet{Header: rtp.Header{SSRC: 789}}, started.Add(time.Second))
	paths = router.voiceMediaPaths("listener", started.Add(2*time.Second))
	if len(paths) != 1 || paths[0].Ingress.SSRC != 789 || paths[0].Ingress.Packets != 1 {
		t.Fatalf("old publication contaminated new one: %+v", paths)
	}
	router.DetachPeer("speaker")
	if len(router.voiceMediaPaths("listener", time.Now())) != 0 {
		t.Fatal("retired publisher retained path")
	}
}

func TestVoiceIngressJitterPreservesWrappedRTPClock(t *testing.T) {
	for _, tc := range []struct {
		name        string
		first, next uint32
		gap         time.Duration
		wantJitter  float64
	}{
		{"ordinary packet", 1920, 2880, 20 * time.Millisecond, 0},
		{"timestamp wraps forward", 0xfffffe20, 480, 20 * time.Millisecond, 0},
		{"reordered across wrap", 480, 0xfffffe20, 20 * time.Millisecond, 2.5},
		{"intentional silence", 1920, 49920, time.Second, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			diagnostic := &audioIngressDiagnostic{}
			started := time.Unix(1, 0)
			diagnostic.observe(&rtp.Packet{Header: rtp.Header{SSRC: 1, Timestamp: tc.first}, Payload: []byte{1, 2}}, started)
			diagnostic.observe(&rtp.Packet{Header: rtp.Header{SSRC: 1, Timestamp: tc.next}, Payload: []byte{1, 2}}, started.Add(tc.gap))
			got := diagnostic.snapshot(started.Add(tc.gap))
			if got.JitterMS != tc.wantJitter || got.Bytes != 28 || got.Packets != 2 {
				t.Fatalf("ingress = %+v, want jitter %v ms, 28 bytes and two packets", got, tc.wantJitter)
			}
		})
	}
}

func TestVoiceReceiverReportsUseAudioBindingAndRTCPUnits(t *testing.T) {
	router, output := audioReceiverReportFixture(t)
	ssrc := uint32(output.sender.GetParameters().Encodings[0].SSRC)
	if got := router.voiceReceiverReports("listener"); len(got) != 0 {
		t.Fatalf("missing receiver report became healthy zeroes: %+v", got)
	}
	for _, tc := range []struct {
		name     string
		lost     uint32
		wantLost int64
	}{
		{"positive loss", 7, 7},
		{"negative signed 24 bit loss", 0xfffffe, -2},
		{"minimum signed 24 bit loss", 0x800000, -8388608},
		{"maximum signed 24 bit loss", 0x7fffff, 8388607},
		{"new zero replaces old sample", 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			router.recordAudioReceiverReports("listener", output.sender, []rtcp.ReceptionReport{{SSRC: ssrc, TotalLost: tc.lost, FractionLost: 64, Jitter: 480}})
			got := router.voiceReceiverReports("listener")
			if len(got) != 1 {
				t.Fatalf("receiver reports = %+v, want one latest sample", got)
			}
			report := got[0]
			if report.SSRC != ssrc || report.PublisherID != "speaker" || report.Slot != SlotMic {
				t.Fatalf("receiver report attributed to wrong binding: %+v", report)
			}
			if report.PacketsLost != tc.wantLost || report.FractionLost != .25 || report.JitterMS != 10 {
				t.Fatalf("RTCP units/sign conversion incorrect: %+v", report)
			}
			if report.ReceivedAt <= 0 || report.AgeMS < 0 || report.Stale {
				t.Fatalf("fresh receiver report marked stale or missing timestamp: %+v", report)
			}
		})
	}
	router.mu.Lock()
	if output.receiverReport == nil {
		router.mu.Unlock()
		t.Fatal("no receiver report retained for freshness check")
	}
	output.receiverReport.receivedAt = time.Now().Add(-16 * time.Second)
	router.mu.Unlock()
	got := router.voiceReceiverReports("listener")
	if len(got) != 1 || !got[0].Stale || got[0].AgeMS < 15000 {
		t.Fatalf("old receiver report not marked stale: %+v", got)
	}
}

func TestVoiceReceiverReportsRejectUnknownVideoAndRetiredBindings(t *testing.T) {
	router, output := audioReceiverReportFixture(t)
	ssrc := uint32(output.sender.GetParameters().Encodings[0].SSRC)
	video := pubTrackFor(router, "listener", "speaker").video[SlotCam]
	videoSSRC := uint32(video.sender.GetParameters().Encodings[0].SSRC)
	for _, tc := range []struct {
		name, subscriber string
		sender           *pion.RTPSender
		ssrc             uint32
	}{
		{"another subscriber", "other-listener", output.sender, ssrc},
		{"unknown SSRC", "listener", output.sender, ssrc ^ 1},
		{"video feedback", "listener", video.sender, videoSSRC},
		{"audio report on video sender", "listener", video.sender, ssrc},
		{"missing sender", "listener", nil, ssrc},
	} {
		t.Run(tc.name, func(t *testing.T) {
			router.recordAudioReceiverReports(tc.subscriber, tc.sender, []rtcp.ReceptionReport{{SSRC: tc.ssrc, TotalLost: 9}})
			if got := router.voiceReceiverReports("listener"); len(got) != 0 {
				t.Fatalf("unrelated feedback retained: %+v", got)
			}
		})
	}
	report := []rtcp.ReceptionReport{{SSRC: ssrc, TotalLost: 7}}
	router.recordAudioReceiverReports("listener", output.sender, report)
	if got := router.voiceReceiverReports("listener"); len(got) != 1 {
		t.Fatalf("valid report missing before cleanup: %+v", got)
	}
	router.LeaveChannel(1, "speaker")
	if got := router.voiceReceiverReports("listener"); len(got) != 0 {
		t.Fatalf("removed publisher retained feedback: %+v", got)
	}
	router.JoinChannel(1, "speaker")
	replacement := pubTrackFor(router, "listener", "speaker").audio[SlotMic]
	if replacement.sender == output.sender {
		t.Fatal("fixture reused retired sender")
	}
	router.recordAudioReceiverReports("listener", output.sender, report)
	if got := router.voiceReceiverReports("listener"); len(got) != 0 {
		t.Fatalf("retired sender contaminated replacement binding: %+v", got)
	}
	report[0].SSRC = uint32(replacement.sender.GetParameters().Encodings[0].SSRC)
	router.recordAudioReceiverReports("listener", replacement.sender, report)
	router.DetachPeer("listener")
	if got := router.voiceReceiverReports("listener"); len(got) != 0 {
		t.Fatalf("detached subscriber retained feedback: %+v", got)
	}
}

func TestVoiceReceiverReportsCollectedFromActualRTCP(t *testing.T) {
	engine, err := NewWithNetwork(testLogger(), nil, false, NetworkConfig{UDPAddr: "127.0.0.1:0", ExternalIPs: []string{"127.0.0.1"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = engine.Close() })
	engine.iceServers = nil
	peer, err := engine.NewPeerConnection("listener")
	if err != nil {
		t.Fatal(err)
	}
	router := NewRouter(nil)
	if err := router.AttachPeer("listener", peer); err != nil {
		t.Fatal(err)
	}
	router.JoinChannel(1, "listener")
	router.JoinChannel(1, "speaker")
	router.PrepareSubscriber("listener")
	settings := pion.SettingEngine{}
	settings.SetIncludeLoopbackCandidate(true)
	settings.SetInterfaceFilter(usableICEInterface)
	receiver, err := pion.NewAPI(pion.WithSettingEngine(settings)).NewPeerConnection(pion.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = receiver.Close() })
	received := make(chan uint32, 1)
	receiver.OnTrack(func(track *pion.TrackRemote, _ *pion.RTPReceiver) {
		if _, _, err := track.ReadRTP(); err == nil {
			received <- uint32(track.SSRC())
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
			t.Fatal("ICE gathering timed out")
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
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	var seq uint16
	var ssrc uint32
	for {
		select {
		case ssrc = <-received:
		case <-ticker.C:
			if ssrc != 0 {
				for _, report := range router.voiceReceiverReports("listener") {
					if report.SSRC == ssrc && report.PacketsLost == 7 && report.FractionLost == .25 && report.JitterMS == 10 {
						return
					}
				}
				if err := receiver.WriteRTCP([]rtcp.Packet{&rtcp.ReceiverReport{SSRC: 9876, Reports: []rtcp.ReceptionReport{{SSRC: ssrc, TotalLost: 7, FractionLost: 64, Jitter: 480}}}}); err != nil {
					t.Fatal(err)
				}
			}
			seq++
			router.ForwardRTP("speaker", SlotMic, &rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: 111, SequenceNumber: seq, Timestamp: uint32(seq) * 960}, Payload: []byte{0xf8, 0xff, 0xfe}})
		case <-deadline.C:
			t.Fatalf("receiver report was not collected over actual RTCP: sender=%s receiver=%s audioSSRC=%d reports=%+v", peer.pc.ConnectionState(), receiver.ConnectionState(), ssrc, router.voiceReceiverReports("listener"))
		}
	}
}
