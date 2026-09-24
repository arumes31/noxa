package webrtc

import (
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	pion "github.com/pion/webrtc/v4"
)

func updateEngineBounds(t *testing.T, e *Engine, bounds VideoBounds) error {
	t.Helper()
	return e.SetVideoBoundsForNewPeers(bounds)
}

func runtimeVideoOffer(t *testing.T, e *Engine, id string) (*PeerConnectionWrapper, string) {
	t.Helper()
	p, err := e.NewPeerConnection(id)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []pion.RTPCodecType{pion.RTPCodecTypeAudio, pion.RTPCodecTypeVideo} {
		if _, err := p.pc.AddTransceiverFromKind(kind); err != nil {
			t.Fatal(err)
		}
	}
	sdp, err := p.pc.CreateOffer(nil)
	if err != nil {
		t.Fatal(err)
	}
	return p, sdp.SDP
}

func TestEngineVideoBoundsUpdatePreservesIdentityAndExistingPeers(t *testing.T) {
	for _, startBounded := range []bool{false, true} {
		t.Run(fmt.Sprint(startBounded), func(t *testing.T) {
			initial := VideoBounds{}
			if startBounded {
				initial = VideoBounds{1920, 1080}
			}
			e, err := NewWithVideoBounds(testLogger(), nil, true, NetworkConfig{}, initial)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = e.Close() }()
			first, original := runtimeVideoOffer(t, e, "first")
			fingerprint := e.DTLSFingerprint()
			for i, bounds := range []VideoBounds{{1280, 720}, {640, 360}, {}} {
				if err := updateEngineBounds(t, e, bounds); err != nil {
					t.Fatal(err)
				}
				_, offer := runtimeVideoOffer(t, e, fmt.Sprintf("new-%d", i))
				if !strings.Contains(offer, "VP8/90000") || !strings.Contains(strings.ToLower(offer), "opus/48000/2") || !strings.Contains(strings.ToLower(offer), strings.ToLower(fingerprint)) || e.DTLSFingerprint() != fingerprint {
					t.Fatalf("codec update lost VP8, audio or run identity: %s", offer)
				}
				for _, codec := range []string{"VP9/90000", "H264/90000", "AV1/90000"} {
					if strings.Contains(offer, codec) != (bounds == (VideoBounds{})) {
						t.Fatalf("bounds=%+v codec=%s", bounds, codec)
					}
				}
				if bounds != (VideoBounds{}) && !strings.Contains(offer, fmt.Sprintf("max-fs=%d", ((bounds.Width+15)/16)*((bounds.Height+15)/16))) {
					t.Fatal("new bounds missing from SDP")
				}
				select {
				case <-first.Done():
					t.Fatal("codec update closed existing peer")
				default:
				}
				oldOffer, err := first.pc.CreateOffer(nil)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(oldOffer.SDP, "AV1/90000") != strings.Contains(original, "AV1/90000") {
					t.Fatal("existing negotiated codec set changed in place")
				}
				if e.PeerCount() != i+2 {
					t.Fatal("codec update lost registered peers")
				}
			}
			api := e.api
			for _, invalid := range []VideoBounds{{1, 0}, {-1, 10}, {16384, 720}} {
				if err := updateEngineBounds(t, e, invalid); err == nil || e.api != api {
					t.Fatal("invalid bounds changed engine")
				}
			}
			if err := updateEngineBounds(t, e, VideoBounds{}); err != nil || e.api != api {
				t.Fatal("identical bounds rebuilt API")
			}
			if err := e.Close(); err != nil {
				t.Fatal(err)
			}
			if err := updateEngineBounds(t, e, VideoBounds{}); err == nil {
				t.Fatal("closed engine accepted update")
			}
		})
	}
}

func TestEngineVideoBoundsUpdateRetainsSharedUDPCandidates(t *testing.T) {
	e, err := NewWithNetwork(testLogger(), nil, false, NetworkConfig{UDPAddr: ":0", ExternalIPs: []string{"203.0.113.10"}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = e.Close() }()
	mux := e.udpMux
	addr := mux.GetListenAddresses()[0].String()
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	for i, bounds := range []VideoBounds{{640, 360}, {}} {
		if err := updateEngineBounds(t, e, bounds); err != nil {
			t.Fatal(err)
		}
		peer, err := e.NewPeerConnection(fmt.Sprint(i))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := peer.pc.CreateDataChannel("probe", nil); err != nil {
			t.Fatal(err)
		}
		done := pion.GatheringCompletePromise(peer.pc)
		offer, err := peer.pc.CreateOffer(nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := peer.pc.SetLocalDescription(offer); err != nil {
			t.Fatal(err)
		}
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("candidate gathering timed out")
		}
		if e.udpMux != mux || !strings.Contains(peer.pc.LocalDescription().SDP, "203.0.113.10 "+port+" typ host") {
			t.Fatal("bounds update changed shared ICE transport")
		}
	}
}

func TestEngineVideoBoundsUpdateRetainsDisabledAV1Preference(t *testing.T) {
	e, err := NewWithVideoBounds(testLogger(), nil, false, NetworkConfig{}, VideoBounds{640, 360})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = e.Close() }()
	if err := e.SetVideoBoundsForNewPeers(VideoBounds{}); err != nil {
		t.Fatal(err)
	}
	_, offer := runtimeVideoOffer(t, e, "unbounded")
	if strings.Contains(offer, "AV1/90000") || !strings.Contains(offer, "VP9/90000") || !strings.Contains(offer, "H264/90000") {
		t.Fatal("removing bounds did not restore the operator codec preference")
	}
}
