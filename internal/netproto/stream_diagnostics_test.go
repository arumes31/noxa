package netproto

import (
	"encoding/json"
	"math"
	"testing"
)

func TestVideoSenderTelemetryIsBoundedAndAllowlisted(t *testing.T) {
	valid := VideoSenderDiagnostics{SSRC: 123, RID: "f", Slot: "screen", Generation: 9007199254740993, Codec: "video/vp8", EncoderImplementation: "libvpx", QualityReason: "cpu"}
	if !valid.Valid() {
		t.Fatal("valid video sender rejected")
	}
	for _, change := range []func(*VideoSenderDiagnostics){
		func(r *VideoSenderDiagnostics) { r.SSRC = 0 },
		func(r *VideoSenderDiagnostics) { r.Generation = 0 },
		func(r *VideoSenderDiagnostics) { r.RID = "unbounded" },
		func(r *VideoSenderDiagnostics) { r.Slot = "mic" },
		func(r *VideoSenderDiagnostics) { r.Codec = "192.0.2.1" },
		func(r *VideoSenderDiagnostics) { r.EncoderImplementation = "private device name" },
		func(r *VideoSenderDiagnostics) { r.QualityReason = "arbitrary text" },
		func(r *VideoSenderDiagnostics) { v := math.NaN(); r.EncodedFPS = &v },
		func(r *VideoSenderDiagnostics) { v := 241.0; r.CaptureFPS = &v },
		func(r *VideoSenderDiagnostics) { v := -1.0; r.BytesSent = &v },
		func(r *VideoSenderDiagnostics) { v := 1000000001.0; r.TargetBitrateBPS = &v },
		func(r *VideoSenderDiagnostics) { v := 101.0; r.RetransmitPercent = &v },
		func(r *VideoSenderDiagnostics) { a, b := 11.0, 10.0; r.KeyFrames, r.FramesEncoded = &a, &b },
		func(r *VideoSenderDiagnostics) { a, b := 11.0, 10.0; r.RetransmittedBytes, r.BytesSent = &a, &b },
		func(r *VideoSenderDiagnostics) { a, b := 11.0, 10.0; r.RetransmittedPackets, r.PacketsSent = &a, &b },
	} {
		r := valid
		change(&r)
		if r.Valid() {
			t.Fatalf("invalid row accepted: %+v", r)
		}
	}
	report := extendedVoiceDiagnostic(t, `"ssrc":123`)
	report.VideoSenders = []VideoSenderDiagnostics{valid}
	if !report.Valid() {
		t.Fatal("valid additive telemetry rejected")
	}
	report.VideoSenders = append(report.VideoSenders, valid)
	if report.Valid() {
		t.Fatal("duplicate video sender accepted")
	}
	report.VideoSenders = make([]VideoSenderDiagnostics, 9)
	if report.Valid() {
		t.Fatal("oversized video sender report accepted")
	}
	raw, err := json.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil || decoded["generation"] != "9007199254740993" || decoded["sent_fps"] != nil {
		t.Fatalf("generation or missing measurement changed: %s, %v", raw, err)
	}
}
