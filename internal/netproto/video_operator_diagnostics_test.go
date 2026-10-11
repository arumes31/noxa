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
