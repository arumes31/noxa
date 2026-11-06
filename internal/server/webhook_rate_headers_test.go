package server

import (
	"net/http/httptest"
	"testing"
	"time"

	"noxa/internal/store"
)

func TestWebhookRateHeaders(t *testing.T) {
	now := time.Now()
	w := httptest.NewRecorder()
	if got := setWebhookRateHeaders(w, store.WebhookIdentity{ID: 7, RateRemaining: 2, RateReset: now.Add(2500 * time.Millisecond)}, now); got != 3 {
		t.Fatalf("reset = %d, want 3 (rounded up)", got)
	}
	if h := w.Header(); h.Get("X-RateLimit-Limit") != "5" || h.Get("X-RateLimit-Remaining") != "2" || h.Get("X-RateLimit-Reset") != "3" {
		t.Fatalf("headers = %v", h)
	}
	w = httptest.NewRecorder()
	if got := setWebhookRateHeaders(w, store.WebhookIdentity{}, now); got != 0 || w.Header().Get("X-RateLimit-Limit") != "" {
		t.Fatalf("unidentified hook produced headers %v", w.Header())
	}
	w = httptest.NewRecorder()
	setWebhookRateHeaders(w, store.WebhookIdentity{ID: 7, RateRemaining: -1, RateReset: now.Add(-time.Second)}, now)
	if h := w.Header(); h.Get("X-RateLimit-Remaining") != "0" || h.Get("X-RateLimit-Reset") != "0" {
		t.Fatalf("clamped headers = %v", h)
	}
}
