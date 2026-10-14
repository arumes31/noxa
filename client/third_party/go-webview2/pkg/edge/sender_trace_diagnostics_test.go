package edge

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSenderTraceGlobalInstantIsDiagnosedNotAttributed(t *testing.T) {
	p := senderTraceParser{}
	global := `{"value":[{"name":"VCMEncodedFrameCallback::Encoded","cat":"webrtc","ph":"I","s":"g","ts":99,"pid":0,"tid":0,"args":{"timestamp":1000}}]}`
	if p.add(global) != "" {
		t.Fatal("fixture")
	}
	if p.add(traceMarkers) != "ambiguous_marker_scope" {
		t.Fatal("global track accepted as renderer ownership")
	}
	d := p.diagnostics
	if len(p.events) != 1 || p.counts["encode"] != 0 || d.FirstRejection.Kind != "encode" || d.FirstRejection.Check != "cross_kind_pid" || d.FirstRejection.PIDRelation != "different" {
		t.Fatalf("wrong first rejection: %+v", d.FirstRejection)
	}
	if d.ByKind["encoded"].Scopes["global"] != 1 || d.ByKind["encoded"].PIDStates["zero"] != 1 || d.ByKind["process"].Seen != 1 {
		t.Fatal("tail or global representation missing")
	}
	if d.EntriesExamined != 5 || d.EntriesAfterFreeze != 3 {
		t.Fatal("incorrect classified prefix/tail bounds")
	}
	frozen, _ := json.Marshal(p.events)
	first := *d.FirstRejection
	if p.add(traceMarkers) != "ambiguous_marker_scope" || *d.FirstRejection != first {
		t.Fatal("first error changed")
	}
	after, _ := json.Marshal(p.events)
	if string(after) != string(frozen) || d.ByKind["process"].Seen != 2 {
		t.Fatal("flush changed accepted events or lost counters")
	}
}

func TestSenderTraceProducerMismatchDiagnostics(t *testing.T) {
	for _, scenario := range []struct{ from, to, check, pid, tid string }{
		{`"pid":1`, `"pid":9`, "within_kind_pid", "different", "same"},
		{`"tid":2`, `"tid":9`, "within_kind_tid", "same", "different"},
	} {
		p := senderTraceParser{}
		if p.add(traceMarkers) != "" {
			t.Fatal("fixture")
		}
		if p.add(strings.Replace(traceMarkers, scenario.from, scenario.to, 1)) != "ambiguous_marker_scope" {
			t.Fatal("multiple producers accepted")
		}
		r := p.diagnostics.FirstRejection
		if r.Check != scenario.check || r.PIDRelation != scenario.pid || r.TIDRelation != scenario.tid || len(p.events) != 4 {
			t.Fatalf("wrong mismatch: %+v", r)
		}
	}
}

func TestSenderTraceDiagnosticVocabularyPrivacyAndMalformedFreeze(t *testing.T) {
	p := senderTraceParser{}
	bad := strings.Replace(traceMarkers, `"ts":100`, `"ts":null`, 1)
	if p.add(bad) != "malformed_marker" {
		t.Fatal("malformed marker accepted")
	}
	raw := `{"value":[{"name":"VideoEncoder::Encode","cat":"private-category","ph":"private-phase","s":"private-scope","pid":null,"tid":-1,"args":{"secret":"private-payload"}},{"name":"secret-title","pid":888888888},{"name":"TaskQueuePacedSender::MaybeProcessPackets","cat":"disabled-by-default-webrtc","ph":"E"},false]}`
	if p.add(raw) != "malformed_marker" || len(p.events) != 0 || p.diagnostics.FirstRejection.Check != "timestamp" {
		t.Fatal("malformed result was not frozen")
	}
	d := p.diagnostics
	if d.ByKind["encode"].CategoryOther != 1 || d.ByKind["encode"].Phases["other"] != 1 || d.ByKind["encode"].Scopes["other"] != 1 || d.ByKind["encode"].PIDStates["invalid"] != 1 || d.MalformedEntries != 1 {
		t.Fatal("fixed buckets missing")
	}
	b, _ := json.Marshal(d)
	if len(b) > 8192 {
		t.Fatal("diagnostics exceed reserved export allowance")
	}
	for _, secret := range []string{"private", "secret", "888888888", `"pid":`, `"tid":`, `"args":`, `"name":`} {
		if strings.Contains(string(b), secret) {
			t.Fatalf("raw metadata escaped: %s", secret)
		}
	}
}

func TestSenderTraceDiagnosticScanAndMessageBounds(t *testing.T) {
	p := senderTraceParser{}
	chunk := `{"value":[` + strings.Repeat(`{"name":"unknown"},`, senderTraceEventLimit-1) + `{"name":"unknown"}]}`
	for range senderTraceDiagnosticLimit / senderTraceEventLimit {
		if p.add(chunk) != "" {
			t.Fatal("premature diagnostic cap")
		}
	}
	if p.add(traceMarkers) != "diagnostic_limit" || !p.diagnostics.ClassificationTruncated || p.diagnostics.EntriesExamined != senderTraceDiagnosticLimit {
		t.Fatal("scan not bounded")
	}
	if p.add(traceMarkers) != "diagnostic_limit" || len(p.events) != 0 {
		t.Fatal("timing resumed after cap")
	}
	q := senderTraceParser{}
	if q.add(strings.Repeat(" ", senderTraceMessageLimit+1)) != "message_limit" {
		t.Fatal("message cap")
	}
	if q.add(traceMarkers) != "message_limit" || len(q.events) != 0 || q.diagnostics.ByKind["encode"].Seen != 1 {
		t.Fatal("oversized input did not freeze later timings")
	}
}
