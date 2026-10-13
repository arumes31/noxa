package netproto

import (
	"encoding/json"
	"testing"
)

func TestKeyframeDiagnosticsOptionalFixedSchema(t *testing.T) {
	var old VideoStreamLayerDiagnostics
	if err := json.Unmarshal([]byte(`{"rid":"f","ssrc":42,"started_at":1,"ingress":{}}`), &old); err != nil {
		t.Fatal(err)
	}
	if old.KeyframeRequests != nil {
		t.Fatal("old response invented measured zero")
	}
	raw, err := json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]json.RawMessage
	if err = json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	if _, present := wire["keyframe_requests"]; present {
		t.Fatal("absent diagnostic not omitted")
	}
	current := VideoKeyframeRequestDiagnostics{H264Assembly: VideoKeyframeRequestCounters{Requests: 3, Coalesced: 1, WriteSucceeded: 1, WriteFailed: 1}}
	raw, err = json.Marshal(current)
	if err != nil {
		t.Fatal(err)
	}
	var reasons map[string]map[string]uint64
	if err = json.Unmarshal(raw, &reasons); err != nil {
		t.Fatal(err)
	}
	names := []string{"consumer_start", "recording_source", "watch_start", "quality_change", "vp8_bounds", "h264_assembly", "await_keyframe", "viewer_feedback", "explicit"}
	if len(reasons) != len(names) {
		t.Fatal("unbounded reason schema")
	}
	for _, name := range names {
		counters, ok := reasons[name]
		if !ok || len(counters) != 4 {
			t.Fatal("reason/counter fields changed")
		}
		for _, field := range []string{"requests", "coalesced", "write_succeeded", "write_failed"} {
			if _, ok := counters[field]; !ok {
				t.Fatal("missing counter")
			}
		}
	}
	if reasons["h264_assembly"]["requests"] != 3 || reasons["h264_assembly"]["write_failed"] != 1 {
		t.Fatal("outcomes conflated")
	}
}
