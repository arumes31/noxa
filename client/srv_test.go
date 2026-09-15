package main

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
	"time"

	"noxa/internal/tlscert"
)

func TestResolveServerAddresses(t *testing.T) {
	for _, test := range []struct {
		name, input, wantHost, wantTrust string
		records                          []*net.SRV
		lookupErr                        error
		want                             []string
		wantErr                          bool
	}{
		{name: "SRV target and custom port", input: "voice.example", wantHost: "voice.example", wantTrust: "voice.example:12333", records: []*net.SRV{{Target: "node.example.", Port: 23456}}, want: []string{"node.example:23456"}},
		{name: "resolver priority order", input: "voice.example", wantHost: "voice.example", wantTrust: "voice.example:12333", records: []*net.SRV{{Target: "first.example.", Port: 23456, Priority: 0}, {Target: "backup.example.", Port: 34567, Priority: 10}}, want: []string{"first.example:23456", "backup.example:34567"}},
		{name: "explicit port bypasses SRV", input: "voice.example:12333", wantTrust: "voice.example:12333", want: []string{"voice.example:12333"}},
		{name: "IPv4 default", input: "127.0.0.1", wantTrust: "127.0.0.1:12333", want: []string{"127.0.0.1:12333"}},
		{name: "IPv6 default", input: "2001:db8::1", wantTrust: "[2001:db8::1]:12333", want: []string{"[2001:db8::1]:12333"}},
		{name: "bracketed IPv6", input: "[::1]", wantTrust: "[::1]:12333", want: []string{"[::1]:12333"}},
		{name: "unclosed IPv6 bracket", input: "[::1", wantErr: true},
		{name: "unopened IPv6 bracket", input: "::1]", wantErr: true},
		{name: "scoped IPv6", input: "fe80::1%eth0", wantTrust: "[fe80::1%eth0]:12333", want: []string{"[fe80::1%eth0]:12333"}},
		{name: "IPv6 explicit port", input: "[::1]:23456", wantTrust: "[::1]:23456", want: []string{"[::1]:23456"}},
		{name: "no SRV", input: " voice.example ", wantHost: "voice.example", wantTrust: "voice.example:12333", want: []string{"voice.example:12333"}},
		{name: "NXDOMAIN", input: "voice.example", wantHost: "voice.example", wantTrust: "voice.example:12333", lookupErr: &net.DNSError{IsNotFound: true}, want: []string{"voice.example:12333"}},
		{name: "DNS failure", input: "voice.example", wantHost: "voice.example", lookupErr: errors.New("DNS failure"), wantErr: true},
		{name: "DNS cancellation", input: "voice.example", wantHost: "voice.example", lookupErr: context.Canceled, wantErr: true},
		{name: "unavailable service", input: "voice.example", wantHost: "voice.example", records: []*net.SRV{{Target: "."}}, wantErr: true},
		{name: "invalid SRV port", input: "voice.example", wantHost: "voice.example", records: []*net.SRV{{Target: "node.example."}}, wantErr: true},
		{name: "empty SRV target", input: "voice.example", wantHost: "voice.example", records: []*net.SRV{{Port: 23456}}, wantErr: true},
		{name: "empty address", input: " ", wantErr: true},
		{name: "empty port", input: "voice.example:", wantErr: true},
		{name: "bad port", input: "voice.example:65536", wantErr: true},
		{name: "URL is not a hostname", input: "https://voice.example", wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			called := false
			lookup := func(ctx context.Context, service, proto, host string) (string, []*net.SRV, error) {
				called = true
				if service != "noxa" || proto != "tcp" || host != test.wantHost {
					t.Fatalf("lookup(%q, %q, %q)", service, proto, host)
				}
				if _, ok := ctx.Deadline(); !ok {
					t.Fatal("DNS lookup needs a deadline")
				}
				return "", test.records, test.lookupErr
			}
			got, trust, err := resolveServerAddresses(t.Context(), test.input, lookup)
			if (err != nil) != test.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, test.wantErr)
			}
			if !test.wantErr && (!reflect.DeepEqual(got, test.want) || trust != test.wantTrust) {
				t.Fatalf("got %v, trust %q; want %v, trust %q", got, trust, test.want, test.wantTrust)
			}
			if called != (test.wantHost != "") {
				t.Fatalf("lookup called = %v", called)
			}
		})
	}
}

func TestSRVDialTransportFailoverAndCertificatePin(t *testing.T) {
	startServer := func() (*net.SRV, string) {
		t.Helper()
		cert, fingerprint, err := tlscert.Ensure(t.TempDir(), "", "", nil)
		if err != nil {
			t.Fatal(err)
		}
		listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
			Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13,
		})
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan struct{})
		go func() {
			defer close(done)
			for {
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
				_ = conn.(*tls.Conn).HandshakeContext(t.Context())
				_ = conn.Close()
			}
		}()
		t.Cleanup(func() { _ = listener.Close(); <-done })
		host, port, err := net.SplitHostPort(listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		portNumber, err := strconv.ParseUint(port, 10, 16)
		if err != nil {
			t.Fatal(err)
		}
		return &net.SRV{Target: host, Port: uint16(portNumber)}, fingerprint
	}
	first, firstFingerprint := startServer()
	second, secondFingerprint := startServer()
	// Reserve then close a local port to exercise connection-refused failover.
	closed, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closedPort := closed.Addr().(*net.TCPAddr).Port
	_ = closed.Close()
	records := []*net.SRV{{Target: "127.0.0.1", Port: uint16(closedPort)}, first}
	manager := newConnManager(context.Background())
	manager.knownServers = loadKnownServersAt(filepath.Join(t.TempDir(), "known_servers.json"))
	manager.lookupSRV = func(context.Context, string, string, string) (string, []*net.SRV, error) { return "", records, nil }
	conn, err := manager.dialTransport("voice.example")
	if err != nil {
		t.Fatalf("SRV failover: %v", err)
	}
	_ = conn.Close()
	if status, err := manager.knownServers.verify("voice.example:12333", firstFingerprint); err != nil || status != trustOK {
		t.Fatalf("original hostname pin = %v, %v", status, err)
	}
	// A DNS change must fail against the original pin, even with a valid backup.
	records = []*net.SRV{second, first}
	conn, err = manager.dialTransport("voice.example")
	if conn != nil {
		_ = conn.Close()
		t.Fatal("DNS change bypassed certificate pin")
	}
	if !errors.Is(err, errFingerprintMismatch) {
		t.Fatalf("DNS change error = %v", err)
	}
	app := &App{knownServers: manager.knownServers}
	if err := app.TrustServerFingerprint("voice.example", secondFingerprint); err != "" {
		t.Fatal(err)
	}
	conn, err = manager.dialTransport("voice.example")
	if err != nil {
		t.Fatalf("explicitly approved replacement: %v", err)
	}
	_ = conn.Close()
}
