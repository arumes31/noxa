package server

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"noxa/internal/authorization"
	"noxa/internal/netproto"
)

func voiceDiagnosticsEnv(t *testing.T) (*testEnv, *memoryRoleStore, *authorization.Authority) {
	t.Helper()
	backend := serverRoleFixture()
	backend.policy.Roles[0].Permissions = []authorization.Capability{authorization.ViewChannel, authorization.Connect}
	backend.policy.Channels = append(backend.policy.Channels, authorization.ChannelPolicy{ChannelID: 2})
	authority, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	env := startTestEnvDeps(t, nil, nil, func(d *Deps) { d.Authority = authority })
	t.Cleanup(env.stop)
	env.state.AddChannel(testChannel(1))
	env.state.AddChannel(testChannel(2))
	return env, backend, authority
}

func voiceTelemetryFixture() netproto.VoiceTelemetry {
	return netproto.VoiceTelemetry{
		ChannelID: 1, ClientVersion: "test-client", ConnectionState: "connected", OutputState: "running", Volume: 100,
		Tracks: []netproto.VoiceReceiverDiagnostics{{TrackID: "voice-track", PublisherID: "publisher", Codec: "audio/opus"}},
	}
}

func voiceTelemetryBarrier(t *testing.T, conn net.Conn) {
	t.Helper()
	send(t, conn, netproto.MsgPing, netproto.Ping{})
	for {
		frame := readFrame(t, conn)
		switch netproto.MessageType(frame.Type) {
		case netproto.MsgError:
			var result netproto.Error
			if err := netproto.Decode(frame, &result); err != nil {
				t.Fatal(err)
			}
			t.Fatalf("voice telemetry rejected: %+v", result)
		case netproto.MsgPong:
			return
		}
	}
}

func queryVoiceDiagnostics(t *testing.T, conn net.Conn, target string) *netproto.VoiceDiagnostics {
	t.Helper()
	send(t, conn, netproto.MsgClientInfoQuery, netproto.ClientInfoQuery{ClientID: target})
	var response struct {
		Diagnostics json.RawMessage `json:"voice_diagnostics"`
	}
	if err := netproto.Decode(readOfType(t, conn, netproto.MsgClientInfoResponse), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Diagnostics) == 0 {
		return nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(response.Diagnostics, &fields); err != nil {
		t.Fatal(err)
	}
	if _, ok := fields["client_report"]; !ok {
		t.Fatal("diagnostic snapshot must explicitly include client_report, even when null")
	}
	var result netproto.VoiceDiagnostics
	if err := json.Unmarshal(response.Diagnostics, &result); err != nil {
		t.Fatal(err)
	}
	return &result
}

func TestVoiceTelemetryOnlyOwnerOrAdministratorCanReadClientReports(t *testing.T) {
	env, backend, authority := voiceDiagnosticsEnv(t)
	member, memberID := dialAuthed(t, env.addr, "admin-uid")
	defer func() { _ = member.Close() }()
	owner, ownerID := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = owner.Close() }()
	if err := env.state.MoveClient(memberID, 1); err != nil {
		t.Fatal(err)
	}
	send(t, member, netproto.MsgVoiceTelemetry, voiceTelemetryFixture())
	voiceTelemetryBarrier(t, member)
	got := queryVoiceDiagnostics(t, owner, memberID)
	if got == nil || got.ClientID != memberID || got.Nickname != "admin" || got.ChannelID != 1 || got.ClientReport == nil {
		t.Fatalf("owner missing authenticated member report: %+v", got)
	}
	if got.ClientReport.Stale || got.ClientReport.ReceivedAt <= 0 || got.ClientReport.AgeMS < 0 || got.ClientReport.Report.ClientVersion != "test-client" {
		t.Fatalf("fresh report metadata: %+v", got.ClientReport)
	}
	if got.ClientReport.Report.RTTMS != nil || len(got.ClientReport.Report.Tracks) != 1 || got.ClientReport.Report.Tracks[0].LossPercent != nil || got.Transport != nil {
		t.Fatalf("unavailable measurements became healthy zeroes: %+v", got)
	}
	missing := queryVoiceDiagnostics(t, owner, ownerID)
	if missing == nil || missing.ClientReport != nil {
		t.Fatalf("missing report should be null: %+v", missing)
	}
	for _, tc := range []struct {
		name        string
		permissions []authorization.Capability
		allowed     bool
	}{
		{"ordinary member", nil, false},
		{"connection metadata permission", []authorization.Capability{authorization.ViewConnectionInfo}, false},
		{"administrator", []authorization.Capability{authorization.Administrator}, true},
		{"administrator revoked", nil, false},
	} {
		backend.mu.Lock()
		backend.policy.Roles = backend.policy.Roles[:1]
		backend.policy.Roles = append(backend.policy.Roles, authorization.Role{ID: 20, Name: "Reader", Position: 1, Permissions: tc.permissions})
		backend.policy.Members = []authorization.RoleMember{{UserID: 1, RoleIDs: []int64{20}}}
		backend.policy.Revision++
		backend.mu.Unlock()
		if err := authority.Reload(t.Context()); err != nil {
			t.Fatal(err)
		}
		if got := queryVoiceDiagnostics(t, member, memberID); (got != nil) != tc.allowed {
			t.Fatalf("%s own diagnostic visibility: %+v", tc.name, got)
		}
		if got := queryVoiceDiagnostics(t, member, ownerID); (got != nil) != tc.allowed {
			t.Fatalf("%s other diagnostic visibility: %+v", tc.name, got)
		}
	}
}

