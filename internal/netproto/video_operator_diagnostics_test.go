package netproto

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestVideoTerminalDropAvailabilityIsExplicit(t *testing.T) {
	legacy, err := json.Marshal(VideoPacerDiagnostics{})
	if err != nil || strings.Contains(string(legacy), "terminal_drops") {
		t.Fatalf("unavailable counters must remain absent: %s %v", legacy, err)
	}
	reported, err := json.Marshal(VideoPacerDiagnostics{TerminalDrops: &VideoTerminalDropDiagnostics{}})
	if err != nil || !strings.Contains(string(reported), `"terminal_drops":`) || !strings.Contains(string(reported), `"expired_ticket":0`) {
		t.Fatalf("reported zero must not mean unavailable: %s %v", reported, err)
	}
	var decoded VideoPacerDiagnostics
	if err := json.Unmarshal(legacy, &decoded); err != nil || decoded.TerminalDrops != nil {
		t.Fatalf("legacy diagnostics no longer decode as unavailable: %+v %v", decoded, err)
	}
}

func TestVideoUpstreamFeedbackAvailabilityIsExplicit(t *testing.T) {
	legacy, err := json.Marshal(VideoOperatorPublication{})
	if err != nil || strings.Contains(string(legacy), "upstream_feedback") {
		t.Fatalf("unavailable feedback must be absent: %s %v", legacy, err)
	}
	var decoded VideoOperatorPublication
	if err = json.Unmarshal(legacy, &decoded); err != nil || decoded.UpstreamFeedback != nil {
		t.Fatal("legacy publication did not preserve unknown feedback")
	}
	reported, err := json.Marshal(VideoOperatorPublication{UpstreamFeedback: &VideoUpstreamFeedbackDiagnostics{}})
	if err != nil || !strings.Contains(string(reported), `"upstream_feedback":`) || !strings.Contains(string(reported), `"attempts":0`) || !strings.Contains(string(reported), `"in_flight_oldest_age_us":null`) {
		t.Fatalf("zero/unknown distinction missing: %s %v", reported, err)
	}
	if err = json.Unmarshal(reported, &decoded); err != nil || decoded.UpstreamFeedback == nil || len(decoded.UpstreamFeedback.DurationHistogram) != 8 {
		t.Fatal("reported observation contract did not round-trip")
	}
}
