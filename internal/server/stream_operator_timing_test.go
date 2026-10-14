package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"noxa/internal/webrtc"
)

type timingDiagnosticVoice struct {
	fakeVoice
	actions []string
	onArm   func()
}

func (v *timingDiagnosticVoice) StreamTimingDiagnostics(q webrtc.StreamTimingRequest, action string) (*webrtc.StreamTimingDiagnostics, error) {
	v.actions = append(v.actions, action)
	if action == "arm" && v.onArm != nil {
		v.onArm()
	}
	return &webrtc.StreamTimingDiagnostics{PeerID: q.PeerID, SSRC: q.SSRC}, nil
}
func timingQueryFixture() string {
	return "?publisher_id=pub&slot=screen&generation=23&ssrc=42&route_token=" + strings.Repeat("a", 64) + "&peer_id=peer-one&ingress_started_at=1234"
}
func TestStreamTimingLocalExplicitOperation(t *testing.T) {
	for _, tc := range []struct {
		name, method, remote, header, origin string
		status                               int
		action                               string
	}{
		{"remote", "POST", "192.0.2.1:12", "upstream-timing", "", 403, ""},
		{"get", "GET", "127.0.0.1:12", "", "", 200, "read"},
		{"arm", "POST", "127.0.0.1:12", "upstream-timing", "", 200, "arm"},
		{"stop", "DELETE", "[::1]:12", "upstream-timing", "", 200, "stop"},
		{"missing header", "POST", "127.0.0.1:12", "", "", 400, ""},
		{"browser origin", "POST", "127.0.0.1:12", "upstream-timing", "https://example.test", 400, ""},
		{"put", "PUT", "127.0.0.1:12", "upstream-timing", "", 405, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, _ := routeHandlerFixture()
			voice := &timingDiagnosticVoice{}
			server.deps.Voice = voice
			req := httptest.NewRequestWithContext(t.Context(), tc.method, "/debug/stream-timing"+timingQueryFixture(), nil)
			req.RemoteAddr = tc.remote
			req.Header.Set("X-NoXa-Diagnostic", tc.header)
			req.Header.Set("Origin", tc.origin)
			req.Header.Set("X-Forwarded-For", "127.0.0.1")
			out := httptest.NewRecorder()
			server.StreamTimingDiagnosticsHandler().ServeHTTP(out, req)
			if out.Code != tc.status {
				t.Fatalf("status=%d body=%s", out.Code, out.Body.String())
			}
			if tc.action == "" && len(voice.actions) != 0 {
				t.Fatal("unauthorized backend call")
			}
			if tc.action != "" && (len(voice.actions) != 1 || voice.actions[0] != tc.action) {
				t.Fatalf("actions %v", voice.actions)
			}
		})
	}
}
func TestStreamTimingQueryStrictPins(t *testing.T) {
	base := strings.TrimPrefix(timingQueryFixture(), "?")
	if _, ok := streamTimingQuery(base); !ok {
		t.Fatal("valid rejected")
	}
	for _, raw := range []string{base + "&peer_id=other", strings.Replace(base, "ingress_started_at=1234", "ingress_started_at=01234", 1), strings.Replace(base, "route_token="+strings.Repeat("a", 64), "", 1), base + "&duration=3600", base + "&unknown=x"} {
		if _, ok := streamTimingQuery(raw); ok {
			t.Fatalf("bad query accepted %q", raw)
		}
	}
}
func TestStreamTimingAuthRaceStopsArmedRecord(t *testing.T) {
	server, _ := routeHandlerFixture()
	voice := &timingDiagnosticVoice{}
	server.deps.Voice = voice
	voice.onArm = func() { server.mu.Lock(); server.clients["pub"] = &Client{ID: "pub", authed: true}; server.mu.Unlock() }
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/debug/stream-timing"+timingQueryFixture(), nil)
	req.RemoteAddr = "127.0.0.1:12"
	req.Header.Set("X-NoXa-Diagnostic", "upstream-timing")
	out := httptest.NewRecorder()
	server.StreamTimingDiagnosticsHandler().ServeHTTP(out, req)
	if out.Code != 409 || len(voice.actions) != 2 || voice.actions[1] != "stop" {
		t.Fatalf("status/actions %d %v", out.Code, voice.actions)
	}
}
