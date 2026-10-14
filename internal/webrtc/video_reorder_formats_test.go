package webrtc

import (
	"testing"
	"time"

	"github.com/pion/rtp"
	pion "github.com/pion/webrtc/v4"
)

func TestVideoReorderCannotRelabelQueuedUnsupportedProfile(t *testing.T) {
	for _, mime := range []string{pion.MimeTypeH264, pion.MimeTypeH265} {
		t.Run(mime, func(t *testing.T) {
			r, out, _ := hevcRouterTap(t)
			good, bad := hevcRouterCodec(hevcRouterFmtp), hevcRouterCodec("profile-id=2;tier-flag=0;level-id=93")
			key := func(seq uint16, ts uint32) *rtp.Packet { return hevcPacket(seq, ts, true, hevcKey(19)...) }
			if mime == pion.MimeTypeH264 {
				good = profileTestCodec(admittedH264Fmtp)
				bad = profileTestCodec("packetization-mode=1;profile-level-id=64001f")
				key = func(seq uint16, ts uint32) *rtp.Packet {
					return h264ForwardPacket(seq, ts, true, 24, 0, 2, 0x67, 1, 0, 2, 0x68, 1, 0, 2, 0x65, 1)
				}
			}
			// seq3's unsupported profile is captured while it waits for seq2. A later
			// good-profile repair must not relabel seq3 with current track metadata.
			track := &hevcRouterTrack{fakeVideoTrack: fakeVideoTrack{ssrc: 7}, current: good, packets: []*rtp.Packet{key(1, 90), key(3, 270), key(2, 180), key(4, 360)}, codecs: []pion.RTPCodecCapability{good, bad, good, good}}
			r.ReadVideoLoop("pub", SlotScreen, track)
			if out.count() != 3 {
				t.Fatalf("queued forbidden-profile unit was relabeled/forwarded: packets=%d", out.count())
			}
			for i, want := range []uint16{1, 2, 4} {
				if out.packets[i].SequenceNumber != want {
					t.Fatalf("output sequence %d got %d want %d", i, out.packets[i].SequenceNumber, want)
				}
			}
		})
	}
}

func TestVideoReorderOwnsDelayedCodecCapability(t *testing.T) {
	var q videoReorder
	now := time.Now()
	good := hevcRouterCodec(hevcRouterFmtp)
	bad := hevcRouterCodec("profile-id=2;tier-flag=0;level-id=93")
	bad.RTCPFeedback = []pion.RTCPFeedback{{Type: "nack", Parameter: "pli"}}
	var sequences []uint16
	var formats []pion.RTPCodecCapability
	emit := func(p *rtp.Packet, c pion.RTPCodecCapability) {
		sequences = append(sequences, p.SequenceNumber)
		formats = append(formats, c)
	}
	q.pushFormats(hevcPacket(1, 90, true, hevcKey(19)...), good, now, emit)
	q.pushFormats(hevcPacket(3, 270, true, hevcKey(19)...), bad, now, emit)
	bad.SDPFmtpLine = good.SDPFmtpLine
	bad.ClockRate = 8000
	bad.RTCPFeedback[0].Type = "changed"
	q.pushFormats(hevcPacket(2, 180, true, hevcKey(19)...), good, now, emit)
	if len(formats) != 3 || sequences[2] != 3 || formats[2].SDPFmtpLine != "profile-id=2;tier-flag=0;level-id=93" || formats[2].ClockRate != 90000 || formats[2].RTCPFeedback[0].Type != "nack" {
		t.Fatalf("queued format aliased later metadata: %+v", formats)
	}
	if q.bytes != 0 || len(q.pending) != 0 {
		t.Fatal("drained queue retained packet storage")
	}
}
