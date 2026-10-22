package main

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestVideoPortalFixedEndpointsAndMetadata(t *testing.T) {
	for _, tc := range []struct{ provider, resource, host, path, body, thumbnail string }{
		{"vimeo", "22439234", "vimeo.com", "/api/oembed.json", `{"title":"The Mountain","author_name":"Creator","thumbnail_url":"https://i.vimeocdn.com/video/demo.jpg","html":"<script>bad()</script>"}`, "https://i.vimeocdn.com/video/demo.jpg"},
		{"dailymotion", "x84sh87", "www.dailymotion.com", "/services/oembed", `{"title":"The Mountain","author_name":"Creator","thumbnail_url":"https://s2.dmcdn.net/v/demo/x720"}`, "https://s2.dmcdn.net/v/demo/x720"},
		{"twitch", "clip:PublicClip_123", "clips.twitch.tv", "/PublicClip_123", `<head><meta content="The Mountain" property="og:title"><meta property='og:profile:username' content='Creator'><meta property="og:image" content="https://static-cdn.jtvnw.net/clip.jpg"><script>bad()</script></head><meta property="og:title" content="ignored body">`, "https://static-cdn.jtvnw.net/clip.jpg"},
		{"whatnot", "user:creator", "www.whatnot.com", "/user/creator", `<head><meta property="og:title" content="The Mountain"><meta property="og:profile:username" content="Creator"><meta property="og:image" content="https://images.whatnot.com/demo.jpg"></head>`, "https://images.whatnot.com/demo.jpg"},
	} {
		t.Run(tc.provider, func(t *testing.T) {
			got, err := fetchVideoPortalPreview(context.Background(), tc.provider, tc.resource, func(req *http.Request) (*http.Response, error) {
				if req.URL.Scheme != "https" || req.URL.Host != tc.host || req.URL.Path != tc.path || req.Header.Get("User-Agent") != "noXaLinkPreview/1.0" {
					t.Fatalf("unexpected request: %s", req.URL)
				}
				if tc.provider == "vimeo" && req.URL.Query().Get("url") != "https://vimeo.com/22439234" {
					t.Fatal("invalid canonical Vimeo URL")
				}
				if tc.provider == "dailymotion" && req.URL.Query().Get("url") != "https://www.dailymotion.com/video/x84sh87" {
					t.Fatal("invalid canonical Dailymotion URL")
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(tc.body))}, nil
			})
			if err != nil || got.Title != "The Mountain" || got.Author != "Creator" || got.Thumbnail != tc.thumbnail {
				t.Fatalf("preview=%#v, %v", got, err)
			}
		})
	}
}

func TestVideoPortalRejectsInvalidResourcesBeforeRequest(t *testing.T) {
	for _, tc := range []struct{ provider, id string }{{"evil", "123"}, {"vimeo", "../private"}, {"vimeo", "0"}, {"dailymotion", "x?redirect=evil"}, {"twitch", "clip:../private"}, {"twitch", "channel:evil.test"}, {"twitch", "video:https://evil.test"}, {"whatnot", "user:.."}, {"whatnot", "s:../../private"}, {"whatnot", "live:not-a-uuid"}} {
		_, err := fetchVideoPortalPreview(context.Background(), tc.provider, tc.id, func(*http.Request) (*http.Response, error) { t.Fatal("unexpected request"); return nil, nil })
		if err == nil {
			t.Errorf("accepted %#v", tc)
		}
	}
}

func TestVideoPortalThumbnailAllowlist(t *testing.T) {
	for _, tc := range []struct{ provider, value string }{{"vimeo", "https://i.vimeocdn.com.evil.test/image"}, {"vimeo", "http://i.vimeocdn.com/image"}, {"twitch", "https://127.0.0.1/image"}, {"twitch", "https://user@static-cdn.jtvnw.net/image"}, {"twitch", "https://static-cdn.jtvnw.net:8443/image"}, {"dailymotion", "https://evil.dmcdn.net/image"}, {"whatnot", "https://images.whatnot.com.evil.test/image"}, {"whatnot", "https://127.0.0.1/image"}} {
		if got := safePortalThumbnail(tc.provider, tc.value); got != "" {
			t.Errorf("accepted %q", got)
		}
	}
}

func TestVideoPortalRejectsUnavailableAndOversizeResponses(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
	}{{302, "redirect"}, {404, "missing"}, {429, "limited"}, {200, "not json"}, {200, strings.Repeat("x", 64*1024+1)}, {200, `{"html":"<script>no title</script>"}`}} {
		_, err := fetchVideoPortalPreview(context.Background(), "vimeo", "22439234", func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader(tc.body))}, nil
		})
		if err == nil {
			t.Errorf("accepted response %d", tc.status)
		}
	}
}

func TestWhatnotRedirectsStayOnRecognizedPublicPaths(t *testing.T) {
	for _, raw := range []string{"https://evil.test/live/123", "http://www.whatnot.com/user/test", "https://www.whatnot.com:8443/user/test", "https://user@www.whatnot.com/user/test", "https://www.whatnot.com/api/private", "https://www.whatnot.com.evil.test/user/test"} {
		u, _ := url.Parse(raw)
		if portalPreviewRedirect("whatnot", &http.Request{URL: u}, nil) == nil {
			t.Errorf("accepted redirect %q", raw)
		}
	}
	u, _ := url.Parse("https://www.whatnot.com/live/59f6f43b-0b3f-4181-b873-2c7b4d31f77f?invitedBy=test")
	if err := portalPreviewRedirect("whatnot", &http.Request{URL: u}, []*http.Request{{}}); err != nil || u.RawQuery != "" {
		t.Fatalf("valid redirect: %s, %v", u, err)
	}
	if portalPreviewRedirect("whatnot", &http.Request{URL: u}, make([]*http.Request, 3)) == nil {
		t.Fatal("unbounded redirects")
	}
	if portalPreviewRedirect("vimeo", &http.Request{URL: u}, nil) == nil {
		t.Fatal("other provider redirects allowed")
	}
}
