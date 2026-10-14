package edge

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSenderTraceNumericParserPrivacyAndPhases(t *testing.T) {
	p := senderTraceParser{}
	if why := p.add(traceMarkers); why != "" {
		t.Fatal(why)
	}
	if !p.markersPresent() || len(p.events) != 4 {
		t.Fatal("missing events")
	}
	for _, phase := range []string{"E", "e", "n", "R"} {
		x := strings.Replace(traceMarkers, `"ph":"X"`, `"ph":"`+phase+`"`, 1)
		q := senderTraceParser{}
		if q.add(x) != "" || q.counts["encode"] != 0 {
			t.Fatal("invented unsupported phase")
		}
	}
	b, _ := json.Marshal(p.events)
	for _, s := range []string{"private", "secret", "pid", "tid", "name", "storage"} {
		if strings.Contains(string(b), s) {
			t.Fatal("metadata escaped")
		}
	}
}
func TestSenderTraceParserMalformedAndBounds(t *testing.T) {
	cases := []struct{ raw, want string }{{`{`, "malformed_data"}, {`{"value":[]} {}`, "malformed_data"}, {strings.Repeat(" ", senderTraceMessageLimit+1), "message_limit"}, {strings.Replace(traceMarkers, `"rtp_timestamp":1000`, `"rtp_timestamp":4294967296`, 1), "malformed_marker"}, {strings.Replace(traceMarkers, `"ts":100`, `"ts":null`, 1), "malformed_marker"}, {strings.Replace(traceMarkers, `"sequence_number":3`, `"sequence_number":65536`, 1), "malformed_marker"}, {strings.Replace(traceMarkers, `"timestamp":1000`, `"timestamp":1.5`, 1), "malformed_marker"}}
	for _, tt := range cases {
		p := senderTraceParser{}
		if why := p.add(tt.raw); why != tt.want {
			t.Fatalf("want%s got%s", tt.want, why)
		}
	}
	p := senderTraceParser{rawBytes: senderTraceRawLimit}
	if p.add(traceMarkers) != "raw_limit" {
		t.Fatal("raw cap")
	}
	p = senderTraceParser{retainedBytes: senderTraceRetainedLimit}
	if p.add(traceMarkers) != "retained_limit" {
		t.Fatal("retained cap")
	}
	p = senderTraceParser{events: make([]SenderTraceEvent, senderTraceEventLimit)}
	if p.add(traceMarkers) != "event_limit" {
		t.Fatal("event cap")
	}
}
func TestSenderTraceMultipleProducerAmbiguity(t *testing.T) {
	p := senderTraceParser{}
	if p.add(traceMarkers) != "" {
		t.Fatal("initial")
	}
	if p.add(strings.Replace(traceMarkers, `"tid":2`, `"tid":9`, 1)) != "ambiguous_marker_scope" {
		t.Fatal("multiple encoders silently merged")
	}
}
func TestSenderTraceUnknownEventsAndCategoriesAreDiscarded(t *testing.T) {
	p := senderTraceParser{}
	if p.add(`{"value":[{"name":"secret url","args":{"content":"secret"}}]}`) != "" || len(p.events) != 0 || p.unknown != 1 {
		t.Fatal("unknown")
	}
	if p.add(strings.ReplaceAll(traceMarkers, `"webrtc"`, `"network"`)) != "" || p.counts["encode"] != 0 || p.counts["encoded"] != 0 {
		t.Fatal("category mismatch accepted")
	}
}

func TestSenderTraceCannotCombineDifferentRendererProcesses(t *testing.T) {
	p := senderTraceParser{}
	// Individually unique producer per kind is insufficient: all stages must
	// originate in the same renderer process, even though their threads differ.
	raw := strings.Replace(traceMarkers, `"ts":102,"pid":1`, `"ts":102,"pid":9`, 1)
	if p.add(raw) != "ambiguous_marker_scope" {
		t.Fatal("cross-renderer stages marked as one sender trace")
	}
}
