package server

import (
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/rtp"
	pion "github.com/pion/webrtc/v4"
	"go.uber.org/zap"

	"noxa/internal/authorization"
	"noxa/internal/netproto"
	"noxa/internal/webrtc"
)

// This exercises the real authenticated control handler, production SFU and
// ICE/DTLS/SRTP in both directions. Payloads are synthetic VP8 RTP, not captured
// screen/audio; controlled sender telemetry is distinct from a browser encoder.
func TestStreamDiagnosticsRealSFUControlAndMedia(t *testing.T) {
	engine, err := webrtc.NewWithNetwork(zap.NewNop(), nil, false, webrtc.NetworkConfig{UDPAddr: "127.0.0.1:0", ExternalIPs: []string{"127.0.0.1"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = engine.Close() })
	voice := webrtc.NewVoice(engine, webrtc.NewRouter(zap.NewNop()), zap.NewNop())
	backend := serverRoleFixture()
	backend.policy.Roles[0].Permissions = []authorization.Capability{authorization.Connect, authorization.ViewChannel, authorization.ShareScreen}
	authority, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	env := startTestEnvDeps(t, nil, nil, func(d *Deps) { d.Authority = authority; d.Voice = voice })
	t.Cleanup(env.stop)
	env.state.AddChannel(testChannel(1))
	pub, pubID := dialAuthed(t, env.addr, "user-uid")
	t.Cleanup(func() { _ = pub.Close() })
	sub, subID := dialAuthed(t, env.addr, "admin-uid")
	t.Cleanup(func() { _ = sub.Close() })
	for _, id := range []string{pubID, subID} {
		if err := env.state.MoveClient(id, 1); err != nil {
			t.Fatal(err)
		}
		voice.JoinChannel(id, 1)
		t.Cleanup(func() { _ = voice.ClosePeer(id) })
	}
	newPeer := func() *pion.PeerConnection {
		t.Helper()
		settings := pion.SettingEngine{}
		settings.SetIncludeLoopbackCandidate(true)
		settings.SetIPFilter(func(ip net.IP) bool { return ip.IsLoopback() })
		pc, err := pion.NewAPI(pion.WithSettingEngine(settings)).NewPeerConnection(pion.Configuration{})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = pc.Close() })
		return pc
	}
	publisher, receiver := newPeer(), newPeer()
	track, err := pion.NewTrackLocalStaticRTP(pion.RTPCodecCapability{MimeType: pion.MimeTypeVP8}, "synthetic-screen", "synthetic")
	if err != nil {
		t.Fatal(err)
	}
	sender, err := publisher.AddTrack(track)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := receiver.AddTransceiverFromKind(pion.RTPCodecTypeVideo, pion.RTPTransceiverInit{Direction: pion.RTPTransceiverDirectionRecvonly}); err != nil {
			t.Fatal(err)
		}
	}
	var received atomic.Uint64
	var outputSSRC atomic.Uint32
	receiver.OnTrack(func(track *pion.TrackRemote, _ *pion.RTPReceiver) {
		for {
			packet, _, err := track.ReadRTP()
			if err != nil {
				return
			}
			if track.ID() == pubID+"|screen" {
				outputSSRC.Store(packet.SSRC)
				received.Add(1)
			}
		}
	})
	signalDiagnosticPeer(t, pub, publisher, []netproto.TrackSlot{{TrackID: track.ID(), Slot: "screen"}})
	send(t, pub, netproto.MsgVideoStreamControl, netproto.VideoStreamControl{Action: "publish", Slot: "screen", Active: true, QualityMode: "source"})
	var publication netproto.VideoStreamResult
	if err := netproto.Decode(readOfType(t, pub, netproto.MsgVideoStreamResult), &publication); err != nil || publication.Generation == 0 {
		t.Fatalf("publish: %+v, %v", publication, err)
	}
	if publication.QualityMode != "source" || publication.UploadActive == nil || *publication.UploadActive {
		t.Fatal("new source publication did not report zero-consumer upload state")
	}
	signalDiagnosticPeer(t, sub, receiver, nil)
	send(t, sub, netproto.MsgVideoStreamControl, netproto.VideoStreamControl{Action: "list"})
	var catalog netproto.VideoStreamResult
	if err := netproto.Decode(readOfType(t, sub, netproto.MsgVideoStreamResult), &catalog); err != nil || catalog.Session == 0 {
		t.Fatalf("catalog: %+v, %v", catalog, err)
	}
	if len(catalog.Streams) != 1 || catalog.Streams[0].QualityMode != "source" || catalog.Streams[0].UploadActive != nil {
		t.Fatal("source catalog mode or publisher-only audience scope missing")
	}
	send(t, sub, netproto.MsgVideoStreamControl, netproto.VideoStreamControl{Action: "watch", PublisherID: pubID, Slot: "screen", Generation: publication.Generation, Session: catalog.Session, Revision: 1, Active: true})
	readOfType(t, sub, netproto.MsgVideoStreamResult)
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for sequence := uint16(1); sequence <= 40; sequence++ {
		<-tick.C
		packet := &rtp.Packet{Header: rtp.Header{Version: 2, SequenceNumber: sequence, Timestamp: uint32(sequence) * 1800, Marker: true}, Payload: []byte{0x10, 0x10, 0, 0, 0x9d, 1, 0x2a, 0x80, 2, 0x68, 1}}
		if err := track.WriteRTP(packet); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, "synthetic screen reaching receiver", func() bool { return received.Load() >= 10 })
	encodings := sender.GetParameters().Encodings
	if len(encodings) != 1 || encodings[0].SSRC == 0 {
		t.Fatalf("unexpected publisher encoding count: %d", len(encodings))
	}
	fps, capture, requested, sample := 50.0, 60.0, 60.0, 800.0
	report := voiceTelemetryFixture()
	report.VideoSenders = []netproto.VideoSenderDiagnostics{{SSRC: uint32(encodings[0].SSRC), Slot: "screen", Generation: publication.Generation,
		SampleMS: &sample, RequestedFPS: &requested, CaptureFPS: &capture, EncodedFPS: &fps, SentFPS: &fps, Codec: "video/vp8"}}
	send(t, pub, netproto.MsgVoiceTelemetry, report)
	voiceTelemetryBarrier(t, pub)
	send(t, sub, netproto.MsgVideoStreamControl, netproto.VideoStreamControl{Action: "diagnostics", PublisherID: pubID, Slot: "screen", Generation: publication.Generation, Session: catalog.Session})
	var result netproto.VideoStreamResult
	if err := netproto.Decode(readOfType(t, sub, netproto.MsgVideoStreamResult), &result); err != nil {
		t.Fatal(err)
	}
	details := result.Diagnostics
	if details == nil || details.SenderReport == nil || len(details.SenderReport.Rows) != 1 || len(details.Layers) != 1 || details.Forwarding == nil {
		t.Fatalf("incomplete production stream path: %+v", details)
	}
	row, ingress, output := details.SenderReport.Rows[0], details.Layers[0], details.Forwarding
	if row.CaptureFPS == nil || *row.CaptureFPS != 60 || row.SentFPS == nil || *row.SentFPS != 50 || row.SSRC != ingress.SSRC || row.SSRC != output.SourceSSRC || output.OutputSSRC != outputSSRC.Load() {
		t.Fatalf("sender/source/receiver binding mismatch: sender=%+v ingress=%+v output=%+v", row, ingress, output)
	}
	if ingress.Ingress.Frames < 10 || ingress.Ingress.FPS == nil || *ingress.Ingress.FPS <= 0 || output.Stage.Frames < 10 || output.Stage.FPS == nil || *output.Stage.FPS <= 0 || output.Stage.BitrateBPS == nil || *output.Stage.BitrateBPS <= 0 {
		t.Fatalf("real SFU did not measure media: ingress=%+v output=%+v", ingress, output)
	}
	t.Logf("controlled capture/send %.0f/%.0f fps; measured ingress/forward %.1f/%.1f RTP timestamp fps; receiver packets %d", *row.CaptureFPS, *row.SentFPS, *ingress.Ingress.FPS, *output.Stage.FPS, received.Load())
}

