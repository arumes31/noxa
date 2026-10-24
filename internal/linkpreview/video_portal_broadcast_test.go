package linkpreview

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestPortalBroadcastDatesAndCategory(t *testing.T) {
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		start, end string
		live       bool
		want       string
	}{
		{"2026-10-22T11:00:00Z", "", true, "live"},
		{"2026-10-22T13:00:00Z", "", true, "scheduled"},
		{"2026-10-22T10:00:00Z", "2026-10-22T11:00:00Z", true, "offline"},
		{"", "", true, ""},
		{"invalid", "", true, ""},
		{"2026-10-22T11:00:00Z", "", false, ""},
	} {
		var data any
		raw := map[string]any{"@type": "VideoObject", "name": "Adventure", "genre": "Minecraft", "author": map[string]any{"name": "Creator"}, "publication": map[string]any{"@type": "BroadcastEvent", "startDate": tc.start, "endDate": tc.end, "isLiveBroadcast": tc.live}}
		bytes, _ := json.Marshal(raw)
		if err := json.Unmarshal(bytes, &data); err != nil {
			t.Fatal(err)
		}
		var result VideoPortalPreview
		applyPortalStructuredData(&result, data, now, 0)
		if result.LiveState != tc.want || result.Title != "Adventure" || result.Category != "Minecraft" || result.Author != "Creator" {
			t.Fatalf("result=%+v, want status %s", result, tc.want)
		}
	}
}

func TestPortalStructuredDataIsReadWithoutExecutingScripts(t *testing.T) {
	page := `<head><meta property="og:title" content="Fallback"><script type="application/ld+json">{"@type":"Event","name":"Collector show","organizer":{"name":"Seller"},"startDate":"2030-01-02T18:00:00Z"}</script><script>bad()</script></head>`
	result := publicPagePreview(strings.NewReader(page))
	if result.Title != "Collector show" || result.Author != "Seller" || result.StartsAt != "2030-01-02T18:00:00Z" || result.LiveState != "scheduled" {
		t.Fatalf("result=%+v", result)
	}
	for _, page := range []string{`<head><script type="application/ld+json">{bad JSON}</script></head>`, `<head><script type="application/ld+json">` + strings.Repeat("x", 70*1024) + `</script></head>`} {
		if result := publicPagePreview(strings.NewReader(page)); result.LiveState != "" {
			t.Fatal("invalid metadata produced status")
		}
	}
}
