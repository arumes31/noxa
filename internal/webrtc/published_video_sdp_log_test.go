package webrtc

import (
	"fmt"
	"strings"
	"testing"

	"github.com/pion/webrtc/v4"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

const publishedVideoOffer = "v=0\r\n" +
	"o=- 1 2 IN IP4 127.0.0.1\r\n" +
	"s=-\r\n" +
	"t=0 0\r\n" +
	"m=audio 9 UDP/TLS/RTP/SAVPF 111\r\n" +
	"c=IN IP4 0.0.0.0\r\n" +
	"a=mid:0\r\n" +
	"a=sendrecv\r\n" +
	"a=rtpmap:111 opus/48000/2\r\n" +
	"m=video 9 UDP/TLS/RTP/SAVPF 102 103\r\n" +
	"c=IN IP4 0.0.0.0\r\n" +
	"a=mid:1\r\n" +
	"a=ice-pwd:secretsecretsecretsecret\r\n" +
	"a=sendonly\r\n" +
	"a=msid:stream screen-track\r\n" +
	"a=rtpmap:102 H264/90000\r\n" +
	"a=fmtp:102 level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f\r\n" +
	"a=rtpmap:103 rtx/90000\r\n" +
	"a=fmtp:103 apt=102\r\n" +
	"m=video 9 UDP/TLS/RTP/SAVPF 96\r\n" +
	"c=IN IP4 0.0.0.0\r\n" +
	"a=mid:2\r\n" +
	"a=recvonly\r\n" +
	"a=rtpmap:96 VP8/90000\r\n"

func publishedVideoAnswer(screen string) string {
	return "v=0\r\n" +
		"o=- 3 4 IN IP4 127.0.0.1\r\n" +
		"s=-\r\n" +
		"t=0 0\r\n" +
		"m=audio 9 UDP/TLS/RTP/SAVPF 111\r\n" +
		"c=IN IP4 0.0.0.0\r\n" +
		"a=mid:0\r\n" +
		"a=sendrecv\r\n" +
		"a=rtpmap:111 opus/48000/2\r\n" +
		screen +
		"m=video 9 UDP/TLS/RTP/SAVPF 96\r\n" +
		"c=IN IP4 0.0.0.0\r\n" +
		"a=mid:2\r\n" +
		"a=sendonly\r\n" +
		"a=rtpmap:96 VP8/90000\r\n"
}

func TestPublishedVideoSectionsAccepted(t *testing.T) {
	answer := publishedVideoAnswer("m=video 9 UDP/TLS/RTP/SAVPF 102\r\n" +
		"c=IN IP4 0.0.0.0\r\n" +
		"a=mid:1\r\n" +
		"a=recvonly\r\n" +
		"a=rtpmap:102 H264/90000\r\n" +
		"a=fmtp:102 packetization-mode=1;profile-level-id=42e01f\r\n")
	sections, err := publishedVideoSections(publishedVideoOffer, answer)
	if err != nil {
		t.Fatal(err)
	}
	if len(sections) != 1 {
		t.Fatalf("got %d sections, want only the published one", len(sections))
	}
	got := sections[0]
	if got.mid != "1" || got.trackID != "screen-track" || got.offerDirection != "sendonly" {
		t.Fatalf("unexpected section identity: %+v", got)
	}
	if got.rejected() {
		t.Fatalf("accepted section reported as rejected: %+v", got)
	}
	want := "102 H264/90000 [level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f]"
	if len(got.offerFormats) != 2 || got.offerFormats[0] != want || got.offerFormats[1] != "103 rtx/90000 [apt=102]" {
		t.Fatalf("offer formats = %q", got.offerFormats)
	}
	if len(got.answerFormats) != 1 || got.answerDirection != "recvonly" || got.answerPort != 9 {
		t.Fatalf("answer = %+v", got)
	}
	for _, format := range got.offerFormats {
		if strings.Contains(format, "secret") {
			t.Fatal("ICE credentials leaked into logged formats")
		}
	}
}

func TestPublishedVideoSectionsRejected(t *testing.T) {
	answer := publishedVideoAnswer("m=video 0 UDP/TLS/RTP/SAVPF 0\r\n" +
		"c=IN IP4 0.0.0.0\r\n" +
		"a=mid:1\r\n" +
		"a=inactive\r\n")
	sections, err := publishedVideoSections(publishedVideoOffer, answer)
	if err != nil {
		t.Fatal(err)
	}
	if len(sections) != 1 || !sections[0].rejected() {
		t.Fatalf("rejected section not detected: %+v", sections)
	}
}

func TestPublishedVideoSectionsInheritSessionDirectionAndSSRCMSID(t *testing.T) {
	offer := "v=0\r\n" +
		"o=- 1 2 IN IP4 127.0.0.1\r\n" +
		"s=-\r\n" +
		"t=0 0\r\n" +
		"a=recvonly\r\n" +
		"m=video 9 UDP/TLS/RTP/SAVPF 96\r\n" +
		"c=IN IP4 0.0.0.0\r\n" +
		"a=mid:0\r\n" +
		"a=rtpmap:96 VP8/90000\r\n" +
		"m=video 9 UDP/TLS/RTP/SAVPF 96\r\n" +
		"c=IN IP4 0.0.0.0\r\n" +
		"a=mid:1\r\n" +
		"a=sendonly\r\n" +
		"a=rtpmap:96 VP8/90000\r\n" +
		"a=ssrc:1234 cname:user\r\n" +
		"a=ssrc:1234 msid:stream ssrc-track\r\n"
	answer := "v=0\r\n" +
		"o=- 3 4 IN IP4 127.0.0.1\r\n" +
		"s=-\r\n" +
		"t=0 0\r\n" +
		"m=video 9 UDP/TLS/RTP/SAVPF 96\r\n" +
		"c=IN IP4 0.0.0.0\r\n" +
		"a=mid:0\r\n" +
		"a=sendonly\r\n" +
		"a=rtpmap:96 VP8/90000\r\n" +
		"m=video 9 UDP/TLS/RTP/SAVPF 96\r\n" +
		"c=IN IP4 0.0.0.0\r\n" +
		"a=mid:1\r\n" +
		"a=recvonly\r\n" +
		"a=rtpmap:96 VP8/90000\r\n"
	sections, err := publishedVideoSections(offer, answer)
	if err != nil {
		t.Fatal(err)
	}
	if len(sections) != 1 {
		t.Fatalf("session-level recvonly section was treated as published: %+v", sections)
	}
	if sections[0].mid != "1" || sections[0].trackID != "ssrc-track" {
		t.Fatalf("unexpected section: %+v", sections[0])
	}
}

// Both HandleOffer paths (new peer and same-fingerprint re-offer) must log the
// published screen section, and no logged field may carry ICE credentials.
func TestHandleOfferLogsPublishedVideoWithoutCredentials(t *testing.T) {
	v := runtimeVoice(t)
	core, logs := observer.New(zap.InfoLevel)
	v.logger = zap.New(core)
	client := newClientPC(t)
	if _, err := client.AddTransceiverFromKind(webrtc.RTPCodecTypeVideo,
		webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionSendonly}); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{"new peer", "re-offer"} {
		before := logs.Len()
		establishVoiceSession(t, v, client, "sharer")
		password := ""
		for line := range strings.Lines(client.LocalDescription().SDP) {
			if value, ok := strings.CutPrefix(strings.TrimSpace(line), "a=ice-pwd:"); ok {
				password = value
			}
		}
		if password == "" {
			t.Fatal("client offer carries no ICE password to check against")
		}
		entries := logs.All()[before:]
		published := 0
		for _, entry := range entries {
			if entry.Message != "webrtc published video negotiated" && entry.Message != "webrtc published video rejected in answer" {
				continue
			}
			published++
			fields := entry.ContextMap()
			if fields["client_id"] != "sharer" || fields["offer_direction"] != "sendonly" {
				t.Fatalf("%s: unexpected fields %v", path, fields)
			}
			if rendered := fmt.Sprint(fields); strings.Contains(rendered, password) {
				t.Fatalf("%s: ICE password leaked into log fields", path)
			}
		}
		if published != 1 {
			t.Fatalf("%s: got %d published video log entries, want 1", path, published)
		}
	}
}
