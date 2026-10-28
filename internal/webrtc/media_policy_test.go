package webrtc

import (
	"strings"
	"testing"

	"github.com/pion/webrtc/v4"
)

// With encrypted channel media the SFU negotiates only Opus and VP8 (plus
// VP8 repair): the codecs whose encrypted frames keep the clear header the
// router's packetization, keyframe and dimension admission rely on.
func TestMediaPolicyE2EENegotiatesOnlyOpusAndVP8(t *testing.T) {
	for _, policy := range []MediaPolicy{{E2EE: true}, {}} {
		m := &webrtc.MediaEngine{}
		if err := registerCodecsWithPolicy(m, true, VideoBounds{}, policy); err != nil {
			t.Fatal(err)
		}
		pc, err := webrtc.NewAPI(webrtc.WithMediaEngine(m)).NewPeerConnection(webrtc.Configuration{})
		if err != nil {
			t.Fatal(err)
		}
		for _, kind := range []webrtc.RTPCodecType{webrtc.RTPCodecTypeAudio, webrtc.RTPCodecTypeVideo} {
			if _, err := pc.AddTransceiverFromKind(kind); err != nil {
				t.Fatal(err)
			}
		}
		offer, err := pc.CreateOffer(nil)
		if err != nil {
			t.Fatal(err)
		}
		_ = pc.Close()
		sdp := strings.ToLower(offer.SDP)
		if !strings.Contains(sdp, "opus/48000") || !strings.Contains(sdp, "vp8/90000") {
			t.Fatalf("policy %+v: missing baseline codecs:\n%s", policy, offer.SDP)
		}
		others := strings.Contains(sdp, "h264/") || strings.Contains(sdp, "h265/") || strings.Contains(sdp, "vp9/") || strings.Contains(sdp, "av1/")
		if others == policy.E2EE {
			t.Fatalf("policy %+v: unexpected codec set:\n%s", policy, offer.SDP)
		}
	}
}
