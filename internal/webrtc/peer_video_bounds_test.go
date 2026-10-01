package webrtc

import (
	"strings"
	"testing"

	pion "github.com/pion/webrtc/v4"
)

func TestPeerVideoBoundsAdvertisesLocalPreferences(t *testing.T) {
	for _, tc := range []struct{ name, fmtp string }{
		{"omitted", ""},
		{"remote receiver bounds", "max-fs=60;max-fr=15"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, err := NewWithVideoBounds(testLogger(), nil, false, NetworkConfig{}, VideoBounds{640, 360})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = e.Close() }()
			media := &pion.MediaEngine{}
			if err := media.RegisterCodec(pion.RTPCodecParameters{
				RTPCodecCapability: pion.RTPCodecCapability{MimeType: pion.MimeTypeVP8, ClockRate: 90000,
					SDPFmtpLine: tc.fmtp, RTCPFeedback: []pion.RTCPFeedback{{Type: "nack"}, {Type: "nack", Parameter: "pli"}}},
				PayloadType: 120,
			}, pion.RTPCodecTypeVideo); err != nil {
				t.Fatal(err)
			}
			client, err := pion.NewAPI(pion.WithMediaEngine(media)).NewPeerConnection(pion.Configuration{})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = client.Close() }()
			for range 2 {
				if _, err := client.AddTransceiverFromKind(pion.RTPCodecTypeVideo); err != nil {
					t.Fatal(err)
				}
			}
			peer, err := e.NewPeerConnection("publisher")
			if err != nil {
				t.Fatal(err)
			}
			offer, err := client.CreateOffer(nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := client.SetLocalDescription(offer); err != nil {
				t.Fatal(err)
			}
			answer, err := peer.HandleOffer(offer.SDP)
			if err != nil {
				t.Fatal(err)
			}
			assertBoundedVideoSDP(t, answer)
			if err := client.SetRemoteDescription(pion.SessionDescription{Type: pion.SDPTypeAnswer, SDP: answer}); err != nil {
				t.Fatal(err)
			}
			// A later engine update cannot alter this existing peer's negotiated
			// receiver settings. Only a new peer picks up the replacement bounds.
			if err := e.SetVideoBoundsForNewPeers(VideoBounds{1280, 720}); err != nil {
				t.Fatal(err)
			}
			renegotiation, err := peer.CreateOffer()
			if err != nil {
				t.Fatal(err)
			}
			assertBoundedVideoSDP(t, renegotiation)
			if err := client.SetRemoteDescription(pion.SessionDescription{Type: pion.SDPTypeOffer, SDP: renegotiation}); err != nil {
				t.Fatal(err)
			}
			remoteAnswer, err := client.CreateAnswer(nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := client.SetLocalDescription(remoteAnswer); err != nil {
				t.Fatal(err)
			}
			if err := peer.HandleAnswer(remoteAnswer.SDP); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func assertBoundedVideoSDP(t *testing.T, description string) {
	t.Helper()
	for _, line := range []string{"a=rtpmap:120 VP8/90000", "a=fmtp:120 max-fs=920", "a=rtcp-fb:120 nack", "a=rtcp-fb:120 nack pli"} {
		if strings.Count(description, line+"\r\n") != 2 {
			t.Fatalf("missing local bounds, payload mapping or feedback %q: %s", line, description)
		}
	}
}

func TestPeerVideoBoundsDoesNotMutateSharedCodecParameters(t *testing.T) {
	e, err := New(testLogger(), nil, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = e.Close() }()
	peer, err := e.NewPeerConnection("publisher")
	if err != nil {
		t.Fatal(err)
	}
	transceiver, err := peer.pc.AddTransceiverFromKind(pion.RTPCodecTypeVideo)
	if err != nil {
		t.Fatal(err)
	}
	// Locally added transceivers have no explicit codec preferences. Pion's
	// returned slice then aliases its MediaEngine, which active tracks read.
	parameters := transceiver.Receiver().GetParameters()
	before := parameters.Codecs[0].SDPFmtpLine
	peer.videoBounds = VideoBounds{640, 360}
	if err := peer.setVideoBoundsCodecPreferences(); err != nil {
		t.Fatal(err)
	}
	if parameters.Codecs[0].SDPFmtpLine != before {
		t.Fatal("local preference update mutated shared codec parameters")
	}
	if !strings.Contains(transceiver.Receiver().GetParameters().Codecs[0].SDPFmtpLine, "max-fs=920") {
		t.Fatal("local preference was not installed")
	}
}