func signalDiagnosticPeer(t *testing.T, conn net.Conn, pc *pion.PeerConnection, tracks []netproto.TrackSlot) {
	t.Helper()
	gathered := pion.GatheringCompletePromise(pc)
	offer, err := pc.CreateOffer(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := pc.SetLocalDescription(offer); err != nil {
		t.Fatal(err)
	}
	select {
	case <-gathered:
	case <-time.After(5 * time.Second):
		t.Fatal("loopback ICE gathering timed out")
	}
	send(t, conn, netproto.MsgWebRTCOffer, netproto.WebRTCOffer{SDP: pc.LocalDescription().SDP, Tracks: tracks})
	var candidates []pion.ICECandidateInit
	answered := false
	for !answered || len(candidates) == 0 {
		frame := readFrame(t, conn)
		switch netproto.MessageType(frame.Type) {
		case netproto.MsgICECandidate:
			var candidate netproto.ICECandidate
			if err := netproto.Decode(frame, &candidate); err != nil {
				t.Fatal(err)
			}
			candidates = append(candidates, pion.ICECandidateInit{Candidate: candidate.Candidate, SDPMid: &candidate.SDPMid, SDPMLineIndex: &candidate.SDPMLineIndex})
		case netproto.MsgWebRTCAnswer:
			var answer netproto.WebRTCAnswer
			if err := netproto.Decode(frame, &answer); err != nil {
				t.Fatal(err)
			}
			if err := pc.SetRemoteDescription(pion.SessionDescription{Type: pion.SDPTypeAnswer, SDP: answer.SDP}); err != nil {
				t.Fatal(err)
			}
			answered = true
		case netproto.MsgError:
			t.Fatalf("loopback signaling rejected: %s", frame.Payload)
		}
	}
	for _, candidate := range candidates {
		if err := pc.AddICECandidate(candidate); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, "loopback media connection", func() bool { return pc.ConnectionState() == pion.PeerConnectionStateConnected })
}
