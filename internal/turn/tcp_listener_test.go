package turn

import (
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
	for range 8 {
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
		t.Fatal("source quota admitted ninth pending socket")
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
