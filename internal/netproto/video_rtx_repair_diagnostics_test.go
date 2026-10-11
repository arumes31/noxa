package netproto

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestVideoRTXRepairDiagnosticsAvailabilityAndOperatorScope(t *testing.T) {
	legacy, err := json.Marshal(VideoOperatorViewer{})
	if err != nil || strings.Contains(string(legacy), "rtx_repairs") {
		t.Fatal("unavailable repair diagnostics must remain absent")
	}
	var decoded VideoOperatorViewer
	if err = json.Unmarshal(legacy, &decoded); err != nil || decoded.RTXRepairs != nil {
		t.Fatal("legacy repair diagnostics must remain unknown")
	}
	reported, err := json.Marshal(VideoOperatorViewer{RTXRepairs: &VideoRTXRepairDiagnostics{}})
	if err != nil || !strings.Contains(string(reported), `"first_repairs":0`) {
		t.Fatal("supported zero repairs must remain explicit")
	}
	if err = json.Unmarshal(reported, &decoded); err != nil || decoded.RTXRepairs == nil || len(decoded.RTXRepairs.RepeatGapHistogram) != 7 {
		t.Fatal("repair histogram contract did not round-trip")
	}
	viewer, err := json.Marshal(VideoStreamForwardDiagnostics{})
	if err != nil || strings.Contains(string(viewer), "rtx_repairs") {
		t.Fatal("operator diagnostics escaped to viewer schema")
	}
}
