package webrtc

import (
	"fmt"
	"io"
	"net"
	"strings"
	"testing"

	"github.com/pion/interceptor"
	"github.com/pion/rtp"
	pion "github.com/pion/webrtc/v4"
	"go.uber.org/zap"
)

const admittedH264Fmtp = "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42001f"

func profileTestCodec(fmtp string) pion.RTPCodecCapability {
	return pion.RTPCodecCapability{MimeType: pion.MimeTypeH264, ClockRate: 90000, SDPFmtpLine: fmtp}
}

// Production codec/interceptor construction and HandleOffer, with candidate
// gathering disabled. No media, remote candidates or external ICE servers.
func profileTestEngine(t *testing.T) (*Engine, *PeerConnectionWrapper, pion.SettingEngine) {
	t.Helper()
	e, err := New(zap.NewNop(), nil, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close() })
	e.iceServers = nil
	se := e.settingEngine
	se.SetInterfaceFilter(func(string) bool { return false })
	se.SetIPFilter(func(net.IP) bool { return false })
	me, ir, err := newEngineMediaObserved(false, VideoBounds{}, e.egress, e.upstreamFeedback)
	if err != nil {
		t.Fatal(err)
	}
	e.api = pion.NewAPI(pion.WithMediaEngine(me), pion.WithInterceptorRegistry(ir), pion.WithSettingEngine(se))
	peer, err := e.NewPeerConnection("publisher")
	if err != nil {
		t.Fatal(err)
	}
	return e, peer, se
}

func profileTestOffer(t *testing.T, se pion.SettingEngine, codecs []pion.RTPCodecParameters, direction pion.RTPTransceiverDirection) string {
	t.Helper()
	me := &pion.MediaEngine{}
	for _, codec := range codecs {
		if err := me.RegisterCodec(codec, pion.RTPCodecTypeVideo); err != nil {
			t.Fatal(err)
		}
	}
	pc, err := pion.NewAPI(pion.WithMediaEngine(me), pion.WithSettingEngine(se)).NewPeerConnection(pion.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pc.Close() })
	if _, err = pc.AddTransceiverFromKind(pion.RTPCodecTypeVideo, pion.RTPTransceiverInit{Direction: direction}); err != nil {
		t.Fatal(err)
	}
	offer, err := pc.CreateOffer(nil)
	if err != nil {
		t.Fatal(err)
	}
	return offer.SDP
}

func profileTestPair(pt pion.PayloadType, codec pion.RTPCodecCapability) []pion.RTPCodecParameters {
	return []pion.RTPCodecParameters{{RTPCodecCapability: codec, PayloadType: pt}, {RTPCodecCapability: pion.RTPCodecCapability{MimeType: pion.MimeTypeRTX, ClockRate: 90000, SDPFmtpLine: fmt.Sprintf("apt=%d", pt)}, PayloadType: pt + 1}}
}

func TestH264ProfileAdmissionActualEngine(t *testing.T) {
	for _, profile := range []string{"42001f", "42002a", "4d001f", "64001f"} {
		for _, asym := range []int{0, 1} {
			for _, direction := range []pion.RTPTransceiverDirection{pion.RTPTransceiverDirectionSendonly, pion.RTPTransceiverDirectionSendrecv} {
				t.Run(fmt.Sprintf("%s/asym%d/%s", profile, asym, direction), func(t *testing.T) {
					_, peer, se := profileTestEngine(t)
					fmtp := fmt.Sprintf("level-asymmetry-allowed=%d;packetization-mode=1;profile-level-id=%s", asym, profile)
					answer, err := peer.HandleOffer(profileTestOffer(t, se, profileTestPair(120, profileTestCodec(fmtp)), direction))
					if !strings.HasPrefix(profile, "4200") {
						if err == nil {
							t.Fatal("non-Baseline-only offer accepted through Pion partial match")
						}
						return
					}
					if err != nil {
						t.Fatal(err)
					}
					for _, line := range []string{"a=recvonly", "a=fmtp:120 " + fmtp, "a=rtpmap:121 rtx/90000", "a=fmtp:121 apt=120"} {
						if !strings.Contains(answer, line+"\r\n") {
							t.Fatalf("valid format or PT/RTX changed: missing %q", line)
						}
					}
				})
			}
		}
	}
}

