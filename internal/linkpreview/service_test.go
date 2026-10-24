package linkpreview

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"net"
	"net/http"
	"net/netip"
	"noxa/internal/netproto"
	"strings"
	"testing"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func pngFixture(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
func TestOnlyPublicHTTPSPreviewTargets(t *testing.T) {
	for _, value := range []string{"http://example.com/", "file:///etc/passwd", "javascript:alert(1)", "https://user:pass@example.com/", "https://example.com:444/", "https://localhost/", "https://printer.local/", "https://127.0.0.1/", "https://10.1.2.3/", "https://169.254.169.254/", "https://100.100.100.200/", "https://[::1]/", "https://[::ffff:127.0.0.1]/", "https://[64:ff9b::7f00:1]/"} {
		if _, err := pageURL(value); err == nil {
			t.Errorf("accepted %s", value)
		}
	}
	for _, ip := range []string{"127.0.0.1", "10.0.0.1", "172.16.0.1", "192.168.1.1", "169.254.1.1", "100.64.0.1", "192.0.0.8", "198.18.0.1", "0.1.2.3", "224.0.0.1", "::ffff:10.0.0.1", "fc00::1", "fe80::1", "2001:db8::1", "2002:7f00:1::1"} {
		if publicIP(netip.MustParseAddr(ip)) {
			t.Errorf("public %s", ip)
		}
	}
	for _, ip := range []string{"8.8.8.8", "1.1.1.1", "2606:4700:4700::1111"} {
		if !publicIP(netip.MustParseAddr(ip)) {
			t.Errorf("blocked public %s", ip)
		}
	}
	c := newHTTPClient()
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://127.0.0.1/", nil)
	if c.CheckRedirect(req, []*http.Request{req}) == nil {
		t.Fatal("redirect to loopback allowed")
	}
	if c.Transport.(*http.Transport).Proxy != nil {
		t.Fatal("environment proxy may bypass address validation")
	}
}
func TestRasterDiscardsMetadataAndRejectsExecutableFormats(t *testing.T) {
	raw := append(pngFixture(t), []byte("<script>execute()</script>")...)
	encoded, err := raster(raw)
	if err != nil {
		t.Fatal(err)
	}
	data, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(encoded, "data:image/jpeg;base64,"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("script")) {
		t.Fatal("trailing source data survived reencoding")
	}
	if _, err := jpeg.Decode(bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	for _, payload := range []string{"<svg onload='alert(1)'/>", "MZ executable", "#!/bin/sh\nwhoami", "<!doctype html><script>run()</script>"} {
		if _, err := raster([]byte(payload)); err == nil {
			t.Fatalf("accepted %q", payload)
		}
	}
	var huge bytes.Buffer
	if err := png.Encode(&huge, image.NewGray(image.Rect(0, 0, 4097, 1))); err != nil {
		t.Fatal(err)
	}
	if _, err := raster(huge.Bytes()); err == nil {
		t.Fatal("oversized dimension accepted")
	}
}
func TestPreviewReturnsOnlyInertMetadataAndTranscodedBytes(t *testing.T) {
	s := New()
	calls := 0
	picture := pngFixture(t)
	s.client = &http.Client{Transport: transportFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		mime, body := "text/html", []byte(`<html><head><title>Fallback title</title><meta property="og:title" content="Example"><meta property="og:image" content="/photo.png"><meta name="description" content="Safe text"></head><script>fetch('https://evil.test')</script><iframe src="https://evil.test"></iframe>`)
		if req.URL.Path == "/photo.png" {
			mime, body = "image/png", picture
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {mime}}, Body: io.NopCloser(bytes.NewReader(body)), Request: req}, nil
	})}
	r := netproto.LinkPreviewRequest{Provider: "web", Resource: "https://example.com/"}
	got, err := s.Preview(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "Example" || got.Description != "Safe text" || !strings.HasPrefix(got.Thumbnail, "data:image/jpeg;base64,") {
		t.Fatalf("result %+v", got)
	}
	if calls != 2 {
		t.Fatalf("executed embedded resources: %d requests", calls)
	}
	if _, err := s.Preview(context.Background(), r); err != nil || calls != 2 {
		t.Fatal("cache missed")
	}
}
func TestPreviewRejectsExecutableMimeOversizeAndUnsafeThumbnails(t *testing.T) {
	for _, tc := range []struct{ mime, body string }{
		{"application/javascript", "alert(1)"}, {"application/octet-stream", "MZ"}, {"image/svg+xml", "<svg/>"},
		{"text/html", strings.Repeat("x", 256*1024+1)},
	} {
		t.Run(tc.mime, func(t *testing.T) {
			s := New()
			s.client = &http.Client{Transport: transportFunc(func(req *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {tc.mime}}, Body: io.NopCloser(strings.NewReader(tc.body)), Request: req}, nil
			})}
			if _, err := s.Preview(context.Background(), netproto.LinkPreviewRequest{Provider: "web", Resource: "https://example.com/"}); err == nil {
				t.Fatal("accepted disallowed response")
			}
		})
	}
	s := New()
	calls := 0
	s.client = &http.Client{Transport: transportFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/html"}}, Body: io.NopCloser(strings.NewReader(`<head><meta property="og:image" content="https://169.254.169.254/latest/meta-data"></head>`)), Request: req}, nil
	})}
	got, err := s.Preview(context.Background(), netproto.LinkPreviewRequest{Provider: "web", Resource: "https://example.com/"})
	if err != nil || calls != 1 || got.Thumbnail != "" {
		t.Fatalf("unsafe image fetched: calls=%d error=%v", calls, err)
	}
}
func TestImageMIMEAndGlobalConcurrencyBound(t *testing.T) {
	s := New()
	s.client = &http.Client{Transport: transportFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"image/svg+xml"}}, Body: io.NopCloser(bytes.NewReader(pngFixture(t))), Request: req}, nil
	})}
	if _, err := s.Preview(context.Background(), netproto.LinkPreviewRequest{Provider: "image", Resource: "https://example.com/image"}); err == nil {
		t.Fatal("declared active format accepted")
	}
	for range cap(s.slots) {
		s.slots <- struct{}{}
	}
	if _, err := s.Preview(context.Background(), netproto.LinkPreviewRequest{Provider: "web", Resource: "https://example.com/"}); err == nil {
		t.Fatal("concurrency bound ignored")
	}
}

func TestResolvedDialRejectsMixedDNSAndPinsPublicAddress(t *testing.T) {
	for _, addresses := range [][]netip.Addr{
		{netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("127.0.0.1")},
		{netip.MustParseAddr("8.8.8.8")},
	} {
		lookups, dials := 0, 0
		_, _ = resolvedDial(context.Background(), "tcp", "example.com", "443", func(context.Context, string, string) ([]netip.Addr, error) { lookups++; return addresses, nil }, func(_ context.Context, network, address string) (net.Conn, error) {
			dials++
			if network != "tcp" || address != "8.8.8.8:443" {
				t.Fatalf("dial re-resolved an untrusted hostname: %s", address)
			}
			return nil, errors.New("test dial")
		})
		if lookups != 1 {
			t.Fatal("DNS lookup repeated")
		}
		if len(addresses) == 2 && dials != 0 || len(addresses) == 1 && dials != 1 {
			t.Fatalf("unexpected dials: %d", dials)
		}
	}
}
