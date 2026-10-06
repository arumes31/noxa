package netproto

import (
	"encoding/json"
	"math"
	"reflect"
	"testing"
)

func TestVoiceTelemetryValidatesTransportAndSenderAllowlist(t *testing.T) {
	t.Parallel()
	report := extendedVoiceDiagnostic(t, `"ssrc":123`)
	if err := json.Unmarshal([]byte(`{"transport":{"protocol":"udp","local_candidate":"relay","remote_candidate":"host","relay_protocol":"tcp"},"senders":[{"ssrc":456,"sample_ms":5000,"packets_sent":250,"packets_per_second":50,"bitrate_bps":32000}]}`), &report); err != nil {
		t.Fatal(err)
	}
	if !report.Valid() {
		t.Fatal("valid additive transport/sender fields rejected")
	}
	if report.Transport == nil || len(report.Senders) != 1 || report.Tracks[0].SSRC == nil {
		t.Fatal("new fields were discarded")
	}
	report.Transport.Protocol = "192.0.2.9"
	if report.Valid() {
		t.Fatal("non-enumerated transport data accepted")
	}
	report.Transport.Protocol = "udp"
	report.Senders = append(report.Senders, report.Senders[0])
	if report.Valid() {
		t.Fatal("duplicate sender SSRC accepted")
	}
}

func extendedVoiceDiagnostic(t *testing.T, extra string) VoiceTelemetry {
	t.Helper()
	var report VoiceTelemetry
	payload := `{"channel_id":7,"connection_state":"connected","output_state":"running","volume":100,"tracks":[{"track_id":"audio",` + extra + `}]}`
	if err := json.Unmarshal([]byte(payload), &report); err != nil {
		t.Fatal(err)
	}
	return report
}

func TestVoiceTelemetryPreservesDiscardedPacketMeasurements(t *testing.T) {
	t.Parallel()
	report := extendedVoiceDiagnostic(t, `"packets_received":1000,"packets_discarded":25,"discard_percent":20`)
	if !report.Valid() {
		t.Fatal("valid discarded packet measurements rejected")
	}
	encoded, err := json.Marshal(report.Tracks[0])
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	if fields["packets_discarded"] != float64(25) || fields["discard_percent"] != float64(20) {
		t.Fatalf("discarded packet measurements lost: %s", encoded)
	}
}

func TestVoiceTelemetryRejectsInvalidDiscardedPacketMeasurements(t *testing.T) {
	t.Parallel()
	for _, extra := range []string{
		`"packets_discarded":-1`, `"packets_discarded":9007199254740992`,
		`"discard_percent":-1`, `"discard_percent":100.1`,
		`"packets_received":10,"packets_discarded":11`,
	} {
		if extendedVoiceDiagnostic(t, extra).Valid() {
			t.Errorf("accepted invalid measurements: %s", extra)
		}
	}
	for _, value := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		report := extendedVoiceDiagnostic(t, `"packets_received":1000`)
		report.Tracks[0].PacketsDiscarded = &value
		if report.Valid() {
			t.Errorf("accepted nonfinite discarded count %v", value)
		}
		report.Tracks[0].PacketsDiscarded = nil
		report.Tracks[0].DiscardPercent = &value
		if report.Valid() {
			t.Errorf("accepted nonfinite discarded percentage %v", value)
		}
	}
}

func TestVoiceTelemetryPreservesOptionalAdaptivePlaybackMeasurements(t *testing.T) {
	t.Parallel()
	report := extendedVoiceDiagnostic(t, `"silent_concealed_samples":123,"silent_concealment_percent":1.2,"non_silent_concealment_percent":0.3,"buffer_minimum_ms":44,"accelerated_samples":1000,"decelerated_samples":99,"acceleration_percent":0.4,"deceleration_percent":0.1`)
	if !report.Valid() {
		t.Fatal("valid optional measurements rejected")
	}
	encoded, err := json.Marshal(report.Tracks[0])
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]float64{
		"silent_concealed_samples": 123, "silent_concealment_percent": 1.2, "non_silent_concealment_percent": 0.3,
		"buffer_minimum_ms": 44, "accelerated_samples": 1000, "decelerated_samples": 99,
		"acceleration_percent": 0.4, "deceleration_percent": 0.1,
	} {
		if fields[key] != want {
			t.Errorf("%s = %v, want %v", key, fields[key], want)
		}
	}
}

func TestVoiceTelemetryRejectsInvalidOptionalAdaptivePlaybackMeasurements(t *testing.T) {
	t.Parallel()
	for _, field := range []string{"silent_concealed_samples", "accelerated_samples", "decelerated_samples",
		"silent_concealment_percent", "non_silent_concealment_percent", "acceleration_percent", "deceleration_percent", "buffer_minimum_ms"} {
		t.Run(field, func(t *testing.T) {
			for _, value := range []string{"-1", "9007199254740992"} {
				if extendedVoiceDiagnostic(t, `"`+field+`":`+value).Valid() {
					t.Errorf("accepted invalid %s=%s", field, value)
				}
			}
		})
	}
}

func TestVoiceTelemetryRejectsNonfiniteOptionalAdaptivePlaybackMeasurements(t *testing.T) {
	t.Parallel()
	for _, field := range []string{"SilentConcealedSamples", "AcceleratedSamples", "DeceleratedSamples",
		"SilentConcealmentPercent", "NonSilentConcealmentPercent", "AccelerationPercent", "DecelerationPercent", "BufferMinimumMS"} {
		t.Run(field, func(t *testing.T) {
			for _, value := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
				report := extendedVoiceDiagnostic(t, `"concealment_percent":0`)
				reflect.ValueOf(&report.Tracks[0]).Elem().FieldByName(field).Set(reflect.ValueOf(&value))
				if report.Valid() {
					t.Errorf("accepted nonfinite %s=%v", field, value)
				}
			}
		})
	}
}

func TestVoiceTelemetrySilentConcealmentCannotExceedTotalConcealment(t *testing.T) {
	t.Parallel()
	if extendedVoiceDiagnostic(t, `"silent_concealed_samples":20,"concealed_samples":10`).Valid() {
		t.Fatal("accepted an inconsistent silence subset")
	}
	if !extendedVoiceDiagnostic(t, `"silent_concealed_samples":10,"concealed_samples":10`).Valid() {
		t.Fatal("rejected an entirely silent concealment count")
	}
}

func TestVoiceTelemetryOptionalPercentageAndDelayBoundaries(t *testing.T) {
	t.Parallel()
	for _, field := range []string{"silent_concealment_percent", "non_silent_concealment_percent", "acceleration_percent", "deceleration_percent"} {
		t.Run(field, func(t *testing.T) {
			if !extendedVoiceDiagnostic(t, `"`+field+`":100`).Valid() {
				t.Fatal("rejected the 100 percent boundary")
			}
			if extendedVoiceDiagnostic(t, `"`+field+`":100.01`).Valid() {
				t.Fatal("accepted a value above 100 percent")
			}
		})
	}
	if !extendedVoiceDiagnostic(t, `"buffer_minimum_ms":60000`).Valid() {
		t.Fatal("rejected the delay boundary")
	}
	if extendedVoiceDiagnostic(t, `"buffer_minimum_ms":60000.01`).Valid() {
		t.Fatal("accepted a delay above the boundary")
	}
}

func TestVoiceTelemetryAcceptsLegacyReportWithoutAdaptivePlaybackMeasurements(t *testing.T) {
	t.Parallel()
	if !extendedVoiceDiagnostic(t, `"concealment_percent":0`).Valid() {
		t.Fatal("legacy report rejected")
	}
}
