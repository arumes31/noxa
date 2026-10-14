package main

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func withUpdateAPIBase(t *testing.T, base string) {
	t.Helper()
	previous := updateAPIBase
	updateAPIBase = base
	t.Cleanup(func() { updateAPIBase = previous })
}

func TestUpdateURLLocalTrustRequiresConfiguredOrigin(t *testing.T) {
	tests := []struct {
		name, base, target string
		allowed            bool
	}{
		{"production localhost", "https://api.github.com", "https://localhost:8443/update", false},
		{"production IPv4", "https://api.github.com", "http://127.0.0.1:8080/update", false},
		{"production IPv6", "https://api.github.com", "https://[::1]/update", false},
		{"production mapped IPv4", "https://api.github.com", "https://[::ffff:127.0.0.1]/update", false},
		{"same owned origin", "http://127.0.0.1:8080/api", "http://127.0.0.1:8080/asset", true},
		{"same default port normalized", "http://LOCALHOST.:80/api", "http://localhost/asset", true},
		{"same HTTPS default port", "https://localhost/api", "https://LOCALHOST.:443/asset", true},
		{"same canonical IPv6", "http://[0:0:0:0:0:0:0:1]:8080/api", "http://[::1]:8080/asset", true},
		{"other local port", "http://localhost:8080", "http://localhost:8081/asset", false},
		{"HTTPS origin downgrade", "https://localhost:8443", "http://localhost:8443/asset", false},
		{"HTTP origin other scheme", "http://localhost:8080", "https://localhost:8080/asset", false},
		{"other loopback host", "http://127.0.0.1:8080", "http://127.0.0.2:8080/asset", false},
		{"DNS alias is another origin", "http://127.0.0.1:8080", "http://localhost:8080/asset", false},
		{"arbitrary configured host", "https://updates.example.com", "https://updates.example.com/asset", false},
		{"private configured host", "https://10.0.0.1", "https://10.0.0.1/asset", false},
		{"credentialed API origin", "http://user@localhost:8080", "http://localhost:8080/asset", false},
		{"wrong API scheme", "ftp://localhost:8080", "http://localhost:8080/asset", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			withUpdateAPIBase(t, tc.base)
			_, err := parseUpdateURL(tc.target)
			if (err == nil) != tc.allowed {
				t.Fatalf("target=%q configured=%q allowed=%v error=%v", tc.target, tc.base, tc.allowed, err)
			}
		})
	}
}

type updatePolicyRoundTripper func(*http.Request) (*http.Response, error)

func (f updatePolicyRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestUpdateRedirectChecksDestinationBeforeRequest(t *testing.T) {
	for _, tc := range []struct {
		name, target string
		allowed      bool
	}{
		{"localhost", "https://localhost:8443/private", false},
		{"loopback", "http://127.0.0.1:8080/private", false},
		{"IPv6 loopback", "https://[::1]/private", false},
		{"private address", "https://10.0.0.1/private", false},
		{"link local metadata", "https://169.254.169.254/latest/meta-data", false},
		{"untrusted public host", "https://updates.example.com/asset", false},
		{"GitHub CDN", "https://release-assets.githubusercontent.com/github-production-release-asset/asset.exe", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withUpdateAPIBase(t, "https://api.github.com")
			client := *updateHTTPClient
			var requests []string
			client.Transport = updatePolicyRoundTripper(func(req *http.Request) (*http.Response, error) {
				requests = append(requests, req.URL.String())
				header := make(http.Header)
				status := http.StatusOK
				if len(requests) == 1 {
					header.Set("Location", tc.target)
					status = http.StatusFound
				}
				return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(strings.NewReader("fixture")), Request: req}, nil
			})
			target, err := parseUpdateURL("https://github.com/o/r/releases/download/v1/app.exe")
			if err != nil {
				t.Fatal(err)
			}
			req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, target.String(), nil)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := client.Do(req)
			if resp != nil {
				_ = resp.Body.Close()
			}
			if tc.allowed {
				if err != nil || len(requests) != 2 || requests[1] != tc.target {
					t.Fatalf("trusted redirect failed: requests=%v err=%v", requests, err)
				}
			} else if err == nil || len(requests) != 1 {
				t.Fatalf("redirect contacted forbidden destination: requests=%v err=%v", requests, err)
			}
		})
	}
}
