package linkpreview

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestYouTubePreviewFixedEndpoint(t *testing.T) {
	got, err := fetchYouTubePreview(context.Background(), "Fvr-SJPAh7o", func(req *http.Request) (*http.Response, error) {
		if req.URL.Scheme != "https" || req.URL.Host != "www.youtube.com" || req.URL.Path != "/oembed" || req.URL.Query().Get("url") != "https://www.youtube.com/watch?v=Fvr-SJPAh7o" {
			t.Fatalf("unexpected preview request: %s", req.URL)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"title":"A real title","author_name":"Creator","html":"<script>untrusted</script>","thumbnail_url":"http://127.0.0.1/private"}`))}, nil
	})
	if err != nil || got.Title != "A real title" || got.Author != "Creator" {
		t.Fatalf("preview = %#v, %v", got, err)
	}
}

func TestYouTubePreviewRejectsInvalidIDsBeforeRequest(t *testing.T) {
	for _, id := range []string{"", "short", "../private", "Fvr-SJPAh7o?x=1", "https://evil.test"} {
		_, err := fetchYouTubePreview(context.Background(), id, func(*http.Request) (*http.Response, error) { t.Fatal("unexpected network request"); return nil, nil })
		if err == nil {
			t.Errorf("accepted %q", id)
		}
	}
}

func TestYouTubePreviewRejectsOversizeAndRedirectResponses(t *testing.T) {
	for _, response := range []struct {
		status int
		body   string
	}{{302, "redirect"}, {200, strings.Repeat("x", 64*1024+1)}, {200, "invalid JSON"}} {
		_, err := fetchYouTubePreview(context.Background(), "Fvr-SJPAh7o", func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: response.status, Body: io.NopCloser(strings.NewReader(response.body))}, nil
		})
		if err == nil {
			t.Errorf("accepted invalid response: %d", response.status)
		}
	}
	if got := limitPreviewText("  "+strings.Repeat("ö", 201)+"  ", 200); len([]rune(got)) != 200 {
		t.Fatalf("title length = %d", len([]rune(got)))
	}
}
