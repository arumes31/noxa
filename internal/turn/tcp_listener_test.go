package turn

import (
	"fmt"
	"net"
	"testing"
	"time"
)

func TestTCPListenerClosesAcceptedConnections(t *testing.T) {
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	limited := limitedTCPListener(listener, 2)
	client, err := (&net.Dialer{}).DialContext(t.Context(), "tcp4", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	accepted, err := limited.Accept()
	if err != nil {
		t.Fatal(err)
	}
	if err := limited.Close(); err != nil {
		t.Fatal(err)
	}
	_ = accepted.SetReadDeadline(time.Now().Add(time.Second))
	var b [1]byte
	if _, err := accepted.Read(b[:]); err == nil {
		t.Fatal("accepted socket survived listener shutdown")
	}
}

func TestTCPPreAuthDeadlineSurvivesTrickleAndReclaimsSlot(t *testing.T) {
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	limited := limitedTCPListener(listener, 2)
	limited.preAuthLifetime = 150 * time.Millisecond
	defer func() { _ = limited.Close() }()
	client, err := (&net.Dialer{}).DialContext(t.Context(), "tcp4", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	accepted, err := limited.Accept()
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		var b [1]byte
		for {
			if _, err := accepted.Read(b[:]); err != nil {
				done <- err
				return
			}
		}
	}()
	started := time.Now()
	for range 40 {
		_, _ = client.Write([]byte{0})
		select {
		case <-done:
			if time.Since(started) > time.Second {
				t.Fatal("trickle extended fixed deadline")
			}
			limited.mu.Lock()
			pending, active := limited.pending, len(limited.conns)
			limited.mu.Unlock()
			if pending != 0 || active != 0 {
				t.Fatal("expired transport retained slot")
			}
			return
		case <-time.After(10 * time.Millisecond):
		}
	}
	t.Fatal("unauthenticated trickle survived deadline")
}

func TestTCPPreAuthSourceQuotaDoesNotConsumeAllSlots(t *testing.T) {
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	limited := limitedTCPListener(listener, 32)
	defer func() { _ = limited.Close() }()
	dial := func() net.Conn {
		t.Helper()
		c, e := (&net.Dialer{}).DialContext(t.Context(), "tcp4", listener.Addr().String())
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(func() { _ = c.Close() })
		return c
	}
	var first net.Conn
	for range maxPendingPerSource {
		dial()
		c, e := limited.Accept()
		if e != nil {
			t.Fatal(e)
		}
		if first == nil {
			first = c
		}
	}
	rejected := dial()
	accepted := make(chan net.Conn, 1)
	go func() { c, _ := limited.Accept(); accepted <- c }()
	_ = rejected.SetReadDeadline(time.Now().Add(time.Second))
	var b [1]byte
	if _, e := rejected.Read(b[:]); e == nil {
		t.Fatal("source quota admitted an extra pending socket")
	} else if timeout, ok := e.(net.Error); ok && timeout.Timeout() {
		t.Fatal("over-quota socket was not promptly closed")
	}
	_ = first.Close()
	dial()
	select {
	case c := <-accepted:
		if c == nil {
			t.Fatal("reclaimed slot could not be accepted")
		}
	case <-time.After(time.Second):
		t.Fatal("released source budget was not reclaimed")
	}
}

// fakeAddrListener hands out pipe ends that report chosen remote addresses.
type fakeAddrListener struct{ conns chan net.Conn }

func (l fakeAddrListener) Accept() (net.Conn, error) {
	c, ok := <-l.conns
	if !ok {
		return nil, net.ErrClosed
	}
	return c, nil
}
func (l fakeAddrListener) Close() error   { return nil }
func (l fakeAddrListener) Addr() net.Addr { return &net.TCPAddr{} }

type remoteAddrConn struct {
	net.Conn
	remote net.Addr
}

func (c remoteAddrConn) RemoteAddr() net.Addr { return c.remote }

func TestTCPPreAuthGroupsIPv6AndCapsPendingPool(t *testing.T) {
	fake := fakeAddrListener{conns: make(chan net.Conn, 64)}
	limited := limitedTCPListener(fake, 16)
	defer func() { _ = limited.Close() }()
	offer := func(ip string, port int) net.Conn {
		a, b := net.Pipe()
		t.Cleanup(func() { _ = a.Close(); _ = b.Close() })
		fake.conns <- remoteAddrConn{Conn: a, remote: &net.TCPAddr{IP: net.ParseIP(ip), Port: port}}
		return b
	}
	for i := range maxPendingPerSource {
		offer(fmt.Sprintf("2001:db8:1:2::%x", i+1), 1000)
	}
	// Rejected: same /64; admitted: a fresh source, which Accept returns next.
	rejected := offer("2001:db8:1:2:ffff::1", 1000)
	offer("192.0.2.1", 1000)
	for range maxPendingPerSource + 1 {
		if _, err := limited.Accept(); err != nil {
			t.Fatal(err)
		}
	}
	if c, ok := limited.conns["[2001:db8:1:2:ffff::1]:1000"]; ok {
		t.Fatalf("same /64 bypassed the source quota: %v", c.RemoteAddr())
	}
	_ = rejected.SetReadDeadline(time.Now().Add(time.Second))
	var b [1]byte
	if _, err := rejected.Read(b[:]); err == nil {
		t.Fatal("over-quota stream left open")
	}
	// The pending pool is half the socket limit.
	for i := 2; limited.pending < limited.maxPending; i++ {
		offer(fmt.Sprintf("192.0.2.%d", i), 1000)
		if _, err := limited.Accept(); err != nil {
			t.Fatal(err)
		}
	}
	if limited.maxPending != 8 {
		t.Fatalf("pending pool = %d, want half of 16", limited.maxPending)
	}
	overflow := offer("198.51.100.1", 1000)
	go func() { _, _ = limited.Accept() }()
	defer close(fake.conns)
	_ = overflow.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := overflow.Read(b[:]); err == nil {
		t.Fatal("pending pool overflow left open")
	}
}
