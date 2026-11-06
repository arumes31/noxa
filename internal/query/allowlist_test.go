package query

import (
	"context"
	"io"
	"net"
	"net/netip"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

type addrConn struct {
	net.Conn
	remote net.Addr
}

func (c addrConn) RemoteAddr() net.Addr { return c.remote }

func TestSourceAllowedMatchesPrefixes(t *testing.T) {
	srv := New("", nil, newFakeBackend())
	conn := func(addr string) net.Conn {
		return addrConn{remote: net.TCPAddrFromAddrPort(netip.MustParseAddrPort(addr))}
	}
	if !srv.sourceAllowed(conn("203.0.113.9:4000")) {
		t.Fatal("empty allowlist must allow every source")
	}

	srv.AllowedPrefixes = []netip.Prefix{
		netip.MustParsePrefix("10.0.0.0/8"),
		netip.MustParsePrefix("2001:db8::/32"),
	}
	cases := map[string]bool{
		"10.1.2.3:4000":           true,
		"[::ffff:10.1.2.3]:4000":  true, // IPv4-mapped IPv6 is unmapped first
		"[2001:db8::7]:4000":      true,
		"192.168.1.1:4000":        false,
		"127.0.0.1:4000":          false,
		"[2001:db9::1]:4000":      false,
		"[fe80::1%eth0]:4000":     false,
		"[::ffff:192.0.2.1]:4000": false,
	}
	for addr, want := range cases {
		if got := srv.sourceAllowed(conn(addr)); got != want {
			t.Errorf("sourceAllowed(%s) = %v, want %v", addr, got, want)
		}
	}
}

// TestQueryAllowlistRefusesSilently verifies a refused source is closed
// before the banner and does not occupy a connection slot.
func TestQueryAllowlistRefusesSilently(t *testing.T) {
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	srv := New(addr, nil, newFakeBackend())
	srv.AllowedPrefixes = []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")}
	srv.MaxConns = 1
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		_ = srv.Close()
		<-errCh
	})

	for range 3 {
		conn := dialWhenListening(t, addr)
		if err := conn.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(conn) // EOF or reset: either way no bytes may arrive
		_ = conn.Close()
		if len(data) != 0 {
			t.Fatalf("refused source received %q, want no bytes", data)
		}
	}
	srv.mu.Lock()
	active := len(srv.conns)
	srv.mu.Unlock()
	if active != 0 {
		t.Fatalf("refused sources left %d registered connections", active)
	}
}

func dialWhenListening(t *testing.T, addr string) net.Conn {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := (&net.Dialer{}).DialContext(t.Context(), "tcp", addr)
		if err == nil {
			return conn
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("query listener never became reachable")
	return nil
}

func TestQueryAllowlistAdmitsListedSource(t *testing.T) {
	addr, _ := startQueryServerWith(t, newFakeBackend(), func(s *Server) {
		s.AllowedPrefixes = []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}
	})
	conn, _ := dialQuery(t, addr) // fails unless both banner lines arrive
	_ = conn.Close()
}

func TestSSHQueryAllowlistRefusesSource(t *testing.T) {
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	base := New("", nil, newFakeBackend())
	base.AllowedPrefixes = []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")}
	srv := NewSSH(addr, filepath.Join(t.TempDir(), "host.key"), base)
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		_ = srv.Close()
		<-errCh
	})

	cfg := &ssh.ClientConfig{
		User:            "admin-uid",
		Auth:            []ssh.AuthMethod{ssh.Password("pw")},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), //nolint:gosec // test-only ephemeral host key
		Timeout:         2 * time.Second,
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		client, err := ssh.Dial("tcp", addr, cfg)
		if err == nil {
			_ = client.Close()
			t.Fatal("SSH handshake succeeded from a source outside query_allowed_cidrs")
		}
		var opErr *net.OpError
		if !asOpError(err, &opErr) {
			return // listener reachable; handshake refused as expected
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("SSH query listener never became reachable")
}

// asOpError reports whether err is a dial-level failure (listener not up yet).
func asOpError(err error, target **net.OpError) bool {
	op, ok := err.(*net.OpError)
	if ok && op.Op == "dial" {
		*target = op
		return true
	}
	return false
}
