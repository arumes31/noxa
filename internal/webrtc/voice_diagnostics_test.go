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
