package webrtc

import (
	"bytes"
	"image"
	"image/jpeg"
	"testing"
	"testing/synctest"

	"github.com/pion/interceptor"
	"github.com/pion/rtp"
	pion "github.com/pion/webrtc/v4"
)

func TestVideoPreviewDoesNotBlockFanoutBehindSlowReceiver(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := NewRouter(nil)
		for _, id := range []string{"pub", "slow", "healthy"} {
			r.JoinChannel(1, id)
		}
		generation := testVideoPublication(t, r, "pub", "slow", SlotScreen)
		testVideoPublication(t, r, "pub", "healthy", SlotScreen)
		registerVideoSource(r, "pub", SlotScreen, "", 9001)
		track, err := pion.NewTrackLocalStaticRTP(pion.RTPCodecCapability{MimeType: pion.MimeTypeVP8, ClockRate: 90000}, "pub|screen", "pub")
		if err != nil {
			t.Fatal(err)
		}
		capture := &extensionCapture{}
		if _, err := track.Bind(extensionTrackContext{codec: pion.RTPCodecParameters{RTPCodecCapability: track.Codec(), PayloadType: 96}, writer: capture}); err != nil {
			t.Fatal(err)
		}
		r.pubTracks["healthy"] = map[string]*pubTrack{"pub": {video: map[string]*pubSlot{SlotScreen: {track: track}}}}
		r.mu.RLock()
		delivery := r.mediaOutputLocked("pub", "slow", SlotScreen, false, nil).delivery
		r.mu.RUnlock()
		stream := &mediaEgressStream{active: true, registry: &mediaEgressRegistry{}}
		packet, ok := stream.prepare(&rtp.Packet{Header: rtp.Header{Version: 2}, Payload: []byte{1}}, mediaTicket{router: r, delivery: delivery, videoRevision: r.videoPolicySnapshot().revision})
		if !ok {
			t.Fatal("prepare failed")
		}
		entered, release, written := make(chan struct{}), make(chan struct{}), make(chan struct{})
		go func() {
			defer close(written)
			_, _, _ = stream.writeResult(&packet.Header, packet.Payload, nil, interceptor.RTPWriterFunc(func(_ *rtp.Header, payload []byte, _ interceptor.Attributes) (int, error) {
				close(entered)
				<-release
				return len(payload), nil
			}))
		}()
		<-entered
		defer func() { close(release); <-written; synctest.Wait() }()
		var data bytes.Buffer
		if err := jpeg.Encode(&data, image.NewRGBA(image.Rect(0, 0, 16, 16)), nil); err != nil {
			t.Fatal(err)
		}
		preview := make(chan error, 1)
		go func() { preview <- r.SetVideoPreview("pub", SlotScreen, generation, data.Bytes()) }()
		// The upload either completes or is parked behind the slow final write.
		// No real sleeps or scheduling-dependent writer-start assumptions.
		synctest.Wait()
		forwarded := make(chan int, 1)
		go func() {
			p := boundsKeyPacket(1, 1, 640, 360)
			p.SSRC = 9001
			forwarded <- r.ForwardVideo("pub", SlotScreen, "", p)
		}()
		synctest.Wait()
		select {
		case err := <-preview:
			if err != nil {
				t.Errorf("preview update: %v", err)
			}
		default:
			t.Error("preview cache update waited for the slow receiver socket")
		}
		select {
		case sent := <-forwarded:
			if sent != 1 || len(capture.payload) == 0 {
				t.Errorf("healthy fanout: sent=%d bytes=%d", sent, len(capture.payload))
			}
		default:
			t.Error("preview metadata writer blocked publisher fanout to healthy receiver")
		}
	})
}
