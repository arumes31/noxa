package webrtc

import (
	"bytes"
	"testing"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
)

// Bind a real Pion output track to a capture writer so this exercises the
// router's subscriber path, including Pion's SSRC/payload-type rewriting.
type extensionTrackContext struct {
	webrtc.TrackLocalContext
	codec  webrtc.RTPCodecParameters
	writer *extensionCapture
}

func (c extensionTrackContext) CodecParameters() []webrtc.RTPCodecParameters {
	return []webrtc.RTPCodecParameters{c.codec}
}
func (c extensionTrackContext) SSRC() webrtc.SSRC                       { return 12345 }
func (c extensionTrackContext) SSRCRetransmission() webrtc.SSRC         { return 0 }
func (c extensionTrackContext) SSRCForwardErrorCorrection() webrtc.SSRC { return 0 }
func (c extensionTrackContext) ID() string                              { return "subscriber" }
func (c extensionTrackContext) WriteStream() webrtc.TrackLocalWriter    { return c.writer }

type extensionCapture struct {
	webrtc.TrackLocalWriter
	header  rtp.Header
	payload []byte
}

func (w *extensionCapture) WriteRTP(header *rtp.Header, payload []byte) (int, error) {
	w.header = header.Clone()
	w.payload = payload
	return len(payload), nil
}

func TestSubscriberRTPDoesNotForwardPublisherExtensions(t *testing.T) {
	for _, slot := range []string{SlotMic, SlotCam} {
		t.Run(slot, func(t *testing.T) {
			e, err := New(testLogger(), nil, false)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = e.Close() })
			r := NewRouter(nil)
			attachFakePeer(t, e, r, "subscriber")
			r.JoinChannel(1, "subscriber")
			r.JoinChannel(1, "publisher")
			if slot == SlotCam {
				testVideoPublication(t, r, "publisher", "subscriber", slot)
			}
			registerVideoSource(r, "publisher", SlotCam, "", 4242)
			output := pubTrackFor(r, "subscriber", "publisher").slots(slotKinds[slot])[slot]
			capture := &extensionCapture{}
			_, err = output.track.Bind(extensionTrackContext{
				codec:  webrtc.RTPCodecParameters{RTPCodecCapability: output.track.Codec(), PayloadType: 120},
				writer: capture,
			})
			if err != nil {
				t.Fatal(err)
			}
			packet := makeAudioPacket(t, 42, 33)
			if slot == SlotCam {
				packet.Payload = boundsKeyPacket(1, 1, 640, 360).Payload
			}
			// Publisher MID and transport-cc IDs belong to its own negotiation.
			if err := packet.SetExtension(3, []byte("0")); err != nil {
				t.Fatal(err)
			}
			if err := packet.SetExtension(5, []byte{0, 7}); err != nil {
				t.Fatal(err)
			}
			original, err := packet.Marshal()
			if err != nil {
				t.Fatal(err)
			}
			var sent int
			if slot == SlotMic {
				sent = r.ForwardRTP("publisher", slot, packet)
			} else {
				sent = r.ForwardVideo("publisher", slot, "", packet)
			}
			if sent != 1 {
				t.Fatalf("sent = %d, want 1", sent)
			}
			if capture.header.Extension || len(capture.header.Extensions) != 0 {
				t.Errorf("subscriber received publisher-specific RTP extensions: %v", capture.header.GetExtensionIDs())
			}
			if capture.header.SSRC != 12345 || capture.header.PayloadType != 120 {
				t.Error("subscriber binding was not applied")
			}
			if !bytes.Equal(capture.payload, packet.Payload) || &capture.payload[0] != &packet.Payload[0] {
				t.Error("media payload must remain shared and unchanged")
			}
			after, err := packet.Marshal()
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(original, after) {
				t.Error("forwarding mutated the publisher packet")
			}
		})
	}
}
