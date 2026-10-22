package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"noxa/internal/netproto"
	"noxa/internal/state"
)

func diagnosticsLookupFixture(count int) *TCPServer {
	s := &TCPServer{deps: &Deps{State: state.New(nil)}, clients: make(map[string]*Client, count)}
	for i := count - 1; i >= 0; i-- {
		id := fmt.Sprintf("client-%05d", i)
		s.clients[id] = &Client{ID: id, authed: true}
		s.deps.State.AddClient(&state.Client{ClientID: id, Nickname: fmt.Sprintf("Member %d", i)})
	}
	return s
}

func TestVoiceDiagnosticsLookupPreservesFiltersAndListLimit(t *testing.T) {
	s := diagnosticsLookupFixture(300)
	s.clients["unauthed"] = &Client{ID: "unauthed"}
	for _, tc := range []struct {
		name, query   string
		status, count int
		truncated     bool
	}{
		{"exact ID beyond list limit", "?client_id=client-00299", http.StatusOK, 1, false},
		{"nickname beyond list limit", "?nickname=MEMBER%20299", http.StatusOK, 1, false},
		{"missing ID", "?client_id=missing", http.StatusNotFound, 0, false},
		{"unauthenticated ID", "?client_id=unauthed", http.StatusNotFound, 0, false},
		{"bounded sorted list", "", http.StatusOK, 256, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			s.VoiceDiagnosticsHandler().ServeHTTP(response, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/debug/voice"+tc.query, nil))
			if response.Code != tc.status {
				t.Fatalf("status = %d, want %d", response.Code, tc.status)
			}
			if tc.status != http.StatusOK {
				return
			}
			var result struct {
				Clients   []*netproto.VoiceDiagnostics
				Truncated bool
			}
			if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if len(result.Clients) != tc.count || result.Truncated != tc.truncated {
				t.Fatalf("unexpected result count/limit: %+v", result)
			}
			if tc.query != "" && result.Clients[0].ClientID != "client-00299" {
				t.Fatalf("wrong selected client: %+v", result.Clients[0])
			}
			for i := 1; i < len(result.Clients); i++ {
				if result.Clients[i-1].ClientID >= result.Clients[i].ClientID {
					t.Fatal("unfiltered diagnostics are not sorted")
				}
			}
		})
	}
}

func BenchmarkVoiceDiagnosticsExactClient(b *testing.B) {
	for _, count := range []int{100, 1000, 10000} {
		b.Run(fmt.Sprintf("clients-%d", count), func(b *testing.B) {
			s := diagnosticsLookupFixture(count)
			handler := s.VoiceDiagnosticsHandler()
			request := httptest.NewRequestWithContext(b.Context(), http.MethodGet, fmt.Sprintf("/debug/voice?client_id=client-%05d", count-1), nil)
			b.ReportAllocs()
			for b.Loop() {
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, request)
				if response.Code != http.StatusOK {
					b.Fatalf("unexpected status: %d", response.Code)
				}
			}
		})
	}
}