func TestH264ProfileAdmissionMixedAndAmbiguousSDP(t *testing.T) {
	for _, tc := range []struct {
		name, bad string
		vp8       bool
	}{
		{"main with Baseline", "packetization-mode=1;profile-level-id=4d001f", false},
		{"high with VP8", "packetization-mode=1;profile-level-id=64001f", true},
		{"duplicate profile", "packetization-mode=1;profile-level-id=64001f;profile-level-id=42001f", false},
		{"duplicate mode", "packetization-mode=0;packetization-mode=1;profile-level-id=42001f", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, peer, se := profileTestEngine(t)
			good := profileTestCodec(admittedH264Fmtp + ";max-fs=8160;sprop-parameter-sets=Z0IAHw==,aM4=")
			if tc.vp8 {
				good = pion.RTPCodecCapability{MimeType: pion.MimeTypeVP8, ClockRate: 90000}
			}
			codecs := append(profileTestPair(120, profileTestCodec(tc.bad)), profileTestPair(122, good)...)
			answer, err := peer.HandleOffer(profileTestOffer(t, se, codecs, pion.RTPTransceiverDirectionSendonly))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(answer, "a=rtpmap:120 ") || strings.Contains(answer, "a=rtpmap:121 ") {
				t.Fatal("forbidden format or its RTX retained")
			}
			if !strings.Contains(answer, "a=rtpmap:122 ") || !strings.Contains(answer, "a=fmtp:123 apt=122\r\n") {
				t.Fatal("valid format or matching RTX removed")
			}
			if !tc.vp8 && !strings.Contains(answer, "a=fmtp:122 "+good.SDPFmtpLine+"\r\n") {
				t.Fatal("optional valid fmtp altered")
			}
		})
	}
}

func TestH264ProfileAdmissionRouterState(t *testing.T) {
	e, _, _ := profileTestEngine(t)
	r := NewRouter(nil)
	attachFakePeer(t, e, r, "sub")
	r.JoinChannel(1, "sub")
	r.JoinChannel(1, "pub")
	generation := testVideoPublication(t, r, "pub", "sub", SlotCam)
	if _, err := r.WatchVideoWithCodecs("sub", "pub", SlotCam, generation, 2, r.VideoWatchSession("sub"), true, []string{"h264", "vp8"}); err != nil {
		t.Fatal(err)
	}
	token, ok := r.claimSlot("pub", SlotCam+slotSep)
	if !ok {
		t.Fatal("claim")
	}
	good := profileTestCodec(admittedH264Fmtp)
	if !r.setVideoCodec("pub", SlotCam, "", token, good) {
		t.Fatal("valid baseline denied")
	}
	output := pubTrackFor(r, "sub", "pub").video[SlotCam]
	renegotiations := 0
	r.SetRenegotiateHook(func(string) { renegotiations++ })
	for _, fmtp := range []string{"packetization-mode=1;profile-level-id=4d001f", "packetization-mode=1;profile-level-id=64001f", "packetization-mode=1;profile-level-id=64001f;PROFILE-LEVEL-ID=42001f"} {
		bad := profileTestCodec(fmtp)
		if r.setVideoCodec("pub", SlotCam, "", token, bad) {
			t.Error("invalid profile admitted")
		}
		if !sameVideoCodec(r.videoCodecs[videoSourceRef{"pub", SlotCam}], good) || pubTrackFor(r, "sub", "pub").video[SlotCam] != output || renegotiations != 0 {
			t.Fatal("rejection changed bindings/state")
		}
		// A stale/corrupt cached entry must not pass the unchanged-codec shortcut,
		// nor be offered to a late receiver declaring the legacy Baseline token.
		r.videoCodecs[videoSourceRef{"pub", SlotCam}] = bad
		if r.setVideoCodec("pub", SlotCam, "", token, bad) {
			t.Error("unchanged invalid profile bypassed admission")
		}
		if r.subscriberVideoCodecLocked("sub", "pub", SlotCam).MimeType != pion.MimeTypeVP8 {
			t.Error("legacy H264 receiver admitted non-Baseline cached codec")
		}
		r.videoCodecs[videoSourceRef{"pub", SlotCam}] = good
	}
}

