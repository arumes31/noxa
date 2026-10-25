package health

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Exercise actual listeners rather than relying on sleeps or inspecting fields.
func TestListenerLifecycle(t *testing.T) {
	for _, secure := range []bool{false, true} {
		name := "http"
		if secure {
			name = "https"
		}
		t.Run(name, func(t *testing.T) {
			srv := New("127.0.0.1:0", nil, nil)
			client := &http.Client{Timeout: 3 * time.Second}
			if secure {
				fixture := httptest.NewTLSServer(http.NotFoundHandler())
				cert := fixture.TLS.Certificates[0]
				roots := x509.NewCertPool()
				roots.AddCert(fixture.Certificate())
				fixture.Close()
				srv.SetTLSCertificate(cert)
				transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}
				client.Transport = transport
				t.Cleanup(transport.CloseIdleConnections)
			}
			listening := make(chan string, 1)
			srv.srv.BaseContext = func(listener net.Listener) context.Context {
				listening <- listener.Addr().String()
				return t.Context()
			}
			done := make(chan error, 1)
			go func() { done <- srv.Start() }()
			t.Cleanup(func() { _ = srv.srv.Close() })
			var addr string
			select {
			case addr = <-listening:
			case err := <-done:
				t.Fatalf("listener exited: %v", err)
			case <-time.After(3 * time.Second):
				t.Fatal("listener did not start")
			}
			for _, endpoint := range []string{"/healthz", "/readyz"} {
				request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, name+"://"+addr+endpoint, nil)
				if err != nil {
					t.Fatal(err)
				}
				response, err := client.Do(request)
				if err != nil {
					t.Fatal(err)
				}
				_ = response.Body.Close()
				if response.StatusCode != http.StatusOK {
					t.Fatalf("%s: status %d", endpoint, response.StatusCode)
				}
				if secure && response.TLS == nil {
					t.Fatal("HTTPS request did not negotiate TLS")
				}
			}
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			if err := srv.Shutdown(ctx); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal("Start did not return after shutdown")
			}
		})
	}
}

func TestListenerReportsBindFailure(t *testing.T) {
	var config net.ListenConfig
	occupied, err := config.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = occupied.Close() }()
	srv := New(occupied.Addr().String(), nil, nil)
	done := make(chan error, 1)
	go func() { done <- srv.Start() }()
	t.Cleanup(func() { _ = srv.srv.Close() })
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("occupied address accepted")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("bind failure did not return")
	}
}
