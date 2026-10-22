package turn

import (
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	proxyproto "github.com/pires/go-proxyproto"
)

func testProxyListener(t *testing.T, trusted []string) *proxyListener {
	t.Helper()
	raw, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	l := newProxyListener(raw, trusted)
	t.Cleanup(func() { _ = l.Close() })
	return l
}
func dialProxy(t *testing.T, l net.Listener) net.Conn {
	t.Helper()
	c, err := (&net.Dialer{Timeout: time.Second}).DialContext(t.Context(), "tcp4", l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}
func requireProxyClosed(t *testing.T, c net.Conn) {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	var b [1]byte
	if _, err := c.Read(b[:]); err == nil {
		t.Fatal("invalid stream survived")
	} else if e, ok := err.(net.Error); ok && e.Timeout() {
		t.Fatal("invalid stream was not closed promptly")
	}
}
func TestProxyHeaderPreservesAddressAndPayload(t *testing.T) {
	for _, version := range []byte{1, 2} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			l := testProxyListener(t, []string{"127.0.0.1"})
			client := dialProxy(t, l)
			source := &net.TCPAddr{IP: net.ParseIP("192.0.2.7"), Port: 54321}
			h := proxyproto.HeaderProxyFromAddrs(version, source, client.RemoteAddr())
			if _, err := h.WriteTo(client); err != nil {
				t.Fatal(err)
			}
			if _, err := client.Write([]byte("payload")); err != nil {
				t.Fatal(err)
			}
			server, err := l.Accept()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = server.Close() }()
			if server.RemoteAddr().String() != source.String() {
				t.Fatal(server.RemoteAddr())
			}
			b := make([]byte, 7)
			_ = server.SetReadDeadline(time.Now().Add(time.Second))
			if _, err := io.ReadFull(server, b); err != nil || string(b) != "payload" {
				t.Fatalf("payload lost: %q %v", b, err)
			}
		})
	}
}
func TestProxyRejectsUntrustedAndMalformedHeaders(t *testing.T) {
	for _, tc := range []struct {
		name    string
		trusted []string
		header  string
	}{
		{"untrusted", []string{"127.0.0.2"}, "PROXY TCP4 192.0.2.7 127.0.0.1 1234 12341\r\n"},
		{"missing", []string{"127.0.0.1"}, "GET / HTTP/1.1\r\n\r\n"},
		{"unknown", []string{"127.0.0.1"}, "PROXY UNKNOWN\r\n"},
		{"zero source", []string{"127.0.0.1"}, "PROXY TCP4 0.0.0.0 127.0.0.1 1234 12341\r\n"},
		{"bad port", []string{"127.0.0.1"}, "PROXY TCP4 192.0.2.7 127.0.0.1 0 12341\r\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := testProxyListener(t, tc.trusted)
			c := dialProxy(t, l)
			_, _ = c.Write([]byte(tc.header))
			requireProxyClosed(t, c)
		})
	}
}
func TestSlowProxyHeaderDoesNotBlockOtherClients(t *testing.T) {
	l := testProxyListener(t, []string{"127.0.0.1"})
	slow := dialProxy(t, l)
	_, _ = slow.Write([]byte("PRO"))
	good := dialProxy(t, l)
	_, _ = good.Write([]byte("PROXY TCP4 192.0.2.8 127.0.0.1 1234 12341\r\n"))
	accepted := make(chan net.Conn, 1)
	go func() { c, _ := l.Accept(); accepted <- c }()
	select {
	case c := <-accepted:
		if c == nil {
			t.Fatal("valid stream rejected")
		}
		_ = c.Close()
	case <-time.After(time.Second):
		t.Fatal("slow header blocked another client")
	}
	requireProxyClosed(t, slow)
}
func TestProxyQuotasUseClientAddressAndRejectDuplicateTuple(t *testing.T) {
	proxy := testProxyListener(t, []string{"127.0.0.1"})
	l := limitedTCPListener(proxy, 32)
	defer func() { _ = l.Close() }()
	send := func(ip string, port int) net.Conn {
		c := dialProxy(t, l)
		_, _ = fmt.Fprintf(c, "PROXY TCP4 %s 127.0.0.1 %d 12341\r\n", ip, port)
		return c
	}
	for i := range 8 {
		send("192.0.2.7", 10000+i)
		if _, err := l.Accept(); err != nil {
			t.Fatal(err)
		}
	}
	rejected := send("192.0.2.7", 10008)
	accepted := make(chan net.Conn, 1)
	go func() { c, _ := l.Accept(); accepted <- c }()
	requireProxyClosed(t, rejected)
	send("192.0.2.8", 10000)
	select {
	case c := <-accepted:
		if c == nil {
			t.Fatal("second client shares first client's quota")
		}
	case <-time.After(time.Second):
		t.Fatal("second client blocked")
	}
	duplicate := send("192.0.2.8", 10000)
	go func() {
		c, _ := l.Accept()
		if c != nil {
			_ = c.Close()
		}
	}()
	requireProxyClosed(t, duplicate)
}
func TestProxyShutdownClosesIncompleteHeader(t *testing.T) {
	l := testProxyListener(t, []string{"127.0.0.1"})
	c := dialProxy(t, l)
	_, _ = c.Write([]byte("PRO"))
	_ = l.Close()
	requireProxyClosed(t, c)
	if _, err := l.Accept(); err == nil {
		t.Fatal("accept after close")
	}
}
