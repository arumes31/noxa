package webrtc

import (
	"fmt"
	"strings"
	"testing"

	pion "github.com/pion/webrtc/v4"
)

func hevcTestCodec(level uint8) pion.RTPCodecCapability {
	return pion.RTPCodecCapability{MimeType: pion.MimeTypeH265, ClockRate: 90000, SDPFmtpLine: fmt.Sprintf("profile-id=1;tier-flag=0;level-id=%d;tx-mode=SRST", level)}
}

func TestHEVCProfileActualEnginePreservesOfferedLevelAndRTX(t *testing.T) {
	for _, level := range []uint8{30, 60, 63, 90, 93, 120, 123, 150, 153, 156, 180, 183, 186} {
		for _, direction := range []pion.RTPTransceiverDirection{pion.RTPTransceiverDirectionSendonly, pion.RTPTransceiverDirectionSendrecv} {
			t.Run(fmt.Sprintf("level%d/%s", level, direction), func(t *testing.T) {
				_, peer, se := profileTestEngine(t)
				codec := hevcTestCodec(level)
				answer, err := peer.HandleOffer(profileTestOffer(t, se, profileTestPair(120, codec), direction))
				if err != nil {
					t.Fatal(err)
				}
				for _, line := range []string{"a=recvonly", "a=rtpmap:120 H265/90000", "a=fmtp:120 " + codec.SDPFmtpLine, "a=rtpmap:121 rtx/90000", "a=fmtp:121 apt=120"} {
					if !strings.Contains(answer, line+"\r\n") {
						t.Fatalf("answer changed offered level, direction or RTX: missing %q", line)
					}
				}
				if level != 186 && strings.Contains(answer, "level-id=186") {
					t.Fatal("server maximum silently raised negotiated source level")
				}
			})
		}
	}
}

func TestHEVCProfileActualEngineRejectsUnsupportedAndKeepsFallback(t *testing.T) {
	for _, bad := range []string{
		"profile-id=2;tier-flag=0;level-id=123;tx-mode=SRST",
		"profile-id=1;tier-flag=1;level-id=123;tx-mode=SRST",
		"profile-id=1;tier-flag=0;level-id=123;tx-mode=MRST",
		"profile-id=1;tier-flag=0;level-id=123;sprop-max-don-diff=1",
		"profile-id=1;tier-flag=0;level-id=123;LEVEL-ID=186",
		"profile-id=1;tier-flag=0;level-id=0",
		"profile-id=1;tier-flag=0",
	} {
		for _, fallback := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/fallback%v", bad, fallback), func(t *testing.T) {
				_, peer, se := profileTestEngine(t)
				invalid := hevcTestCodec(123)
				invalid.SDPFmtpLine = bad
				codecs := profileTestPair(120, invalid)
				if fallback {
					codecs = append(codecs, profileTestPair(122, profileTestCodec(admittedH264Fmtp))...)
				}
				answer, err := peer.HandleOffer(profileTestOffer(t, se, codecs, pion.RTPTransceiverDirectionSendonly))
				if !fallback {
					if err == nil {
						t.Fatal("unsupported HEVC-only offer accepted")
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(answer, "a=rtpmap:120 ") || strings.Contains(answer, "a=rtpmap:121 ") {
					t.Fatal("unsupported HEVC/RTX retained with fallback")
				}
				if !strings.Contains(answer, "a=fmtp:122 "+admittedH264Fmtp+"\r\n") || !strings.Contains(answer, "a=fmtp:123 apt=122\r\n") {
					t.Fatal("valid Baseline/RTX fallback lost")
				}
			})
		}
	}
}

