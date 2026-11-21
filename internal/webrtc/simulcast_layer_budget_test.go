package webrtc

import (
	"bytes"
	"errors"
	"fmt"
	"runtime"
	"runtime/pprof"
	"strings"
	"testing"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/rtp"
	"github.com/pion/sdp/v3"
	pion "github.com/pion/webrtc/v4"
)

func TestCheckRemoteSDPBudgetCapsSimulcastLayers(t *testing.T) {
	var offer strings.Builder
	offer.WriteString("v=0\r\nm=video 9 UDP/TLS/RTP/SAVPF 96\r\n")
	for i := range maxRemoteRIDs {
		fmt.Fprintf(&offer, "a=rid:%d send\r\n", i)
	}
	if err := checkRemoteSDPBudget(offer.String()); err != nil {
		t.Fatalf("description within budget rejected: %v", err)
	}
	// The budget is per description, so a second section does not renew it.
	offer.WriteString("m=video 9 UDP/TLS/RTP/SAVPF 96\r\na=rid:x send\r\n")
	if err := checkRemoteSDPBudget(offer.String()); !errors.Is(err, ErrSDPTooManyRIDs) {
		t.Fatalf("over-budget description err=%v", err)
	}
}

// simulcastPublisher returns a publisher of one video section with layers
// RID layers and its offer, declared by RID only as browsers do.
func simulcastPublisher(t *testing.T, layers int) (*pion.PeerConnection, []*pion.TrackLocalStaticRTP, string) {
	t.Helper()
	media := &pion.MediaEngine{}
	if err := media.RegisterDefaultCodecs(); err != nil {
		t.Fatal(err)
	}
	if err := pion.ConfigureSimulcastExtensionHeaders(media); err != nil {
		t.Fatal(err)
	}
	publisher, err := pion.NewAPI(pion.WithMediaEngine(media), pion.WithInterceptorRegistry(&interceptor.Registry{}), pion.WithSettingEngine(timingLoopbackSettings())).NewPeerConnection(pion.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = publisher.Close() })
	tracks := make([]*pion.TrackLocalStaticRTP, layers)
	var sender *pion.RTPSender
	for i := range tracks {
		if tracks[i], err = pion.NewTrackLocalStaticRTP(pion.RTPCodecCapability{MimeType: pion.MimeTypeVP8}, "video", "stream", pion.WithRTPStreamID(fmt.Sprintf("r%d", i))); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			sender, err = publisher.AddTrack(tracks[0])
		} else {
			err = sender.AddEncoding(tracks[i])
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	offer, err := publisher.CreateOffer(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := timingGather(publisher, offer); err != nil {
		t.Fatal(err)
	}
	var lines []string
	for line := range strings.SplitSeq(publisher.LocalDescription().SDP, "\r\n") {
		if !strings.HasPrefix(line, "a=ssrc") {
			lines = append(lines, line)
		}
	}
	return publisher, tracks, strings.Join(lines, "\r\n")
}

func simulcastServerPeer(t *testing.T) (*Router, *PeerConnectionWrapper) {
	t.Helper()
	engine, err := NewWithNetwork(testLogger(), nil, false, NetworkConfig{AllowedRemoteCIDRs: []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "100.64.0.0/10", "fc00::/7"}, UDPAddr: "127.0.0.1:0", ExternalIPs: []string{"127.0.0.1"}})
	if err != nil {
		t.Fatal(err)
	}
	engine.iceServers = nil
	router := NewRouter(testLogger())
	router.SetVideoHandlers(func(string) bool { return true })
	router.JoinChannel(1, "pub")
	peer, err := engine.NewPeerConnection("pub")
	if err != nil {
		t.Fatal(err)
	}
	if err := router.AttachPeer("pub", peer); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		router.DetachPeer("pub")
		_ = peer.Close()
		_ = engine.Close()
	})
	return router, peer
}

func TestSimulcastOfferOverLayerBudgetRefused(t *testing.T) {
	_, _, offer := simulcastPublisher(t, maxRemoteRIDs+1)
	_, peer := simulcastServerPeer(t)
	if _, err := peer.HandleOffer(offer); !errors.Is(err, ErrSDPTooManyRIDs) {
		t.Fatalf("offer with %d layers: err=%v", maxRemoteRIDs+1, err)
	}
}

func TestSimulcastLayerReadersStayBounded(t *testing.T) {
	const layers = 3
	publisher, tracks, offer := simulcastPublisher(t, layers)
	router, peer := simulcastServerPeer(t)
	answer, err := peer.HandleOffer(offer)
	if err != nil {
		t.Fatalf("three-layer offer refused: %v", err)
	}
	if err := publisher.SetRemoteDescription(pion.SessionDescription{Type: pion.SDPTypeAnswer, SDP: answer}); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(15 * time.Second); publisher.ConnectionState() != pion.PeerConnectionStateConnected; time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("not connected: %s", publisher.ConnectionState())
		}
	}

	var midID, ridID uint8
	for _, ext := range publisher.GetSenders()[0].GetParameters().HeaderExtensions {
		switch ext.URI {
		case sdp.SDESMidURI:
			midID = uint8(ext.ID)
		case sdp.SDESRTPStreamIDURI:
			ridID = uint8(ext.ID)
		}
	}
	mid := publisher.GetTransceivers()[0].Mid()
	for _, track := range tracks {
		packet := &rtp.Packet{Header: rtp.Header{Version: 2, SequenceNumber: 1, Timestamp: 1000, PayloadType: 96}, Payload: []byte{0x10, 0x00, 0x00, 0x00}}
		if err := packet.SetExtension(midID, []byte(mid)); err != nil {
			t.Fatal(err)
		}
		if err := packet.SetExtension(ridID, []byte(track.RID())); err != nil {
			t.Fatal(err)
		}
		if err := track.WriteRTP(packet); err != nil {
			t.Fatal(err)
		}
	}
	claims := func() int {
		router.mu.RLock()
		defer router.mu.RUnlock()
		return len(router.slotClaims["pub"])
	}
	for deadline := time.Now().Add(10 * time.Second); claims() != layers; time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("layers read = %d, want %d", claims(), layers)
		}
	}

	// Further reports of a layer start readers only within the budget.
	before := runtime.NumGoroutine()
	receiver := peer.pc.GetReceivers()[0]
	for range 4 * maxRemoteRIDs {
		peer.handleTrack(receiver.Tracks()[0], receiver)
	}
	time.Sleep(500 * time.Millisecond)
	var stacks bytes.Buffer
	if err := pprof.Lookup("goroutine").WriteTo(&stacks, 2); err != nil {
		t.Fatal(err)
	}
	readers := strings.Count(stacks.String(), "(*Router).browserTrackLoop(")
	grown := runtime.NumGoroutine() - before
	t.Logf("layer track reports=%d readers=%d goroutines=+%d", peer.layerTracks.Load(), readers, grown)
	if readers < layers || readers > maxRemoteRIDs {
		t.Fatalf("layer readers = %d, want %d..%d", readers, layers, maxRemoteRIDs)
	}
	// Two goroutines per started layer track, plus slack for unrelated ones.
	if grown > 2*(maxRemoteRIDs-layers)+8 {
		t.Fatalf("goroutines grew by %d", grown)
	}
	if claims() != layers {
		t.Fatalf("layers read after further reports = %d, want %d", claims(), layers)
	}
}
