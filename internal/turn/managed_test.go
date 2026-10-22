package turn

import (
	"context"
	"crypto/tls"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/pion/logging"
	pion "github.com/pion/turn/v5"
)

func TestLeaseRotationRevokesAllGenerations(t *testing.T) {
	m := NewManager("test", 32, 1<<20)
	now := time.Now()
	m.now = func() time.Time { return now }
	m.Register("device-one", "account")
	m.Register("device-two", "account")
	m.Authorize("device-one", true)
	m.Authorize("device-two", true)
	first, err := m.Issue("device-one")
	if err != nil {
		t.Fatal(err)
	}
	same, _ := m.Issue("device-one")
	if same != first {
		t.Fatal("non-idempotent issuance")
	}
	if first.ExpiresAt-first.IssuedAt != int64(time.Hour/time.Millisecond) {
		t.Fatal("invalid lifetime")
	}
	now = now.Add(55 * time.Minute)
	m.Authorize("device-one", true)
	m.Authorize("device-two", true)
	second, err := m.Issue("device-one")
	if err != nil {
		t.Fatal(err)
	}
	other, _ := m.Issue("device-two")
	if first.Username == second.Username || second.Generation != first.Generation+1 {
		t.Fatal("generation not rotated")
	}
	auth := func(l Lease) bool {
		_, _, ok := m.Authenticate(&pion.RequestAttributes{Username: l.Username, Realm: "test"})
		return ok
	}
	if !auth(first) || !auth(second) {
		t.Fatal("overlap missing")
	}
	m.Revoke("device-one")
	m.Authorize("device-one", true)
	if auth(first) || auth(second) || !auth(other) {
		t.Fatal("revocation isolation failed")
	}
	if _, err := m.Issue("device-one"); err == nil {
		t.Fatal("revoked session renewed")
	}
	now = now.Add(AuthorizationLease)
	if auth(other) {
		t.Fatal("stale authority lease accepted")
	}
}

func TestDestinationPolicy(t *testing.T) {
	check := DestinationPolicy([]string{"10.1.2.3:9999", "9.9.9.9:1234"}, true, netip.MustParseAddr("9.9.9.9"))
	for _, test := range []struct {
		addr  string
		allow bool
	}{
		{"10.1.2.3:9999", true}, {"10.1.2.3:5432", false}, {"127.0.0.1:80", false},
		{"169.254.169.254:80", false}, {"100.64.0.1:53", false}, {"192.168.1.1:80", false},
		{"9.9.9.9:1234", true}, {"9.9.9.9:12340", false}, {"8.8.8.8:1234", true}, {"224.0.0.1:1234", false}, {"[::ffff:127.0.0.1]:80", false},
	} {
		addr, err := net.ResolveUDPAddr("udp", test.addr)
		if err != nil {
			t.Fatal(err)
		}
		if check(addr) != test.allow {
			t.Errorf("%s", test.addr)
		}
	}
}