func TestHEVCProfileRouterPinsPublisherAndCurrentReceiverLevel(t *testing.T) {
	e, _, _ := profileTestEngine(t)
	r := hevcSelectionRouter()
	attachFakePeer(t, e, r, "sub")
	codecs := []string{"h265", "h264", "vp8"}
	generation, err := r.PublishVideoWithFormats("pub", SlotScreen, 0, true, "source", codecs, 123)
	if err != nil {
		t.Fatal(err)
	}
	session := r.VideoWatchSession("sub")
	if _, err := r.WatchVideoWithFormats("sub", "pub", SlotScreen, generation, 1, session, true, codecs, 123); err != nil {
		t.Fatal(err)
	}
	token, ok := r.claimSlot("pub", SlotScreen+slotSep)
	if !ok {
		t.Fatal("claim")
	}
	good := hevcTestCodec(123)
	if !r.setVideoCodec("pub", SlotScreen, "", token, good) {
		t.Fatal("declared HEVC format rejected")
	}
	if !sameVideoCodec(r.subscriberVideoCodecLocked("sub", "pub", SlotScreen), good) {
		t.Fatal("compatible receiver did not obtain exact format")
	}
	ref := videoSourceRef{"pub", SlotScreen}
	for _, level := range []uint8{120, 150} {
		wrong := hevcTestCodec(level)
		if r.setVideoCodec("pub", SlotScreen, "", token, wrong) {
			t.Fatal("source changed immutable publication level")
		}
		if !sameVideoCodec(r.videoCodecs[ref], good) {
			t.Fatal("rejected format changed source binding")
		}
		r.videoCodecs[ref] = wrong
		if r.setVideoCodec("pub", SlotScreen, "", token, wrong) {
			t.Fatal("unchanged cache bypassed declared level")
		}
		r.videoCodecs[ref] = good
	}
	if _, err := r.WatchVideoWithFormats("sub", "pub", SlotScreen, generation, 2, session, true, codecs, 120); err != nil {
		t.Fatal(err)
	}
	if r.subscriberVideoCodecLocked("sub", "pub", SlotScreen).MimeType != pion.MimeTypeVP8 {
		t.Fatal("downgraded receiver still offered higher-level active source")
	}
	if _, err := r.WatchVideoWithFormats("sub", "pub", SlotScreen, generation, 3, session, true, codecs, 186); err != nil {
		t.Fatal(err)
	}
	if !sameVideoCodec(r.subscriberVideoCodecLocked("sub", "pub", SlotScreen), good) {
		t.Fatal("higher ceiling did not restore exact source format")
	}
	if _, err := r.WatchVideoWithFormats("sub", "pub", SlotScreen, generation, 4, session, false, codecs, 186); err != nil {
		t.Fatal(err)
	}
	if r.subscriberVideoCodecLocked("sub", "pub", SlotScreen).MimeType != pion.MimeTypeVP8 {
		t.Fatal("inactive receiver retained HEVC capability binding")
	}
	if _, err := r.PublishVideo("pub", SlotScreen, generation, false); err != nil {
		t.Fatal(err)
	}
	if r.setVideoCodec("pub", SlotScreen, "", token, good) {
		t.Fatal("removed publication still admitted HEVC packet codec")
	}
}

func TestHEVCProfileClassifierAllocationAndOptionalDefaults(t *testing.T) {
	codec := hevcTestCodec(123)
	codec.SDPFmtpLine = "level-id=123;sprop-max-don-diff=0;interop-constraints=000000000000"
	if level, ok := admittedH265Level(codec); !ok || level != 123 {
		t.Fatal("valid Main/tier0/SRST defaults rejected")
	}
	if allocations := testing.AllocsPerRun(1000, func() { admittedH265Level(codec) }); allocations != 0 {
		t.Fatalf("packet classifier allocates %v times", allocations)
	}
	for _, bad := range []string{"level-id=123;level-id=123", "level-id=0123", "level-id=256", "level-id=123;profile-id=1;PROFILE-ID=1", "level-id=123;interop-constraints=invalid", "level-id=123;sprop-max-don-diff=1"} {
		codec.SDPFmtpLine = bad
		if _, ok := admittedH265Level(codec); ok {
			t.Fatalf("ambiguous/unsupported fmtp accepted: %s", bad)
		}
	}
}
