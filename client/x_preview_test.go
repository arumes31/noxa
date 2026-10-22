package main

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestXPostPreviewFixedEndpointAndPlainText(t *testing.T) {
	got, err := fetchXPostPreview(context.Background(), "20", func(req *http.Request) (*http.Response, error) {
		q := req.URL.Query()
		if req.URL.Scheme != "https" || req.URL.Host != "publish.x.com" || req.URL.Path != "/oembed" || q.Get("url") != "https://x.com/i/status/20" || q.Get("omit_script") != "true" || q.Get("dnt") != "true" || q.Get("hide_thread") != "true" {
			t.Fatalf("unexpected preview request: %s", req.URL)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"author_name":"Author","html":"<blockquote><p>Hello &amp; goodbye<br>second <a href='javascript:bad()'>line</a><script>bad()</script><img src='http://127.0.0.1/private'></p>footer</blockquote><script src='https://platform.x.com/widgets.js'></script>","thumbnail_url":"http://127.0.0.1/private"}`))}, nil
	})
	if err != nil || got.Author != "Author" || got.Text != "Hello & goodbye\nsecond line" {
		t.Fatalf("preview = %#v, %v", got, err)
	}
}

func TestXPostPreviewRejectsInvalidIDsBeforeRequest(t *testing.T) {
	for _, id := range []string{"", "0", "-20", "../private", "20?url=evil", "https://evil.test", "123456789012345678901"} {
		_, err := fetchXPostPreview(context.Background(), id, func(*http.Request) (*http.Response, error) { t.Fatal("unexpected network request"); return nil, nil })
		if err == nil {
			t.Errorf("accepted %q", id)
		}
	}
}

func TestXPostPreviewRejectsUnavailableOrInvalidResponses(t *testing.T) {
	for _, response := range []struct {
		status int
		body   string
	}{{302, "redirect"}, {404, "private or deleted"}, {429, "limited"}, {200, strings.Repeat("x", 64*1024+1)}, {200, "invalid JSON"}, {200, `{"html":"<script>only a script</script>"}`}} {
		_, err := fetchXPostPreview(context.Background(), "20", func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: response.status, Body: io.NopCloser(strings.NewReader(response.body))}, nil
		})
		if err == nil {
			t.Errorf("accepted invalid response: %d", response.status)
		}
	}
	if got := xPostText("<p>" + strings.Repeat("ö", 2001) + "</p>"); len([]rune(got)) != 2000 {
		t.Fatalf("text length = %d", len([]rune(got)))
	}
}
