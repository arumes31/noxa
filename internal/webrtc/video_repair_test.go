package webrtc

import (
	"strings"
	"testing"

	pion "github.com/pion/webrtc/v4"
)

func TestEngineNegotiatesVideoPacketRepair(t *testing.T) {
	for _, bounded := range []bool{false, true} {
		t.Run(map[bool]string{false: "unbounded", true: "bounded VP8"}[bounded], func(t *testing.T) {
			bounds := VideoBounds{}
			if bounded {
				bounds = VideoBounds{640, 360}
			}
			e, err := NewWithVideoBounds(testLogger(), nil, false, NetworkConfig{}, bounds)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = e.Close() })
			media := &pion.MediaEngine{}
			for _, codec := range []pion.RTPCodecParameters{
				{RTPCodecCapability: pion.RTPCodecCapability{MimeType: pion.MimeTypeVP8, ClockRate: 90000}, PayloadType: 120},
				{RTPCodecCapability: pion.RTPCodecCapability{MimeType: pion.MimeTypeRTX, ClockRate: 90000, SDPFmtpLine: "apt=120"}, PayloadType: 121},
				{RTPCodecCapability: pion.RTPCodecCapability{MimeType: pion.MimeTypeH264, ClockRate: 90000, SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42001f"}, PayloadType: 122},
				{RTPCodecCapability: pion.RTPCodecCapability{MimeType: pion.MimeTypeRTX, ClockRate: 90000, SDPFmtpLine: "apt=122"}, PayloadType: 123},
			} {
				if err := media.RegisterCodec(codec, pion.RTPCodecTypeVideo); err != nil {
					t.Fatal(err)
				}
			}
			client, err := pion.NewAPI(pion.WithMediaEngine(media)).NewPeerConnection(pion.Configuration{})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = client.Close() })
			if _, err := client.AddTransceiverFromKind(pion.RTPCodecTypeVideo); err != nil {
				t.Fatal(err)
			}
			offer, err := client.CreateOffer(nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := client.SetLocalDescription(offer); err != nil {
				t.Fatal(err)
			}
			peer, err := e.NewPeerConnection("publisher")
			if err != nil {
				t.Fatal(err)
			}
			answer, err := peer.HandleOffer(offer.SDP)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(answer, "a=rtpmap:121 rtx/90000\r\n") || !strings.Contains(answer, "a=fmtp:121 apt=120\r\n") {
				t.Fatal("VP8 lost its negotiated repair payload")
			}
			if bounded {
				if strings.Contains(answer, "H264") || strings.Contains(answer, "apt=122") {
					t.Fatal("repair widened bounded codec admission")
				}
			} else if !strings.Contains(answer, "a=fmtp:123 apt=122\r\n") {
				t.Fatal("H264 lost its negotiated repair payload")
			}
			if err := client.SetRemoteDescription(pion.SessionDescription{Type: pion.SDPTypeAnswer, SDP: answer}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
