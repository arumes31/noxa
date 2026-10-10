package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"noxa/internal/health"
	"noxa/internal/netproto"
	"noxa/internal/state"
)

type operatorDiagnosticVoice struct {
	fakeVoice
	snapshot func() *netproto.VideoOperatorDiagnostics
	calls    int
}

func (v *operatorDiagnosticVoice) VideoOperatorDiagnostics() *netproto.VideoOperatorDiagnostics {
	v.calls++
	return v.snapshot()
}

func operatorDiagnosticFixture() (*TCPServer, *operatorDiagnosticVoice) {
	now := time.Now()
	voice := &operatorDiagnosticVoice{snapshot: func() *netproto.VideoOperatorDiagnostics {
		return &netproto.VideoOperatorDiagnostics{SampledAt: now.UnixMilli(), Publications: []netproto.VideoOperatorPublication{{
			PublisherID: "publisher", Slot: "screen", Generation: 7, ChannelID: 9,
			Layers:  []netproto.VideoStreamLayerDiagnostics{{SSRC: 123, StartedAt: now.Add(-5 * time.Second).UnixMilli()}},
			Viewers: []netproto.VideoOperatorViewer{{ClientID: "watcher", Session: 8, Watching: true, OutputExists: true, OutputActive: true}, {ClientID: "idle", Session: 9}},
		}}}
	}}
	s := &TCPServer{deps: &Deps{State: state.New(nil), Voice: voice}, clients: make(map[string]*Client)}
	for _, id := range []string{"publisher", "watcher", "idle"} {
		s.clients[id] = &Client{ID: id, authed: true, clientVersion: "0.5.44", UniqueID: "private-identity"}
		s.deps.State.AddClient(&state.Client{ClientID: id, ChannelID: 9, ChannelEpoch: 4, Nickname: "Name " + id, Metadata: map[string]string{"private": "private-metadata"}})
	}
	s.clients["publisher"].voiceTelemetry = &netproto.VoiceTelemetry{ChannelID: 9, VideoSenders: []netproto.VideoSenderDiagnostics{{SSRC: 123, Slot: "screen", Generation: 7}}}
	s.clients["publisher"].voiceTelemetryAt = now.Add(-time.Second)
	s.clients["publisher"].voiceTelemetryEpoch = 4
	return s, voice
}

func operatorDiagnosticRead(t *testing.T, s *TCPServer, method, query, remote string) *httptest.ResponseRecorder {
	t.Helper()
	listener := health.New("127.0.0.1:0", nil, nil)
	listener.HandleLocalGET("/debug/streams", s.StreamOperatorDiagnosticsHandler())
	request := httptest.NewRequestWithContext(t.Context(), method, "/debug/streams"+query, nil)
	request.RemoteAddr = remote
	request.Header.Set("X-Forwarded-For", "127.0.0.1")
	request.Header.Set("Forwarded", "for=127.0.0.1")
	response := httptest.NewRecorder()
	listener.Handler().ServeHTTP(response, request)
	return response
}

func TestStreamOperatorDiagnosticsLoopbackOnlyAndReadOnly(t *testing.T) {
	for _, tc := range []struct {
		name, method, remote string
		status               int
	}{
		{"IPv4", http.MethodGet, "127.0.0.1:1234", http.StatusOK},
		{"IPv6", http.MethodGet, "[::1]:1234", http.StatusOK},
		{"remote despite forwarded headers", http.MethodGet, "192.0.2.5:1234", http.StatusForbidden},
		{"hostname", http.MethodGet, "localhost:1234", http.StatusForbidden},
		{"post", http.MethodPost, "127.0.0.1:1234", http.StatusMethodNotAllowed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, voice := operatorDiagnosticFixture()
			response := operatorDiagnosticRead(t, s, tc.method, "", tc.remote)
			if response.Code != tc.status || response.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("status/cache = %d/%q", response.Code, response.Header().Get("Cache-Control"))
			}
			if tc.status != http.StatusOK && voice.calls != 0 {
				t.Fatal("rejected request reached the media snapshot")
			}
		})
	}
}

func TestStreamOperatorDiagnosticsEnrichesCurrentScopedTelemetry(t *testing.T) {
	s, _ := operatorDiagnosticFixture()
	response := operatorDiagnosticRead(t, s, http.MethodGet, "?nickname=NAME%20PUBLISHER", "127.0.0.1:1234")
	var result netproto.VideoOperatorDiagnostics
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &result) != nil || len(result.Publications) != 1 {
		t.Fatalf("operator response %d: %s", response.Code, response.Body.String())
	}
	publication := result.Publications[0]
	if publication.PublisherNickname != "Name publisher" || publication.PublisherVersion != "0.5.44" || publication.SenderReport == nil || len(publication.SenderReport.Rows) != 1 {
		t.Fatalf("publisher enrichment = %+v", publication)
	}
	if len(publication.Viewers) != 2 || publication.Viewers[0].Nickname != "Name watcher" || publication.Viewers[0].ClientVersion != "0.5.44" || !publication.Viewers[0].Watching || publication.Viewers[1].Watching {
		t.Fatalf("watch-state distinction = %+v", publication.Viewers)
	}
	for _, excluded := range []string{"private-identity", "private-metadata", "unique_id", "password", "address", "jpeg"} {
		if strings.Contains(response.Body.String(), excluded) {
			t.Fatalf("operator snapshot included excluded field %q", excluded)
		}
	}
}