func TestVoiceDiagnosticsHTTPFilters(t *testing.T) {
	env, _, _ := voiceDiagnosticsEnv(t)
	member, memberID := dialAuthed(t, env.addr, "admin-uid")
	defer func() { _ = member.Close() }()
	handler := env.srv.VoiceDiagnosticsHandler()
	for _, tc := range []struct {
		name, method, query string
		status              int
	}{
		{"client id", http.MethodGet, "?client_id=" + memberID, http.StatusOK},
		{"nickname case insensitive", http.MethodGet, "?nickname=ADMIN", http.StatusOK},
		{"unknown client", http.MethodGet, "?client_id=missing", http.StatusNotFound},
		{"ambiguous filter", http.MethodGet, "?client_id=" + memberID + "&nickname=admin", http.StatusBadRequest},
		{"oversized filter", http.MethodGet, "?client_id=" + strings.Repeat("x", 81), http.StatusBadRequest},
		{"read only", http.MethodPost, "", http.StatusMethodNotAllowed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequestWithContext(t.Context(), tc.method, "/debug/voice"+tc.query, nil))
			if response.Code != tc.status || response.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("status/cache = %d/%q, want %d/no-store", response.Code, response.Header().Get("Cache-Control"), tc.status)
			}
			if tc.status == http.StatusOK {
				var result struct {
					Clients []*netproto.VoiceDiagnostics `json:"clients"`
				}
				if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
					t.Fatal(err)
				}
				if len(result.Clients) != 1 || result.Clients[0].ClientID != memberID || result.Clients[0].ClientReport != nil {
					t.Fatalf("filtered diagnostics = %+v", result.Clients)
				}
			}
		})
	}
}

func TestVoiceTelemetryRejectsInvalidAdmission(t *testing.T) {
	env, _, _ := voiceDiagnosticsEnv(t)
	unauthenticated := dialRetry(t, env.addr)
	defer func() { _ = unauthenticated.Close() }()
	send(t, unauthenticated, netproto.MsgVoiceTelemetry, voiceTelemetryFixture())
	if got := readError(t, unauthenticated); got.Code != errCodeNotAuthenticated {
		t.Fatalf("unauthenticated telemetry: %+v", got)
	}
	member, memberID := dialAuthed(t, env.addr, "admin-uid")
	defer func() { _ = member.Close() }()
	owner, _ := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = owner.Close() }()
	if err := env.state.MoveClient(memberID, 1); err != nil {
		t.Fatal(err)
	}
	valid, err := json.Marshal(voiceTelemetryFixture())
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, payload string }{
		{"wrong channel", strings.Replace(string(valid), `"channel_id":1`, `"channel_id":2`, 1)},
		{"out of range measurement", strings.Replace(string(valid), `"rtt_ms":null`, `"rtt_ms":-1`, 1)},
		{"non-finite measurement", strings.Replace(string(valid), `"rtt_ms":null`, `"rtt_ms":1e999`, 1)},
		{"invalid state", strings.Replace(string(valid), `"output_state":"running"`, `"output_state":"made-up"`, 1)},
		{"oversized wire payload", string(valid) + strings.Repeat(" ", 32*1024)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := netproto.WriteFrame(member, &netproto.Frame{Type: uint16(netproto.MsgVoiceTelemetry), Payload: []byte(tc.payload)}); err != nil {
				t.Fatal(err)
			}
			if got := readError(t, member); got.Code == errCodeUnknown {
				t.Fatalf("telemetry handler missing: %+v", got)
			}
			if got := queryVoiceDiagnostics(t, owner, memberID); got == nil || got.ClientReport != nil {
				t.Fatalf("invalid report retained: %+v", got)
			}
		})
	}
}

