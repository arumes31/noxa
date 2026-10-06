package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"noxa/internal/netproto"
)

// These counters distinguish lost speech, generated silence and adaptive
// playback. Verify both owner and local-operator paths retain the same sample.
func TestVoicePlaybackDiagnosticsSurviveControlAndOperatorRead(t *testing.T) {
	env, _, _ := voiceDiagnosticsEnv(t)
	member, memberID := dialAuthed(t, env.addr, "admin-uid")
	defer func() { _ = member.Close() }()
	owner, _ := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = owner.Close() }()
	if err := env.state.MoveClient(memberID, 1); err != nil {
		t.Fatal(err)
	}
	number := func(value float64) *float64 { return &value }
	report := voiceTelemetryFixture()
	report.Tracks[0].SampleMS = number(5000)
	report.Tracks[0].TotalSamples = number(240000)
	report.Tracks[0].ConcealedSamples = number(3000)
	report.Tracks[0].SilentConcealedSamples = number(1000)
	report.Tracks[0].AcceleratedSamples = number(2400)
	report.Tracks[0].DeceleratedSamples = number(480)
	report.Tracks[0].SilentConcealmentPercent = number(0.4)
	report.Tracks[0].NonSilentConcealmentPercent = number(0.8)
	report.Tracks[0].AccelerationPercent = number(1)
	report.Tracks[0].DecelerationPercent = number(0.2)
	report.Tracks[0].BufferMS = number(155)
	report.Tracks[0].BufferTargetMS = number(140)
	report.Tracks[0].BufferMinimumMS = number(120)
	send(t, member, netproto.MsgVoiceTelemetry, report)
	voiceTelemetryBarrier(t, member)
	check := func(got *netproto.VoiceDiagnostics) {
		t.Helper()
		if got == nil || got.ClientReport == nil {
			t.Fatalf("accepted report missing: %+v", got)
		}
		if !reflect.DeepEqual(got.ClientReport.Report, report) {
			t.Fatalf("playback evidence changed: got=%+v want=%+v", got.ClientReport.Report, report)
		}
	}
	check(queryVoiceDiagnostics(t, owner, memberID))
	response := httptest.NewRecorder()
	env.srv.VoiceDiagnosticsHandler().ServeHTTP(response, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/debug/voice?client_id="+memberID, nil))
	if response.Code != http.StatusOK {
		t.Fatalf("operator read status = %d", response.Code)
	}
	var result struct {
		Clients []*netproto.VoiceDiagnostics `json:"clients"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Clients) != 1 {
		t.Fatalf("operator report count = %d", len(result.Clients))
	}
	check(result.Clients[0])
}
