package webpush

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"syscall"
	"time"
)

// errDestinationNotPublic rejects a push delivery whose endpoint resolves to
// a non-public address. Subscription endpoints are chosen by linked devices,
// so delivery must not become a server-origin request into internal networks.
var errDestinationNotPublic = errors.New("webpush: push endpoint address is not public")

var errRedirect = errors.New("webpush: push service redirects are not followed")

var nonPublicPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("2001::/23"), netip.MustParsePrefix("2001:db8::/32"), netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("3fff::/20"),
}

func publicAddr(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	if ip.Is6() && !netip.MustParsePrefix("2000::/3").Contains(ip) {
		return false
	}
	for _, prefix := range nonPublicPrefixes {
		if prefix.Contains(ip) {
			return false
		}
	}
	return true
}

// publicOnlyControl runs after DNS resolution, on the exact address being
// connected, so a rebinding resolver cannot redirect delivery inward.
func publicOnlyControl(_, address string, _ syscall.RawConn) error {
	ap, err := netip.ParseAddrPort(address)
	if err != nil || !publicAddr(ap.Addr()) {
		return errDestinationNotPublic
	}
	return nil
}

// noRedirects keeps delivery on the validated endpoint. Push services answer
// directly; a redirect could otherwise move the request to another scheme or
// host.
func noRedirects(*http.Request, []*http.Request) error { return errRedirect }

// proxyTargetAllowed screens the destination of a proxied delivery. The
// egress proxy resolves and connects on our behalf, so the dial-time address
// check cannot see the endpoint; reject what is non-public without DNS
// (loopback names and non-public IP literals). Names that resolve inward are
// left to the proxy's own destination policy.
func proxyTargetAllowed(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if host == "" || host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return false
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		return publicAddr(ip)
	}
	return true
}

// proxyDialAddr mirrors net/http's canonical proxy address (host:port with
// the scheme's default port), which is what the transport dials.
func proxyDialAddr(u *url.URL) string {
	port := u.Port()
	if port == "" {
		switch u.Scheme {
		case "https":
			port = "443"
		case "socks5", "socks5h":
			port = "1080"
		default:
			port = "80"
		}
	}
	return strings.ToLower(net.JoinHostPort(u.Hostname(), port))
}

func newDeliveryClient(proxy func(*http.Request) (*url.URL, error)) *http.Client {
	direct := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	guarded := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second, Control: publicOnlyControl}
	// Only connections to an operator-configured egress proxy skip the
	// address check; every other dial (including NO_PROXY destinations when
	// a proxy is configured) stays public-only.
	var proxies sync.Map
	return &http.Client{
		Timeout:       15 * time.Second,
		CheckRedirect: noRedirects,
		Transport: &http.Transport{
			Proxy: func(req *http.Request) (*url.URL, error) {
				p, err := proxy(req)
				if err != nil || p == nil {
					return p, err
				}
				if !proxyTargetAllowed(req.URL.Hostname()) {
					return nil, errDestinationNotPublic
				}
				proxies.Store(proxyDialAddr(p), struct{}{})
				return p, nil
			},
			DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
				if _, ok := proxies.Load(strings.ToLower(address)); ok {
					return direct.DialContext(ctx, network, address)
				}
				return guarded.DialContext(ctx, network, address)
			},
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          32,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 10 * time.Second,
		},
	}
}

var defaultDeliveryClient = sync.OnceValue(func() *http.Client {
	return newDeliveryClient(http.ProxyFromEnvironment)
})
