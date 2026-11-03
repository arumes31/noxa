package webpush

import (
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
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

func newDeliveryClient(proxy func(*http.Request) (*url.URL, error)) *http.Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	// With an operator-configured egress proxy the proxy performs the outbound
	// connection; only direct connections are address-checked here.
	if p, err := proxy(&http.Request{URL: &url.URL{Scheme: "https", Host: "push.example"}}); err != nil || p == nil {
		dialer.Control = publicOnlyControl
	}
	return &http.Client{
		Timeout:       15 * time.Second,
		CheckRedirect: noRedirects,
		Transport: &http.Transport{
			Proxy:                 proxy,
			DialContext:           dialer.DialContext,
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
