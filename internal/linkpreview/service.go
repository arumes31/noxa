// Package linkpreview retrieves inert metadata on the server. It has no browser,
// script engine, subprocess runner, cookie jar, or access to local files.
package linkpreview

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
	"noxa/internal/netproto"
)

var errUnavailable = errors.New("link preview unavailable")
var blocked = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("2001::/23"), netip.MustParsePrefix("2001:db8::/32"), netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("3fff::/20"),
}

func publicIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	// Only globally allocated IPv6 space; excludes NAT64/translation, ULA and mapped local targets.
	if ip.Is6() && !netip.MustParsePrefix("2000::/3").Contains(ip) {
		return false
	}
	for _, prefix := range blocked {
		if prefix.Contains(ip) {
			return false
		}
	}
	return true
}
func pageURL(value string) (*url.URL, error) {
	if len(value) > 2048 {
		return nil, errUnavailable
	}
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || (u.Port() != "" && u.Port() != "443") || u.Opaque != "" {
		return nil, errUnavailable
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") || !strings.Contains(host, ".") && !strings.Contains(host, ":") {
		return nil, errUnavailable
	}
	if ip, err := netip.ParseAddr(host); err == nil && !publicIP(ip) {
		return nil, errUnavailable
	}
	u.Fragment = ""
	return u, nil
}

// Resolve once and dial that verified address: no second DNS lookup/rebinding.
func safeDial(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil || port != "443" {
		return nil, errUnavailable
	}
	dialer := net.Dialer{Timeout: 3 * time.Second}
	return resolvedDial(ctx, network, host, port, net.DefaultResolver.LookupNetIP, dialer.DialContext)
}

func resolvedDial(ctx context.Context, network, host, port string, lookup func(context.Context, string, string) ([]netip.Addr, error), dial func(context.Context, string, string) (net.Conn, error)) (net.Conn, error) {
	ips, err := lookup(ctx, "ip", host)
	if err != nil || len(ips) == 0 {
		return nil, errUnavailable
	}
	for _, ip := range ips {
		if !publicIP(ip) {
			return nil, errUnavailable
		}
	}
	for _, ip := range ips {
		conn, e := dial(ctx, network, net.JoinHostPort(ip.String(), port))
		if e == nil {
			return conn, nil
		}
		err = e
	}
	return nil, err
}
func newHTTPClient() *http.Client {
	return &http.Client{Timeout: 8 * time.Second, Transport: &http.Transport{
		Proxy: nil, DialContext: safeDial, DisableKeepAlives: true, MaxResponseHeaderBytes: 16 << 10,
		TLSHandshakeTimeout: 3 * time.Second, ResponseHeaderTimeout: 4 * time.Second,
	}, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 3 {
			return errUnavailable
		}
		_, err := pageURL(req.URL.String())
		return err
	}}
}

type cacheEntry struct {
	result  netproto.LinkPreviewResult
	expires time.Time
}
type Service struct {
	mu     sync.Mutex
	cache  map[netproto.LinkPreviewRequest]cacheEntry
	slots  chan struct{}
	client *http.Client
}

