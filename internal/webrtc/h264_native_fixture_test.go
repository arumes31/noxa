package webrtc

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/rtp/codecs"
)

// Real access units from the desktop native sender (Intel Quick Sync MFT, 1728x1080
// desktop scaled into 640x360, encrypted, as receivers depacketize them), recorded by
// client/internal/nativesender TestNativeCaptureStreamsToReceiver with
// NOXA_NATIVE_TESTDATA. Real encoder SPS is what the inspector must accept.
func TestNativeHardwareEncoderFramesPassEncryptedInspector(t *testing.T) {
	data, err := os.ReadFile("testdata/native-h264-quicksync.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Frames []string `json:"frames"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil || len(fixture.Frames) < 2 {
		t.Fatalf("fixture: %v", err)
	}
	v := h264BoundsInspector{bounds: VideoBounds{640, 360}, encrypted: true}
	var seq uint16
	for i, frameHex := range fixture.Frames {
		frame, err := hex.DecodeString(frameHex)
		if err != nil {
			t.Fatal(err)
		}
		payloads := (&codecs.H264Payloader{}).Payload(1200, frame)
		var unit *h264AccessUnit
		for j, payload := range payloads {
			seq++
			packet := &rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: 102, SequenceNumber: seq, Timestamp: uint32(3000 * (i + 1)), SSRC: 0x4321, Marker: j == len(payloads)-1}, Payload: payload}
			if unit, err = v.push(packet, time.Now()); err != nil {
				t.Fatalf("frame %d rejected by SFU inspector: %v", i, err)
			}
		}
		if unit == nil || unit.keyframe != (i == 0) || unit.width > 640 || unit.height != 360 {
			t.Fatalf("frame %d: %+v", i, unit)
		}
	}
}
