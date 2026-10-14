package server

import (
	"net/http/httptest"
	"strings"
	"testing"

	"noxa/internal/health"
	"noxa/internal/state"
	"noxa/internal/webrtc"
)

type routeDiagnosticVoice struct {
	fakeVoice
	calls int
	read  func(webrtc.StreamRouteRequest) (*webrtc.StreamRouteDiagnostics, error)
}

func (v *routeDiagnosticVoice) StreamRouteDiagnostics(q webrtc.StreamRouteRequest) (*webrtc.StreamRouteDiagnostics, error) {
	v.calls++
	return v.read(q)
}
func routeHandlerFixture() (*TCPServer, *routeDiagnosticVoice) {
	voice := &routeDiagnosticVoice{read: func(q webrtc.StreamRouteRequest) (*webrtc.StreamRouteDiagnostics, error) {
		return &webrtc.StreamRouteDiagnostics{PublisherID: q.PublisherID, Slot: q.Slot, Generation: q.Generation, SSRC: q.SSRC, ChannelID: 7, PeerID: "peer-one", RouteToken: strings.Repeat("a", 64), Local: webrtc.StreamRouteCandidate{Address: "192.0.2.1", Port: 12334, Protocol: "udp", Type: "host"}}, nil
	}}
	s := &TCPServer{deps: &Deps{State: state.New(nil), Voice: voice}, clients: map[string]*Client{"pub": {ID: "pub", authed: true}}}
	s.deps.State.AddClient(&state.Client{ClientID: "pub", ChannelID: 7, ChannelEpoch: 1})
	return s, voice
}
func routeRead(t *testing.T, s *TCPServer, method, query, remote string) *httptest.ResponseRecorder {
	t.Helper()
	server := health.New("127.0.0.1:0", nil, nil)
	server.HandleLocalGET("/debug/stream-route", s.StreamRouteDiagnosticsHandler())
	request := httptest.NewRequestWithContext(t.Context(), method, "/debug/stream-route"+query, nil)
	request.RemoteAddr = remote
	request.Header.Set("Forwarded", "for=127.0.0.1")
	request.Header.Set("X-Forwarded-For", "127.0.0.1")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	return response
}

const validRouteQuery = "?publisher_id=pub&slot=screen&generation=23&ssrc=42"

func TestStreamRouteHandlerLocalOnly(t *testing.T) {
	for _, tc := range []struct {
		method, remote string
		status         int
	}{{"GET", "127.0.0.1:123", 200}, {"GET", "[::1]:123", 200}, {"GET", "192.0.2.1:123", 403}, {"GET", "localhost:123", 403}, {"POST", "127.0.0.1:123", 405}} {
		s, voice := routeHandlerFixture()
		response := routeRead(t, s, tc.method, validRouteQuery, tc.remote)
		if response.Code != tc.status || response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("response %d %s", response.Code, response.Body.String())
		}
		if tc.status != 200 && voice.calls != 0 {
			t.Fatal("rejected request reached ICE")
		}
	}
}
func TestStreamRouteHandlerScopeValidation(t *testing.T) {
	for _, query := range []string{"", validRouteQuery + "&publisher_id=pub", validRouteQuery + "&other=value", strings.Replace(validRouteQuery, "generation=23", "generation=0", 1), strings.Replace(validRouteQuery, "generation=23", "generation=023", 1), strings.Replace(validRouteQuery, "ssrc=42", "ssrc=4294967296", 1), validRouteQuery + "&route_token=", validRouteQuery + "&route_token=bad"} {
		s, voice := routeHandlerFixture()
		response := routeRead(t, s, "GET", query, "127.0.0.1:1")
		if response.Code != 400 || voice.calls != 0 {
			t.Fatalf("invalid query accepted %q", query)
		}
	}
	for _, name := range []string{"unauthenticated", "revoked", "removed", "replacement", "channel", "epoch", "backend-stale"} {
		t.Run(name, func(t *testing.T) {
			s, voice := routeHandlerFixture()
			original := voice.read
			if name == "unauthenticated" {
				s.clients["pub"].authed = false
			}
			voice.read = func(q webrtc.StreamRouteRequest) (*webrtc.StreamRouteDiagnostics, error) {
				switch name {
				case "revoked":
					s.clients["pub"].revoked = true
				case "removed":
					s.deps.State.RemoveClient("pub")
				case "replacement":
					s.clients["pub"] = &Client{ID: "pub", authed: true}
				case "channel":
					s.deps.State.RemoveClient("pub")
					s.deps.State.AddClient(&state.Client{ClientID: "pub", ChannelID: 8, ChannelEpoch: 1})
				case "epoch":
					s.deps.State.RemoveClient("pub")
					s.deps.State.AddClient(&state.Client{ClientID: "pub", ChannelID: 7, ChannelEpoch: 2})
				case "backend-stale":
					return nil, webrtc.ErrStreamRouteUnavailable
				}
				return original(q)
			}
			if response := routeRead(t, s, "GET", validRouteQuery, "127.0.0.1:1"); response.Code != 409 {
				t.Fatalf("stale scope returned %d", response.Code)
			}
		})
	}
}
