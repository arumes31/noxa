package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"strings"
	"testing"
	"time"

	"voicx/internal/netproto"
	"voicx/internal/tlscert"
)

func TestControlTLSConfigRestrictsInsecureMode(t *testing.T) {
	if _, _, err := controlTLSConfig("192.0.2.1:12333", controlTLSMode{insecure: true}); err == nil {
		t.Fatal("remote -tls-insecure address accepted")
	}

	cfg, enabled, err := controlTLSConfig("127.0.0.1:12333", controlTLSMode{insecure: true})
	if err != nil {
		t.Fatalf("loopback -tls-insecure: %v", err)
	}
	if !enabled || !cfg.InsecureSkipVerify || cfg.MinVersion != tls.VersionTLS13 {
		t.Fatalf("unexpected loopback TLS config: %+v", cfg)
	}
}

func TestPinnedTLSConfigVerifiesExactCertificate(t *testing.T) {
	cert, fingerprint, err := tlscert.Ensure(t.TempDir(), "", "", []string{"localhost"})
	if err != nil {
		t.Fatalf("generate certificate: %v", err)
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatalf("parse certificate: %v", err)
	}
	state := tls.ConnectionState{PeerCertificates: []*x509.Certificate{leaf}}

	cfg, err := fingerprintVerifiedTLSConfig(fingerprint)
	if err != nil {
		t.Fatalf("fingerprintVerifiedTLSConfig: %v", err)
	}
	if err := cfg.VerifyConnection(state); err != nil {
		t.Fatalf("matching certificate rejected: %v", err)
	}

	wrong := strings.Repeat("00:", 31) + "00"
	cfg, err = fingerprintVerifiedTLSConfig(wrong)
	if err != nil {
		t.Fatalf("wrong pin syntax: %v", err)
	}
	if err := cfg.VerifyConnection(state); err == nil {
		t.Fatal("mismatched certificate accepted")
	}
}

func TestPinnedTLSHandshake(t *testing.T) {
	cert, fingerprint, err := tlscert.Ensure(t.TempDir(), "", "", []string{"localhost"})
	if err != nil {
		t.Fatalf("generate certificate: %v", err)
	}

	for _, tc := range []struct {
		name      string
		pin       string
		wantError bool
	}{
		{name: "matching pin", pin: fingerprint},
		{name: "wrong pin", pin: strings.Repeat("00", 32), wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := fingerprintVerifiedTLSConfig(tc.pin)
			if err != nil {
				t.Fatalf("TLS config: %v", err)
			}
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatalf("listen: %v", err)
			}
			t.Cleanup(func() { _ = listener.Close() })
			clientConn, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(t.Context(), "tcp", listener.Addr().String())
			if err != nil {
				t.Fatalf("dial: %v", err)
			}
			t.Cleanup(func() { _ = clientConn.Close() })
			serverConn, err := listener.Accept()
			if err != nil {
				t.Fatalf("accept: %v", err)
			}
			t.Cleanup(func() {
				_ = serverConn.Close()
			})
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			serverTLS := tls.Server(serverConn, &tls.Config{
				Certificates: []tls.Certificate{cert},
				MinVersion:   tls.VersionTLS13,
			})
			serverResult := make(chan error, 1)
			go func() { serverResult <- serverTLS.HandshakeContext(ctx) }()
			clientTLS := tls.Client(clientConn, cfg)
			err = clientTLS.HandshakeContext(ctx)
			if tc.wantError {
				if err == nil || !strings.Contains(err.Error(), "TLS fingerprint =") {
					t.Fatalf("mismatched pin handshake error = %v", err)
				}
			} else if err != nil {
				t.Fatalf("matching pin handshake: %v", err)
			}
			if serverErr := <-serverResult; !tc.wantError && serverErr != nil {
				t.Fatalf("server handshake: %v", serverErr)
			}
		})
	}
}

func TestDialFileTransferRequiresConsistentTLSMetadata(t *testing.T) {
	if _, err := dialFileTransfer("127.0.0.1:1", netproto.FileTransferInitResponse{TLS: true}); err == nil {
		t.Fatal("TLS file-transfer response without fingerprint accepted")
	}
	if _, err := dialFileTransfer("127.0.0.1:1", netproto.FileTransferInitResponse{
		TLSFingerprint: strings.Repeat("00:", 31) + "00",
	}); err == nil {
		t.Fatal("plaintext file-transfer response with fingerprint accepted")
	}
}