func TestStreamOperatorDiagnosticsDropsRetiredMembersAndSenderReports(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*TCPServer)
	}{
		{"stale report", func(s *TCPServer) { s.clients["publisher"].voiceTelemetryAt = time.Now().Add(-16 * time.Second) }},
		{"channel epoch changed", func(s *TCPServer) { s.clients["publisher"].voiceTelemetryEpoch++ }},
		{"publication changed", func(s *TCPServer) { s.clients["publisher"].voiceTelemetry.VideoSenders[0].Generation++ }},
		{"source changed", func(s *TCPServer) { s.clients["publisher"].voiceTelemetry.VideoSenders[0].SSRC++ }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := operatorDiagnosticFixture()
			tc.change(s)
			s.deps.State.RemoveClient("idle")
			response := operatorDiagnosticRead(t, s, http.MethodGet, "", "127.0.0.1:1234")
			var result netproto.VideoOperatorDiagnostics
			if json.Unmarshal(response.Body.Bytes(), &result) != nil || len(result.Publications) != 1 || result.Publications[0].SenderReport != nil || len(result.Publications[0].Viewers) != 1 {
				t.Fatalf("retired sample/member leaked: %s", response.Body.String())
			}
			s.clients["publisher"].revoked = true
			response = operatorDiagnosticRead(t, s, http.MethodGet, "", "127.0.0.1:1234")
			if json.Unmarshal(response.Body.Bytes(), &result) != nil || len(result.Publications) != 0 {
				t.Fatalf("revoked publisher leaked: %s", response.Body.String())
			}
		})
	}
}

func TestStreamOperatorDiagnosticsBoundsAndFilters(t *testing.T) {
	s, voice := operatorDiagnosticFixture()
	for _, query := range []string{"?unknown=x", "?publisher_id=a&nickname=b", "?publisher_id=a&publisher_id=b", "?nickname=" + strings.Repeat("a", 129), "?publisher_id=" + strings.Repeat("a", 81)} {
		if response := operatorDiagnosticRead(t, s, http.MethodGet, query, "127.0.0.1:1234"); response.Code != http.StatusBadRequest {
			t.Fatalf("invalid filter status %d: %s", response.Code, query)
		}
	}
	if voice.calls != 0 {
		t.Fatal("invalid filter captured media state")
	}
	snapshot := voice.snapshot()
	publication := snapshot.Publications[0]
	publication.Viewers = make([]netproto.VideoOperatorViewer, netproto.MaxVideoOperatorViewers+1)
	for i := range publication.Viewers {
		publication.Viewers[i] = netproto.VideoOperatorViewer{ClientID: "watcher"}
	}
	snapshot.Publications = make([]netproto.VideoOperatorPublication, netproto.MaxVideoOperatorPublications+1)
	for i := range snapshot.Publications {
		snapshot.Publications[i] = publication
	}
	voice.snapshot = func() *netproto.VideoOperatorDiagnostics { return snapshot }
	response := operatorDiagnosticRead(t, s, http.MethodGet, "?publisher_id=publisher", "127.0.0.1:1234")
	var result netproto.VideoOperatorDiagnostics
	if json.Unmarshal(response.Body.Bytes(), &result) != nil || !result.Truncated || len(result.Publications) != netproto.MaxVideoOperatorPublications {
		t.Fatalf("publication bound lost: %s", response.Body.String())
	}
	for _, row := range result.Publications {
		if !row.ViewersTruncated || len(row.Viewers) != netproto.MaxVideoOperatorViewers {
			t.Fatalf("viewer bound lost: %+v", row)
		}
	}
	response = operatorDiagnosticRead(t, s, http.MethodGet, "?publisher_id=outside-bounded-snapshot", "127.0.0.1:1234")
	if json.Unmarshal(response.Body.Bytes(), &result) != nil || !result.Truncated || len(result.Publications) != 0 {
		t.Fatalf("filtered absence incorrectly presented as complete: %s", response.Body.String())
	}
}

func TestStreamOperatorDiagnosticsUnavailable(t *testing.T) {
	for _, s := range []*TCPServer{{}, {deps: &Deps{State: state.New(nil), Voice: &fakeVoice{}}}} {
		response := operatorDiagnosticRead(t, s, http.MethodGet, "", "127.0.0.1:1234")
		if response.Code != http.StatusServiceUnavailable {
			t.Fatalf("unavailable backend = %d", response.Code)
		}
	}
}

func TestStreamOperatorDiagnosticsRechecksCurrentMembership(t *testing.T) {
	s, _ := operatorDiagnosticFixture()
	s.clients["watcher"].revoked = true
	s.deps.State.AddClient(&state.Client{ClientID: "idle", ChannelID: 10, Nickname: "Moved elsewhere"})
	response := operatorDiagnosticRead(t, s, http.MethodGet, "", "127.0.0.1:1234")
	var result netproto.VideoOperatorDiagnostics
	if json.Unmarshal(response.Body.Bytes(), &result) != nil || len(result.Publications) != 1 || len(result.Publications[0].Viewers) != 0 {
		t.Fatalf("revoked or moved viewer retained: %s", response.Body.String())
	}
	s.deps.State.AddClient(&state.Client{ClientID: "publisher", ChannelID: 10, Nickname: "Moved publisher"})
	response = operatorDiagnosticRead(t, s, http.MethodGet, "", "127.0.0.1:1234")
	if json.Unmarshal(response.Body.Bytes(), &result) != nil || len(result.Publications) != 0 {
		t.Fatalf("publisher moved after backend snapshot: %s", response.Body.String())
	}
}
