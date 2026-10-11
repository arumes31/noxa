package netproto

import (
	"encoding/json"
	"testing"
)

func TestH264DiscontinuityMasksAreAdditiveAndBounded(t *testing.T) {
	var old VideoH264RejectDiagnostics
	if err := json.Unmarshal([]byte(`{"malformed":0,"capacity":0,"discontinuity":3,"idle":0,"age":0}`), &old); err != nil {
		t.Fatal(err)
	}
	if old.Discontinuity != 3 || old.DiscontinuityMasks != nil {
		t.Fatal("old diagnostics acquired measured zero combination counts")
	}
	current := VideoH264RejectDiagnostics{Discontinuity: 28, DiscontinuityMasks: &[7]uint64{1, 2, 3, 4, 5, 6, 7}}
	encoded, err := json.Marshal(current)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatal(err)
	}
	if len(wire) != 6 || string(wire["discontinuity_masks"]) != "[1,2,3,4,5,6,7]" || string(wire["discontinuity"]) != "28" {
		t.Fatalf("unexpected additive wire schema: %s", encoded)
	}
}
