package tlscert

import (
	"crypto/tls"
	"crypto/x509"
	"net"
	"path/filepath"
	"testing"
)

// handshake runs one TLS 1.3 handshake over a pipe and returns the client's
// view of the negotiated key exchange.
func handshake(t *testing.T, server, client *tls.Config) tls.CurveID {
	t.Helper()
	serverConn, clientConn := net.Pipe()
	defer func() { _ = serverConn.Close() }()
	defer func() { _ = clientConn.Close() }()
	errc := make(chan error, 1)
	go func() { errc <- tls.Server(serverConn, server).HandshakeContext(t.Context()) }()
	c := tls.Client(clientConn, client)
	if err := c.HandshakeContext(t.Context()); err != nil {
		t.Fatalf("client handshake: %v", err)
	}
	if err := <-errc; err != nil {
		t.Fatalf("server handshake: %v", err)
	}
	return c.ConnectionState().CurveID
}

func TestCurvePreferencesNegotiateHybridPostQuantum(t *testing.T) {
	t.Setenv("GODEBUG", "tlsmlkem=0") // the pinned list must not depend on defaults
	cert, _, err := Ensure(filepath.Join(t.TempDir(), "tls"), "", "", []string{"noxa.test"})
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	roots.AddCert(leaf)
	server := &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13, CurvePreferences: CurvePreferences()}
	client := &tls.Config{RootCAs: roots, ServerName: "noxa.test", MinVersion: tls.VersionTLS13, CurvePreferences: CurvePreferences()}
	if got := handshake(t, server, client); got != tls.X25519MLKEM768 {
		t.Fatalf("negotiated %v, want X25519MLKEM768", got)
	}

	classical := client.Clone()
	classical.CurvePreferences = []tls.CurveID{tls.X25519}
	if got := handshake(t, server, classical); got != tls.X25519 {
		t.Fatalf("classical peer negotiated %v, want X25519 fallback", got)
	}
}
