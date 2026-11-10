package webpush

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"testing"
)

func postRequest(t *testing.T, target string) *http.Request {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	return req
}

func noProxy(*http.Request) (*url.URL, error) { return nil, nil }

func TestDeliveryClientRejectsNonPublicDestinations(t *testing.T) {
	internal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("delivery reached a loopback service")
	}))
	defer internal.Close()
	resp, err := newDeliveryClient(noProxy).Do(postRequest(t, internal.URL))
	if err == nil {
		_ = resp.Body.Close()
	}
	if !errors.Is(err, errDestinationNotPublic) {
		t.Fatalf("loopback delivery err=%v", err)
	}
}

func TestDeliveryClientDoesNotFollowRedirects(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("redirect was followed")
	}))
	defer target.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer redirector.Close()
	client := newDeliveryClient(noProxy)
	// Exercise only the redirect policy: the loopback test servers would
	// otherwise be refused by the address check.
	client.Transport = http.DefaultTransport
	resp, err := client.Do(postRequest(t, redirector.URL))
	if err == nil {
		_ = resp.Body.Close()
	}
	if !errors.Is(err, errRedirect) {
		t.Fatalf("redirect err=%v", err)
	}
}

func TestPublicAddr(t *testing.T) {
	for addr, want := range map[string]bool{
		"8.8.8.8": true, "2607:f8b0::1": true,
		"127.0.0.1": false, "10.1.2.3": false, "169.254.169.254": false, "100.64.0.1": false,
		"::1": false, "fd00::1": false, "fe80::1": false, "::ffff:127.0.0.1": false, "0.0.0.0": false,
	} {
		if got := publicAddr(netip.MustParseAddr(addr)); got != want {
			t.Errorf("publicAddr(%s)=%v, want %v", addr, got, want)
		}
	}
}

// TestDeliveryClientGuardsDestinationsWithEgressProxy: a configured proxy
// must not disable the public-address check for destinations the proxy
// function routes directly (NO_PROXY), nor accept non-public literals.
func TestDeliveryClientGuardsDestinationsWithEgressProxy(t *testing.T) {
	var proxied []string
	proxyHit := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxied = append(proxied, r.Host)
		w.WriteHeader(http.StatusCreated)
	}))
	defer proxyHit.Close()
	proxyURL, err := url.Parse(proxyHit.URL)
	if err != nil {
		t.Fatal(err)
	}
	internal := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("delivery reached a loopback service directly")
	}))
	defer internal.Close()
	internalURL, err := url.Parse(internal.URL)
	if err != nil {
		t.Fatal(err)
	}
	// Proxy everything except the internal host (a NO_PROXY entry).
	proxyFn := func(req *http.Request) (*url.URL, error) {
		if req.URL.Host == internalURL.Host {
			return nil, nil
		}
		return proxyURL, nil
	}
	client := newDeliveryClient(proxyFn)

	resp, err := client.Do(postRequest(t, internal.URL))
	if err == nil {
		_ = resp.Body.Close()
	}
	if !errors.Is(err, errDestinationNotPublic) {
		t.Fatalf("direct loopback delivery with proxy configured err=%v", err)
	}

	for _, target := range []string{"http://127.0.0.1:9/x", "http://[::1]/x", "http://169.254.169.254/x", "http://localhost/x", "http://a.localhost./x"} {
		resp, err := client.Do(postRequest(t, target))
		if err == nil {
			_ = resp.Body.Close()
		}
		if !errors.Is(err, errDestinationNotPublic) {
			t.Fatalf("proxied %s err=%v", target, err)
		}
	}

	// A public name still goes through the (loopback) proxy.
	resp, err = client.Do(postRequest(t, "http://push.example/send"))
	if err != nil {
		t.Fatalf("proxied public delivery err=%v", err)
	}
	_ = resp.Body.Close()
	if len(proxied) != 1 || proxied[0] != "push.example" {
		t.Fatalf("proxy saw %v", proxied)
	}
}

func TestProxyDialAddr(t *testing.T) {
	for raw, want := range map[string]string{
		"http://Proxy.Internal": "proxy.internal:80", "https://p:8443": "p:8443",
		"socks5://10.0.0.5": "10.0.0.5:1080", "http://[fd00::1]:3128": "[fd00::1]:3128",
	} {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		if got := proxyDialAddr(u); got != want {
			t.Errorf("proxyDialAddr(%s)=%s, want %s", raw, got, want)
		}
	}
}