// This test moves actual datagrams over all supported TURN transports. It
// distinguishes rejecting credentials from stopping an existing allocation.
func TestRealTURNRevocationAndExpiry(t *testing.T) {
	for _, transport := range []string{"udp", "tcp", "tls"} {
		t.Run(transport, func(t *testing.T) {
			m := NewManager("test", 32, 1<<20)
			defer m.Close()
			udp, err := (&net.ListenConfig{}).ListenPacket(t.Context(), "udp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			tcp, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			peer, err := (&net.ListenConfig{}).ListenPacket(t.Context(), "udp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = peer.Close() }()
			logger := logging.NewDefaultLoggerFactory()
			logger.DefaultLogLevel = logging.LogLevelDisabled
			generator := &Generator{Manager: m, Base: &pion.RelayAddressGeneratorPortRange{RelayAddress: net.ParseIP("127.0.0.1"), Address: "127.0.0.1", MinPort: 30000, MaxPort: 39999}, AllowedPeer: DestinationPolicy([]string{peer.LocalAddr().String()}, false)}
			var tlsConfig *tls.Config
			tlsAddress := ""
			stream := tcp
			if transport == "tls" {
				stream = newProxyListener(tcp, []string{"127.0.0.1"})
				proxy, cfg, _ := testTLSProxy(t, tcp.Addr().String(), "127.0.0.1")
				tlsConfig, tlsAddress = cfg, proxy.Addr().String()
			}
			limited := limitedTCPListener(stream, 64)
			limited.preAuthLifetime = 500 * time.Millisecond
			srv, err := pion.NewServer(pion.ServerConfig{Realm: "test", AuthHandler: m.Authenticate, EventHandler: limited.events(), LoggerFactory: logger, PacketConnConfigs: []pion.PacketConnConfig{{PacketConn: udp, RelayAddressGenerator: generator}}, ListenerConfigs: []pion.ListenerConfig{{Listener: limited, RelayAddressGenerator: generator}}})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = srv.Close() }()
			address := udp.LocalAddr().String()
			if transport == "tcp" {
				address = tcp.Addr().String()
			}
			if transport == "tls" {
				address = tlsAddress
			}
			allocate := func(l Lease) (net.PacketConn, error) {
				var conn net.PacketConn
				if transport != "udp" {
					var raw net.Conn
					var e error
					if tlsConfig != nil {
						raw, e = (&tls.Dialer{NetDialer: &net.Dialer{Timeout: time.Second}, Config: tlsConfig}).DialContext(t.Context(), "tcp", address)
					} else {
						raw, e = (&net.Dialer{Timeout: time.Second}).DialContext(t.Context(), "tcp", address)
					}
					if e != nil {
						return nil, e
					}
					conn = pion.NewSTUNConn(raw)
				} else {
					var e error
					conn, e = (&net.ListenConfig{}).ListenPacket(t.Context(), "udp4", "127.0.0.1:0")
					if e != nil {
						return nil, e
					}
				}
				t.Cleanup(func() { _ = conn.Close() })
				c, e := pion.NewClient(&pion.ClientConfig{Conn: conn, STUNServerAddr: address, TURNServerAddr: address, Username: l.Username, Password: l.Password, Realm: "test", LoggerFactory: logger, RTO: 20 * time.Millisecond})
				if e != nil {
					return nil, e
				}
				t.Cleanup(c.Close)
				if e = c.Listen(); e != nil {
					return nil, e
				}
				return c.Allocate()
			}
			issue := func(id string) Lease {
				m.Register(id, "same-account")
				m.Authorize(id, true)
				l, e := m.Issue(id)
				if e != nil {
					t.Fatal(e)
				}
				return l
			}
			a, b := issue("a"), issue("b")
			ra, err := allocate(a)
			if err != nil {
				t.Fatal(err)
			}
			rb, err := allocate(b)
			if err != nil {
				t.Fatal(err)
			}
			if transport != "udp" {
				limited.mu.Lock()
				pending, active := limited.pending, len(limited.conns)
				limited.mu.Unlock()
				if pending != 0 || active != 2 {
					t.Fatalf("successful allocations were not admitted: pending=%d active=%d", pending, active)
				}
			}
			exchange := func(relay net.PacketConn) {
				t.Helper()
				if _, e := relay.WriteTo([]byte("outbound-packet"), peer.LocalAddr()); e != nil {
					t.Fatal(e)
				}
				_ = peer.SetReadDeadline(time.Now().Add(time.Second))
				buf := make([]byte, 64)
				n, from, e := peer.ReadFrom(buf)
				if e != nil || string(buf[:n]) != "outbound-packet" {
					t.Fatalf("outbound: %v", e)
				}
				if _, e = peer.WriteTo([]byte("inbound-packet"), from); e != nil {
					t.Fatal(e)
				}
				_ = relay.SetReadDeadline(time.Now().Add(time.Second))
				n, _, e = relay.ReadFrom(buf)
				if e != nil || string(buf[:n]) != "inbound-packet" {
					t.Fatalf("inbound: %v", e)
				}
			}
			exchange(ra)
			exchange(rb)
			m.mu.Lock()
			m.grants[a.Username].lease.RefreshAt = time.Now().Add(-time.Second).UnixMilli()
			m.mu.Unlock()
			replacement, err := m.Issue("a")
			if err != nil {
				t.Fatal(err)
			}
			rc, err := allocate(replacement)
			if err != nil {
				t.Fatal(err)
			}
			exchange(rc)
			started := time.Now()
			m.Revoke("a")
			t.Logf("socket revocation: %s", time.Since(started))
			// Try both directions on the existing allocation; this does not rely on a
			// refresh error or on cooperation from the removed TURN client.
			_, _ = ra.WriteTo([]byte("denied"), peer.LocalAddr())
			_ = peer.SetReadDeadline(time.Now().Add(150 * time.Millisecond))
			buf := make([]byte, 64)
			if _, _, e := peer.ReadFrom(buf); e == nil {
				t.Fatal("revoked allocation still forwards outbound")
			}
			_, _ = peer.WriteTo([]byte("denied"), ra.LocalAddr())
			_ = ra.SetReadDeadline(time.Now().Add(150 * time.Millisecond))
			if _, _, e := ra.ReadFrom(buf); e == nil {
				t.Fatal("revoked allocation still forwards inbound")
			}
			if _, e := allocate(a); e == nil {
				t.Fatal("copied credential accepted after revocation")
			}
			if _, e := allocate(replacement); e == nil {
				t.Fatal("pre-issued credential accepted after revocation")
			}
			_, _ = rc.WriteTo([]byte("revoked-replacement"), peer.LocalAddr())
			_ = peer.SetReadDeadline(time.Now().Add(150 * time.Millisecond))
			if _, _, e := peer.ReadFrom(buf); e == nil {
				t.Fatal("replacement allocation survived revocation")
			}
			exchange(rb)
			// Force the immutable deadline past without depending on a cleanup timer.
			m.mu.Lock()
			m.grants[b.Username].expires = time.Now().Add(-time.Second)
			m.mu.Unlock()
			_, _ = rb.WriteTo([]byte("expired"), peer.LocalAddr())
			_ = peer.SetReadDeadline(time.Now().Add(150 * time.Millisecond))
			if _, _, e := peer.ReadFrom(buf); e == nil {
				t.Fatal("expired allocation forwarded")
			}
			m.Sweep()
		})
	}
}

func TestRevocationAllocationRace(t *testing.T) {
	m := NewManager("test", 32, 1<<20)
	defer m.Close()
	m.Register("s", "a")
	m.Authorize("s", true)
	l, _ := m.Issue("s")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ctx.Err() == nil {
			_, _ = m.Issue("s")
			m.Authorize("s", true)
		}
	}()
	m.Revoke("s")
	cancel()
	<-done
	if _, _, ok := m.Authenticate(&pion.RequestAttributes{Username: l.Username, Realm: "test"}); ok {
		t.Fatal("renewal resurrected revoked grant")
	}
}