func New() *Service {
	return &Service{cache: make(map[netproto.LinkPreviewRequest]cacheEntry), slots: make(chan struct{}, 4), client: newHTTPClient()}
}
func (s *Service) Preview(ctx context.Context, r netproto.LinkPreviewRequest) (netproto.LinkPreviewResult, error) {
	var empty netproto.LinkPreviewResult
	if len(r.Resource) > 2048 || len(r.Provider) > 20 {
		return empty, errUnavailable
	}
	s.mu.Lock()
	cached, ok := s.cache[r]
	s.mu.Unlock()
	if ok && time.Now().Before(cached.expires) {
		return cached.result, nil
	}
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	default:
		return empty, errUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	result, err := s.fetch(ctx, r)
	if err != nil {
		return empty, errUnavailable
	}
	s.mu.Lock()
	if len(s.cache) >= 64 {
		for key := range s.cache {
			delete(s.cache, key)
			break
		}
	}
	s.cache[r] = cacheEntry{result: result, expires: time.Now().Add(time.Minute)}
	s.mu.Unlock()
	return result, nil
}
func (s *Service) fetch(ctx context.Context, r netproto.LinkPreviewRequest) (netproto.LinkPreviewResult, error) {
	var result netproto.LinkPreviewResult
	var thumbnail string
	client := *s.client
	if r.Provider != "web" && r.Provider != "image" {
		client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
			if _, err := pageURL(req.URL.String()); err != nil {
				return err
			}
			return portalPreviewRedirect(r.Provider, req, via)
		}
	}
	metadata := func(req *http.Request) (*http.Response, error) { return s.metadataRequest(req, &client) }
	// Each parser receives bounded inert bytes. Only the selected metadata fields leave it.
	switch r.Provider {
	case "image":
		data, err := s.image(ctx, r.Resource)
		result.Thumbnail = data
		return result, err
	case "youtube":
		p, err := fetchYouTubePreview(ctx, r.Resource, metadata)
		if err != nil {
			return result, err
		}
		result.Title, result.Author = p.Title, p.Author
		thumbnail = "https://i.ytimg.com/vi/" + r.Resource + "/hqdefault.jpg"
	case "x":
		p, err := fetchXPostPreview(ctx, r.Resource, metadata)
		if err != nil {
			return result, err
		}
		result.Author, result.Text = p.Author, p.Text
	case "web":
		u, err := pageURL(r.Resource)
		if err != nil {
			return result, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return result, err
		}
		resp, err := metadata(req)
		if err != nil {
			return result, err
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			return result, errUnavailable
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 256*1024+1))
		if err != nil || len(body) > 256*1024 {
			return result, errUnavailable
		}
		p := publicPagePreview(bytes.NewReader(body))
		result.Title, result.Author, result.Description = limitPreviewText(p.Title, 200), limitPreviewText(p.Author, 120), limitPreviewText(p.Description, 200)
		if imageURL, err := url.Parse(p.Thumbnail); err == nil && p.Thumbnail != "" {
			thumbnail = resp.Request.URL.ResolveReference(imageURL).String()
		}
	default:
		p, err := fetchVideoPortalPreview(ctx, r.Provider, r.Resource, metadata)
		if err != nil {
			return result, err
		}
		result.Title, result.Author, result.Description = p.Title, p.Author, p.Description
		result.LiveState, result.Category, result.StartsAt = p.LiveState, p.Category, p.StartsAt
		thumbnail = p.Thumbnail
	}
	if thumbnail != "" {
		result.Thumbnail, _ = s.image(ctx, thumbnail)
	}
	return result, nil
}
func (s *Service) metadataRequest(req *http.Request, client *http.Client) (*http.Response, error) {
	if _, err := pageURL(req.URL.String()); err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "noXaLinkPreview/1.0")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	kind, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if kind != "text/html" && kind != "application/json" && kind != "application/ld+json" {
		_ = resp.Body.Close()
		return nil, errUnavailable
	}
	return resp, nil
}
func (s *Service) image(ctx context.Context, value string) (string, error) {
	u, err := pageURL(value)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	kind, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if resp.StatusCode != http.StatusOK || (kind != "image/jpeg" && kind != "image/png" && kind != "image/gif" && kind != "image/webp") {
		return "", errUnavailable
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024+1))
	if err != nil || len(data) > 2*1024*1024 {
		return "", errUnavailable
	}
	return raster(data)
}
func raster(data []byte) (string, error) {
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || config.Width <= 0 || config.Height <= 0 || config.Width > 4096 || config.Height > 4096 || int64(config.Width)*int64(config.Height) > 4*1024*1024 {
		return "", errUnavailable
	}
	if format != "jpeg" && format != "png" && format != "gif" && format != "webp" {
		return "", errUnavailable
	}
	decoded, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return "", errUnavailable
	}
	width, height := config.Width, config.Height
	if width > 640 || height > 640 {
		if width >= height {
			height = max(1, height*640/width)
			width = 640
		} else {
			width = max(1, width*640/height)
			height = 640
		}
	}
	dest := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.BiLinear.Scale(dest, dest.Bounds(), decoded, decoded.Bounds(), draw.Src, nil)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, dest, &jpeg.Options{Quality: 75}); err != nil {
		return "", err
	}
	if buf.Len() > 200*1024 {
		return "", fmt.Errorf("thumbnail too large")
	}
	return "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(buf.Bytes()), nil
}