type profileAdmissionTrack struct {
	fakeVideoTrack
	packets []*rtp.Packet
	formats []string
	index   int
	current string
}

func (t *profileAdmissionTrack) Codec() pion.RTPCodecParameters {
	return pion.RTPCodecParameters{RTPCodecCapability: profileTestCodec(t.current), PayloadType: 102}
}
func (t *profileAdmissionTrack) ReadRTP() (*rtp.Packet, interceptor.Attributes, error) {
	if t.index == len(t.packets) {
		return nil, nil, io.EOF
	}
	p := t.packets[t.index]
	t.current = t.formats[t.index]
	t.index++
	return p, nil, nil
}

func TestH264ProfileAdmissionReadLoopQuarantinesRejectedUnit(t *testing.T) {
	for _, pending := range []bool{false, true} {
		t.Run(fmt.Sprint(pending), func(t *testing.T) {
			r := NewRouter(nil)
			r.JoinChannel(1, "pub")
			r.JoinChannel(1, "recorder")
			r.SetTrackSlots("pub", map[string]string{"desktop": SlotScreen})
			output := &fakeTrackWriter{}
			r.addVideoOutput("recorder", output)
			track := &profileAdmissionTrack{fakeVideoTrack: fakeVideoTrack{ssrc: 7}, current: admittedH264Fmtp}
			if pending {
				track.packets = append(track.packets, h264ForwardPacket(1, 90, false, 0x7c, 0x85, 1))
				track.formats = append(track.formats, admittedH264Fmtp)
			}
			seq := uint16(len(track.packets) + 1)
			track.packets = append(track.packets, h264ForwardPacket(seq, 90, false, 0x41, 1), h264ForwardPacket(seq+1, 90, true, 0x41, 2), h264ForwardPacket(seq+2, 180, true, 24, 0, 2, 0x67, 1, 0, 2, 0x68, 1, 0, 2, 0x65, 1))
			if pending {
				track.packets[1].Payload = []byte{0x7c, 0x05, 1}
				track.packets[2].Payload = []byte{0x7c, 0x45, 2}
			}

			track.formats = append(track.formats, "packetization-mode=1;profile-level-id=64001f", admittedH264Fmtp, admittedH264Fmtp)
			r.ReadVideoLoop("pub", SlotScreen, track)
			if output.count() != 1 || output.packets[0].Timestamp != 180 {
				t.Fatalf("rejected unit escaped or fresh keyframe lost: packets=%v", output.packets)
			}
		})
	}
}

