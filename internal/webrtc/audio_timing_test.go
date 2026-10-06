package webrtc

import (
	"bytes"
	"testing"
	"testing/synctest"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/rtp"
)

// The receiver's adaptive buffer can grow without RTP loss. First rule out a
// server pacing backlog or a rewritten Opus clock, including silence and wrap.
func TestAudioPacingPreservesClockAndDoesNotAccumulateDelay(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		i, err := (mediaCCFactory{}).NewInterceptor("audio-clock")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = i.Close() }()
		pacer := i.(*mediaCCInterceptor).pacer
		pacer.SetTargetBitrate(0)
		video := i.BindLocalStream(&interceptor.StreamInfo{ID: "video", SSRC: 11, MimeType: "video/VP8"}, interceptor.RTPWriterFunc(func(*rtp.Header, []byte, interceptor.Attributes) (int, error) {
			t.Error("zero-budget video emitted")
			return 0, nil
		}))
		for range mediaPacerPackets {
			_, _ = video.Write(&rtp.Header{Version: 2, SSRC: 11}, make([]byte, 1188), nil)
		}
		type received struct {
			header  rtp.Header
			payload []byte
			at      time.Time
		}
		packets := make(chan received, 1)
		audio := i.BindLocalStream(&interceptor.StreamInfo{ID: "voice", SSRC: 22, MimeType: "audio/opus", ClockRate: 48000}, interceptor.RTPWriterFunc(func(header *rtp.Header, payload []byte, _ interceptor.Attributes) (int, error) {
			packets <- received{header.Clone(), append([]byte(nil), payload...), time.Now()}
			return header.MarshalSize() + len(payload), nil
		}))
		sequence := uint16(65530)
		timestamp := ^uint32(0) - 1000
		for n := range 251 {
			if n == 125 {
				// DTX omits media rather than RTP sequence numbers. Keep the
				// 48 kHz timeline running across a five-second silence.
				time.Sleep(5 * time.Second)
				timestamp += 5 * 48000
			}
			payload := []byte{0xf8, 0xff, 0xfe}
			header := rtp.Header{Version: 2, SSRC: 22, SequenceNumber: sequence, Timestamp: timestamp}
			enqueued := time.Now()
			if _, err := audio.Write(&header, payload, nil); err != nil {
				t.Fatal(err)
			}
			time.Sleep(20 * time.Millisecond)
			synctest.Wait()
			select {
			case packet := <-packets:
				if packet.header.SequenceNumber != sequence || packet.header.Timestamp != timestamp || !bytes.Equal(packet.payload, payload) {
					t.Fatalf("Opus clock or payload changed: got=%+v want=%+v", packet.header, header)
				}
				if delay := packet.at.Sub(enqueued); delay < 0 || delay > 5*time.Millisecond {
					t.Fatalf("packet %d accumulated %s of server pacing delay", n, delay)
				}
			default:
				t.Fatalf("packet %d was not emitted during its 20 ms frame interval", n)
			}
			sequence++
			timestamp += 960
		}
	})
}
