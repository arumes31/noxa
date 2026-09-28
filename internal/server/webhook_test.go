package server

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWebhookHTTPRejectsMalformedRequests(t *testing.T) {
	handler := (&TCPServer{}).IncomingWebhookHandler()
	for _, tc := range []struct {
		method, path, token, body string
		status                    int
	}{
		{"GET", "/hooks/1", "", "", 405},
		{"POST", "/hooks/not-a-number", "Bearer bad", `{"content":"test"}`, 401},
		{"POST", "/hooks/1", "", "{}", 401},
		{"POST", "/hooks/1", "Bearer " + strings.Repeat("a", 43), `{"content":"test","channel_id":2}`, 400},
		{"POST", "/hooks/1", "Bearer " + strings.Repeat("a", 43), `{"content":"test"}{}`, 400},
		{"POST", "/hooks/1", "Bearer " + strings.Repeat("a", 43), `{"content":"` + strings.Repeat("x", 13000) + `"}`, 400},
	} {
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		req.Header.Set("Authorization", tc.token)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != tc.status {
			t.Errorf("%s %s: status=%d want%d body=%s", tc.method, tc.path, rec.Code, tc.status, rec.Body.String())
		}
	}
}