func TestH264ProfileAdmissionClassifier(t *testing.T) {
	for _, tc := range []struct {
		name, fmtp string
		clock      uint32
		want       bool
	}{
		{"baseline", admittedH264Fmtp, 90000, true},
		{"higher level", "packetization-mode=1;profile-level-id=42002A", 90000, true},
		{"optional parameters", " PROFILE-LEVEL-ID = 42001F ; packetization-mode = 1 ;max-fs=8160;sprop-parameter-sets=Z0IAHw==,aM4=", 90000, true},
		{"main", "packetization-mode=1;profile-level-id=4d001f", 90000, false},
		{"high", "packetization-mode=1;profile-level-id=64001f", 90000, false},
		{"constrained baseline is not current contract", "packetization-mode=1;profile-level-id=42e01f", 90000, false},
		{"missing mode", "profile-level-id=42001f", 90000, false},
		{"mode zero", "packetization-mode=0;profile-level-id=42001f", 90000, false},
		{"mode leading zero", "packetization-mode=01;profile-level-id=42001f", 90000, false},
		{"missing profile", "packetization-mode=1", 90000, false},
		{"bad level", "packetization-mode=1;profile-level-id=4200+f", 90000, false},
		{"short profile", "packetization-mode=1;profile-level-id=42001", 90000, false},
		{"long profile", "packetization-mode=1;profile-level-id=42001f00", 90000, false},
		{"duplicate profile same", admittedH264Fmtp + ";PROFILE-LEVEL-ID=42001f", 90000, false},
		{"duplicate mode same", admittedH264Fmtp + ";PACKETIZATION-MODE=1", 90000, false},
		{"valueless critical", admittedH264Fmtp + ";profile-level-id", 90000, false},
		{"wrong clock", admittedH264Fmtp, 8000, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := profileTestCodec(tc.fmtp)
			c.ClockRate = tc.clock
			if got := admittedH264Codec(c); got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
	if allocations := testing.AllocsPerRun(100, func() {
		if !admittedH264Codec(profileTestCodec(admittedH264Fmtp)) {
			panic("valid")
		}
	}); allocations != 0 {
		t.Fatalf("packet classifier allocations: %v", allocations)
	}
}

func TestH264ProfileAdmissionRepairAssociations(t *testing.T) {
	for _, apt := range []string{"apt=120;APT=120", "apt=120;apt=122", "apt=122;apt=120", "apt=999", "apt=+120", "apt=bad", "apt", ""} {
		t.Run(apt, func(t *testing.T) {
			codecs := profileTestPair(120, profileTestCodec(admittedH264Fmtp))
			codecs[1].SDPFmtpLine = apt
			kept := admittedVideoFormats(codecs, false)
			if len(kept) != 1 || kept[0].PayloadType != 120 {
				t.Fatalf("ambiguous/invalid repair admitted: %+v", kept)
			}
		})
	}
	codecs := append(profileTestPair(120, profileTestCodec(admittedH264Fmtp)), profileTestPair(122, pion.RTPCodecCapability{MimeType: pion.MimeTypeVP8, ClockRate: 90000})...)
	kept := admittedVideoFormats(codecs, true)
	if len(kept) != 2 || kept[0].PayloadType != 122 || kept[1].SDPFmtpLine != "apt=122" {
		t.Fatalf("bounds retained unrelated media/repair: %+v", kept)
	}
}

func TestH264ProfileAdmissionActualRepairAndDuplicateLines(t *testing.T) {
	for _, tc := range []struct {
		name, apt     string
		duplicateLine bool
	}{
		{"duplicate same apt", "apt=120;apt=120", false},
		{"duplicate different apt", "apt=96;apt=120", false},
		{"duplicate fmtp line", "apt=120", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, peer, se := profileTestEngine(t)
			codecs := profileTestPair(120, profileTestCodec(admittedH264Fmtp))
			codecs[1].SDPFmtpLine = tc.apt
			offer := profileTestOffer(t, se, codecs, pion.RTPTransceiverDirectionSendonly)
			if tc.duplicateLine {
				offer = strings.Replace(offer, "a=fmtp:120 "+admittedH264Fmtp+"\r\n", "a=fmtp:120 "+admittedH264Fmtp+"\r\na=fmtp:120 packetization-mode=1;profile-level-id=64001f\r\n", 1)
			}
			answer, err := peer.HandleOffer(offer)
			if tc.duplicateLine {
				if err == nil {
					t.Fatal("duplicate fmtp lines accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(answer, "a=rtpmap:121 ") || !strings.Contains(answer, "a=rtpmap:120 H264/90000\r\n") {
				t.Fatal("ambiguous RTX survived or primary lost")
			}
		})
	}
}

func TestH264ProfileAdmissionDuplicatePayloadCannotReintroduceFormat(t *testing.T) {
	codecs := profileTestPair(120, profileTestCodec(admittedH264Fmtp))
	codecs = append(codecs, pion.RTPCodecParameters{RTPCodecCapability: profileTestCodec("packetization-mode=1;profile-level-id=64001f"), PayloadType: 120})
	if kept := admittedVideoFormats(codecs, false); len(kept) != 0 {
		t.Fatalf("ambiguous payload retained: %+v", kept)
	}
}

func TestH264ProfileAdmissionStoppedVideoPreservesVoice(t *testing.T) {
	_, peer, se := profileTestEngine(t)
	me := &pion.MediaEngine{}
	for _, codec := range profileTestPair(120, profileTestCodec(admittedH264Fmtp)) {
		if err := me.RegisterCodec(codec, pion.RTPCodecTypeVideo); err != nil {
			t.Fatal(err)
		}
	}
	if err := me.RegisterCodec(pion.RTPCodecParameters{RTPCodecCapability: pion.RTPCodecCapability{MimeType: pion.MimeTypeOpus, ClockRate: 48000, Channels: 2}, PayloadType: 111}, pion.RTPCodecTypeAudio); err != nil {
		t.Fatal(err)
	}
	pc, err := pion.NewAPI(pion.WithMediaEngine(me), pion.WithSettingEngine(se)).NewPeerConnection(pion.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pc.Close() }()
	video, err := pc.AddTransceiverFromKind(pion.RTPCodecTypeVideo, pion.RTPTransceiverInit{Direction: pion.RTPTransceiverDirectionSendonly})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pc.AddTransceiverFromKind(pion.RTPCodecTypeAudio); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if i == 1 {
			if err = video.Stop(); err != nil {
				t.Fatal(err)
			}
		}
		offer, err := pc.CreateOffer(nil)
		if err != nil {
			t.Fatal(err)
		}
		if err = pc.SetLocalDescription(offer); err != nil {
			t.Fatal(err)
		}
		answer, err := peer.HandleOffer(offer.SDP)
		if err != nil {
			t.Fatalf("voice with stopped=%v: %v", i == 1, err)
		}
		if !strings.Contains(answer, "a=rtpmap:111 opus/48000/2\r\n") {
			t.Fatal("voice codec lost")
		}
		if err = pc.SetRemoteDescription(pion.SessionDescription{Type: pion.SDPTypeAnswer, SDP: answer}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestH264ProfileAdmissionRecoveryAfterRejectedUnit(t *testing.T) {
	for _, replacement := range []string{"delta", "fragmented IDR"} {
		t.Run(replacement, func(t *testing.T) {
			r := NewRouter(nil)
			r.JoinChannel(1, "pub")
			r.JoinChannel(1, "recorder")
			r.SetTrackSlots("pub", map[string]string{"desktop": SlotScreen})
			output := &fakeTrackWriter{}
			r.addVideoOutput("recorder", output)
			feedback := &fakeRTCPWriter{}
			r.rtcpWriters["pub"] = feedback
			track := &profileAdmissionTrack{fakeVideoTrack: fakeVideoTrack{ssrc: 7}, current: admittedH264Fmtp}
			track.packets = []*rtp.Packet{
				h264ForwardPacket(1, 90, true, 24, 0, 2, 0x67, 1, 0, 2, 0x68, 1, 0, 2, 0x65, 1),
				h264ForwardPacket(2, 180, false, 0x41, 2),
				h264ForwardPacket(3, 180, true, 0x41, 3),
				recoveryPaddingPacket(7, 4, 0),
			}
			track.formats = []string{admittedH264Fmtp, "packetization-mode=1;profile-level-id=64001f", admittedH264Fmtp, admittedH264Fmtp}
			wantPLI := 1
			if replacement == "delta" {
				track.packets = append(track.packets, h264ForwardPacket(5, 270, true, 0x41, 5), h264ForwardPacket(6, 360, true, 0x41, 6))
				track.formats = append(track.formats, admittedH264Fmtp, admittedH264Fmtp)
			} else {
				wantPLI = 0
				track.packets = append(track.packets, h264ForwardPacket(5, 270, false, 24, 0, 2, 0x67, 1, 0, 2, 0x68, 1), h264ForwardPacket(6, 270, false, 0x7c, 0x85, 6), h264ForwardPacket(7, 270, true, 0x7c, 0x45, 7))
				track.formats = append(track.formats, admittedH264Fmtp, admittedH264Fmtp, admittedH264Fmtp)
			}
			r.ReadVideoLoop("pub", SlotScreen, track)
			if feedback.pliCount() != wantPLI {
				t.Fatalf("recovery PLI=%d want=%d", feedback.pliCount(), wantPLI)
			}
			for _, packet := range output.packets {
				if packet.Timestamp == 180 {
					t.Fatal("quarantined unit escaped")
				}
			}
			if output.count() < 3 {
				t.Fatal("valid replacement output lost")
			}
		})
	}
}

func TestH264ProfileAdmissionInactiveUnsupportedVideoPreservesVoice(t *testing.T) {
	_, peer, se := profileTestEngine(t)
	me := &pion.MediaEngine{}
	for _, codec := range profileTestPair(120, profileTestCodec("packetization-mode=1;profile-level-id=4d001f")) {
		if err := me.RegisterCodec(codec, pion.RTPCodecTypeVideo); err != nil {
			t.Fatal(err)
		}
	}
	if err := me.RegisterCodec(pion.RTPCodecParameters{RTPCodecCapability: pion.RTPCodecCapability{MimeType: pion.MimeTypeOpus, ClockRate: 48000, Channels: 2}, PayloadType: 111}, pion.RTPCodecTypeAudio); err != nil {
		t.Fatal(err)
	}
	pc, err := pion.NewAPI(pion.WithMediaEngine(me), pion.WithSettingEngine(se)).NewPeerConnection(pion.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pc.Close() }()
	if _, err = pc.AddTransceiverFromKind(pion.RTPCodecTypeVideo, pion.RTPTransceiverInit{Direction: pion.RTPTransceiverDirectionSendonly}); err != nil {
		t.Fatal(err)
	}
	if _, err = pc.AddTransceiverFromKind(pion.RTPCodecTypeAudio); err != nil {
		t.Fatal(err)
	}
	offer, err := pc.CreateOffer(nil)
	if err != nil {
		t.Fatal(err)
	}
	// Pion's constructor does not accept inactive; change only this SDP
	// direction before giving it to the production answerer. No client ICE.
	inactive := strings.Replace(offer.SDP, "a=sendonly\r\n", "a=inactive\r\n", 1)
	if inactive == offer.SDP {
		t.Fatal("fixture has no sendonly video")
	}
	answer, err := peer.HandleOffer(inactive)
	if err != nil {
		t.Fatalf("inactive unsupported video broke voice: %v", err)
	}
	if !strings.Contains(answer, "a=rtpmap:111 opus/48000/2\r\n") {
		t.Fatal("voice codec lost")
	}
	parsed := pion.SessionDescription{Type: pion.SDPTypeAnswer, SDP: answer}
	sdp, err := parsed.Unmarshal()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range sdp.MediaDescriptions {
		if m.MediaName.Media == "video" {
			if _, inactive := m.Attribute("inactive"); !inactive {
				t.Fatal("unsupported video activated")
			}
		}
	}
	// The same MID becoming active must be checked afresh, not exempted
	// because its previous negotiated state was inactive.
	if _, err = peer.HandleOffer(offer.SDP); err == nil || !strings.Contains(err.Error(), "no admitted video codec") {
		t.Fatalf("active unsupported offer was not explicitly rejected: %v", err)
	}
}
