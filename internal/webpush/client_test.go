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
