package turn

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"io"
	"math/big"
	"net"
	"sync"
	"testing"
	"time"

	proxyproto "github.com/pires/go-proxyproto"
)

// This fixture models NPM's TLS termination plus PROXY protocol. Go clients
// verify the generated certificate with an explicit test trust root.
func testTLSProxy(t *testing.T, backend, bindIP string) (net.Listener, *tls.Config, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), DNSNames: []string{"localhost"}, IPAddresses: []net.IP{net.ParseIP(bindIP)}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IsCA: true, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(leaf)
	raw, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp4", net.JoinHostPort(bindIP, "0"))
	if err != nil {
		t.Fatal(err)
	}
	listener := tls.NewListener(raw, &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}})
	var mu sync.Mutex
	active := make(map[net.Conn]bool)
	stopped := false
	t.Cleanup(func() {
		_ = listener.Close()
		mu.Lock()
		stopped = true
		for c := range active {
			_ = c.Close()
		}
		mu.Unlock()
	})
	go func() {
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			mu.Lock()
			if stopped {
				mu.Unlock()
				_ = conn.Close()
				return
			}
			active[conn] = true
			mu.Unlock()
			go func() {
				defer func() { _ = conn.Close(); mu.Lock(); delete(active, conn); mu.Unlock() }()
				secure, ok := conn.(*tls.Conn)
				if !ok {
					return
				}
				_ = secure.SetDeadline(time.Now().Add(3 * time.Second))
				if secure.HandshakeContext(t.Context()) != nil {
					return
				}
				_ = secure.SetDeadline(time.Time{})
				upstream, e := (&net.Dialer{Timeout: time.Second}).DialContext(t.Context(), "tcp4", backend)
				if e != nil {
					return
				}
				defer func() { _ = upstream.Close() }()
				header := proxyproto.HeaderProxyFromAddrs(1, conn.RemoteAddr(), conn.LocalAddr())
				if _, e = header.WriteTo(upstream); e != nil {
					return
				}
				done := make(chan struct{})
				go func() { _, _ = io.Copy(upstream, conn); _ = upstream.Close(); _ = conn.Close(); close(done) }()
				_, _ = io.Copy(conn, upstream)
				_ = upstream.Close()
				_ = conn.Close()
				<-done
			}()
		}
	}()
	pin := sha256.Sum256(leaf.RawSubjectPublicKeyInfo)
	return listener, &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots, ServerName: "localhost"}, base64.StdEncoding.EncodeToString(pin[:])
}

func TestTLSProxyRejectsInvalidCertificates(t *testing.T) {
	backend := testProxyListener(t, []string{"127.0.0.1"})
	proxy, valid, _ := testTLSProxy(t, backend.Addr().String(), "127.0.0.1")
	for _, name := range []string{"untrusted", "wrong hostname", "expired"} {
		t.Run(name, func(t *testing.T) {
			cfg := valid.Clone()
			switch name {
			case "untrusted":
				cfg.RootCAs = x509.NewCertPool()
			case "wrong hostname":
				cfg.ServerName = "wrong.example"
			case "expired":
				cfg.Time = func() time.Time { return time.Now().Add(2 * time.Hour) }
			}
			conn, err := (&tls.Dialer{NetDialer: &net.Dialer{Timeout: time.Second}, Config: cfg}).DialContext(t.Context(), "tcp4", proxy.Addr().String())
			if err == nil {
				_ = conn.Close()
				t.Fatal("invalid certificate accepted")
			}
		})
	}
}
