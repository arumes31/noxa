package webrtc

import (
	"bytes"
	"testing"
	"time"

	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	pion "github.com/pion/webrtc/v4"

	"noxa/internal/mediacc/ntp"
)

// A replacement source must not restart the recipient's established SRTP
// replay/sequence space. Payloads are opaque here; the browser integration
// separately runs the real authenticated frame transforms.
func TestAudioSourceReplacementOverSRTP(t *testing.T) {
	verifyNoLeaks(t)
	f := newVideoLoopbackFixture(t, SlotMic, MediaPolicy{E2EE: true})
	arrivals := make(chan *rtp.Packet, 64)
	reports := make(chan *rtcp.SenderReport, 16)
	f.receiver.OnTrack(func(track *pion.TrackRemote, receiver *pion.RTPReceiver) {
		if track.Kind() != pion.RTPCodecTypeAudio {
			return
		}
		go func() {
			for {
				packets, _, err := receiver.ReadRTCP()
				if err != nil {
					return
				}
				for _, p := range packets {
					if sr, ok := p.(*rtcp.SenderReport); ok {
						select {
						case reports <- sr:
						default:
						}
					}
				}
			}
		}()
		for {
			p, _, err := track.ReadRTP()
			if err != nil {
				return
			}
			select {
			case arrivals <- p:
			default:
			}
		}
	})
	negotiateStartupTestPeer(t, f.peer, f.receiver)
	timingWait(t, "audio recipient transport", 5*time.Second, func() bool {
		return f.receiver.ConnectionState() == pion.PeerConnectionStateConnected && f.peer.pc.ConnectionState() == pion.PeerConnectionStateConnected
	})
	f.output = pubTrackFor(f.router, "viewer", "pub").audio[SlotMic]
	f.router.SetTrackSlots("pub", map[string]string{"old-mic": SlotMic})
	if _, ok := f.router.claimSlot("pub", SlotMic, "old-mic"); !ok {
		t.Fatal("initial source claim")
	}
	origin := time.Now()
	capture := ntp.ToNTP(origin.Add(timingTestClockOffset))
	oldOwner, newOwner := new(timingOwner), new(timingOwner)
	f.router.sourceTiming.bind("pub", 100, oldOwner)
	f.router.sourceTiming.observe("pub", oldOwner, &rtcp.SenderReport{SSRC: 100, NTPTime: capture, RTPTime: 1_000_000}, 48000, origin)
	send := func(ssrc uint32, seq uint16, ts uint32, body []byte) {
		t.Helper()
		if f.router.ForwardRTP("pub", SlotMic, &rtp.Packet{Header: rtp.Header{Version: 2, SSRC: ssrc, PayloadType: 111, SequenceNumber: seq, Timestamp: ts}, Payload: body}) != 1 {
			t.Fatal("audio packet not admitted")
		}
	}
	receive := func(body []byte) *rtp.Packet {
		t.Helper()
		deadline := time.After(2 * time.Second)
		for {
			select {
			case p := <-arrivals:
				if bytes.Equal(p.Payload, body) {
					return p
				}
			case <-deadline:
				t.Fatalf("fresh audio payload %x did not reach SRTP recipient", body)
				return nil
			}
		}
	}
	send(100, 30000, 1_000_000, []byte{0xf8, 1})
	receive([]byte{0xf8, 1})
	send(100, 30001, 1_000_960, []byte{0xf8, 2})
	previous := receive([]byte{0xf8, 2})
	f.router.SetTrackSlots("pub", map[string]string{"new-mic": SlotMic})
	if _, ok := f.router.claimSlot("pub", SlotMic, "new-mic"); !ok {
		t.Fatal("replacement source claim")
	}
	nextCapture := ntpAdd(capture, 40*time.Millisecond)
	f.router.sourceTiming.bind("pub", 200, newOwner)
	f.router.sourceTiming.observe("pub", newOwner, &rtcp.SenderReport{SSRC: 200, NTPTime: nextCapture, RTPTime: 10000}, 48000, time.Now())
	// Restart behind the old sequence by more than the SRTP replay window,
	// but less than half the sequence space (not an RTP wrap).
	send(200, 29000, 10000, []byte{0xf8, 3})
	fresh := receive([]byte{0xf8, 3})
	if fresh.SSRC != previous.SSRC || fresh.SequenceNumber != previous.SequenceNumber+1 || int32(fresh.Timestamp-previous.Timestamp) <= 0 {
		t.Fatalf("discontinuous replacement: old=%+v new=%+v", previous.Header, fresh.Header)
	}
	offset := fresh.Timestamp - 10000
	if offset != 991920 {
		t.Fatalf("publisher capture mapping not preserved: offset %d", offset)
	}
	// Newer then reordered packets retain their source offsets; duplicates
	// remain duplicates and are suppressed by the recipient's SRTP protection.
	send(200, 29002, 11920, []byte{0xf8, 5})
	later := receive([]byte{0xf8, 5})
	send(200, 29001, 10960, []byte{0xf8, 4})
	reordered := receive([]byte{0xf8, 4})
	if later.SequenceNumber != fresh.SequenceNumber+2 || reordered.SequenceNumber != fresh.SequenceNumber+1 || reordered.Timestamp != fresh.Timestamp+960 {
		t.Fatal("source reordering was renumbered")
	}
	send(200, 29001, 10960, []byte{0xf8, 4})
	select {
	case p := <-arrivals:
		t.Fatalf("duplicate passed SRTP replay protection: %+v", p.Header)
	case <-time.After(60 * time.Millisecond):
	}
	deadline := time.After(2 * time.Second)
	for {
		select {
		case sr := <-reports:
			if sr.PacketCount < 3 {
				continue
			}
			expected := uint32(10000) + offset + uint32(rtpTicks(ntpDuration(sr.NTPTime, nextCapture), 48000))
			if sr.SSRC != fresh.SSRC || int32(sr.RTPTime-expected) > 2 || int32(sr.RTPTime-expected) < -2 {
				t.Fatalf("replacement SR lost publisher clock: %+v expected RTP %d", sr, expected)
			}
			return
		case <-deadline:
			t.Fatal("no replacement sender report over RTCP")
		}
	}
}