// Backdate stored receipt time instead of waiting on a wall-clock timer.
func backdateVoiceTelemetry(t *testing.T, env *testEnv, clientID string, age time.Duration) {
	t.Helper()
	client, ok := env.srv.clientByID(clientID)
	if !ok {
		t.Fatal("reporter disconnected")
	}
	client.mu.Lock()
	client.voiceTelemetryAt = time.Now().Add(-age)
	client.mu.Unlock()
}

func TestVoiceTelemetryKeepsLatestSampleAndCurrentChannel(t *testing.T) {
	env, _, _ := voiceDiagnosticsEnv(t)
	member, memberID := dialAuthed(t, env.addr, "admin-uid")
	defer func() { _ = member.Close() }()
	owner, _ := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = owner.Close() }()
	if err := env.state.MoveClient(memberID, 1); err != nil {
		t.Fatal(err)
	}
	report := voiceTelemetryFixture()
	send(t, member, netproto.MsgVoiceTelemetry, report)
	voiceTelemetryBarrier(t, member)
	report.Volume = 150
	send(t, member, netproto.MsgVoiceTelemetry, report)
	voiceTelemetryBarrier(t, member)
	if got := queryVoiceDiagnostics(t, owner, memberID); got == nil || got.ClientReport == nil || got.ClientReport.Report.Volume != 100 {
		t.Fatalf("rapid report replaced accepted sample: %+v", got)
	}
	backdateVoiceTelemetry(t, env, memberID, 3*time.Second)
	send(t, member, netproto.MsgVoiceTelemetry, report)
	voiceTelemetryBarrier(t, member)
	if got := queryVoiceDiagnostics(t, owner, memberID); got == nil || got.ClientReport == nil || got.ClientReport.Report.Volume != 150 {
		t.Fatalf("next reporting interval did not replace sample: %+v", got)
	}
	backdateVoiceTelemetry(t, env, memberID, 16*time.Second)
	if got := queryVoiceDiagnostics(t, owner, memberID); got == nil || got.ClientReport == nil || !got.ClientReport.Stale || got.ClientReport.AgeMS < 15000 {
		t.Fatalf("old sample was not marked stale: %+v", got)
	}
	if err := env.state.MoveClient(memberID, 2); err != nil {
		t.Fatal(err)
	}
	if got := queryVoiceDiagnostics(t, owner, memberID); got == nil || got.ChannelID != 2 || got.ClientReport != nil {
		t.Fatalf("previous channel's report exposed: %+v", got)
	}
	if err := env.state.MoveClient(memberID, 1); err != nil {
		t.Fatal(err)
	}
	if got := queryVoiceDiagnostics(t, owner, memberID); got == nil || got.ClientReport != nil {
		t.Fatalf("returning to the same channel revived an earlier session's report: %+v", got)
	}
	if err := member.Close(); err != nil {
		t.Fatal(err)
	}
	reconnected, reconnectedID := dialAuthed(t, env.addr, "admin-uid")
	defer func() { _ = reconnected.Close() }()
	if err := env.state.MoveClient(reconnectedID, 1); err != nil {
		t.Fatal(err)
	}
	if got := queryVoiceDiagnostics(t, owner, reconnectedID); got == nil || got.ClientReport != nil {
		t.Fatalf("new connection inherited old report: %+v", got)
	}
}
