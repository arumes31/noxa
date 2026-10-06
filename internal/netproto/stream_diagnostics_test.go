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

func TestVideoSenderFeedbackDiagnosticsRemainOptionalAndBounded(t *testing.T) {
	base := `{"ssrc":123,"slot":"screen","generation":"42","sample_ms":5000,`
	for _, fields := range []string{
		`"available_outgoing_bitrate_bps":1000000001`, `"available_outgoing_bitrate_bps":-1`,
		`"transport_rtt_ms":60001`, `"remote_rtt_ms":-1`, `"remote_fraction_lost":1.01`,
		`"encoding_max_bitrate_bps":1000000001`, `"encoding_active":"yes"`,
		`"bandwidth_limited_ms":5001`, `"cpu_limited_ms":-1`,
		`"bandwidth_limited_ms":3000,"cpu_limited_ms":3000`,
		`"sample_ms":null,"cpu_limited_ms":1`,
	} {
		var value VideoSenderDiagnostics
		if err := json.Unmarshal([]byte(base+fields+`}`), &value); err == nil && value.Valid() {
			t.Fatalf("invalid sender feedback accepted: %s", fields)
		}
	}
	for _, fields := range []string{
		`"available_outgoing_bitrate_bps":null,"encoding_active":null`,
		`"available_outgoing_bitrate_bps":8000000,"transport_rtt_ms":40,"remote_rtt_ms":80,"remote_fraction_lost":0.02,"encoding_max_bitrate_bps":50000000,"encoding_active":true,"bandwidth_limited_ms":2000,"cpu_limited_ms":0`,
		`"encoding_active":false,"remote_fraction_lost":0,"remote_rtt_ms":0`,
	} {
		var value VideoSenderDiagnostics
		if err := json.Unmarshal([]byte(base+fields+`}`), &value); err != nil || !value.Valid() {
			t.Fatalf("valid optional sender feedback rejected: %s: %v", fields, err)
		}
		raw, err := json.Marshal(value)
		var roundTrip map[string]any
		if err != nil || json.Unmarshal(raw, &roundTrip) != nil {
			t.Fatal("feedback round trip failed", err)
		}
		var input map[string]any
		if err := json.Unmarshal([]byte(base+fields+`}`), &input); err != nil {
			t.Fatal(err)
		}
		for key, expected := range input {
			if roundTrip[key] != expected {
				t.Fatalf("%s changed from %v to %v", key, expected, roundTrip[key])
			}
		}
	}
}
