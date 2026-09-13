package server

import (
	"net"
	"testing"
	"time"

	"voicx/internal/config"
	"voicx/internal/netproto"
)

// Filling admission slots with unauthenticated peers must not hold capacity
// until the longer global inactivity timeout. The authentication deadline must
// evict every idle peer and let a new client communicate again.
func TestPreAuthIdleConnectionsReleaseCapacity(t *testing.T) {
	const maxClients = 64
	s, addr, admitted := startPreauthProbeServer(t, &config.Config{
		TCPAddr:              "127.0.0.1:0",
		MaxClients:           maxClients,
		ClientTimeoutSeconds: 90,
	})
	var conns []net.Conn
	defer func() {
		for _, conn := range conns {
			_ = conn.Close()
		}
	}()
	for range maxClients {
		conns = append(conns, dialPreauthProbe(t, addr))
		select {
		case <-admitted:
		case <-time.After(3 * time.Second):
			t.Fatal("idle connection was not admitted")
		}
	}
	if got := s.clientCount(); got != maxClients {
		t.Fatalf("admitted idle connections = %d, want %d", got, maxClients)
	}

	extra := dialPreauthProbe(t, addr)
	defer extra.Close()
	if err := extra.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	frame, err := netproto.ReadFrame(extra)
	if err != nil {
		t.Fatalf("read admission refusal: %v", err)
	}
	if frame.Type != uint16(netproto.MsgError) {
		t.Fatalf("overflow response type = %d, want Error", frame.Type)
	}
	var refusal netproto.Error
	if err := netproto.Decode(frame, &refusal); err != nil {
		t.Fatal(err)
	}
	if refusal.Code != errCodeUnavailable || refusal.Message != "server is full" {
		t.Fatalf("overflow refusal = %+v", refusal)
	}
	assertPreauthProbeClosed(t, extra, time.Now().Add(3*time.Second))

	// A shared bound catches any surviving peer, without giving each one a
	// fresh allowance. The real 10s authentication deadline is exercised here;
	// tcp_preauth_test.go checks its exact boundary with virtual time.
	deadline := time.Now().Add(13 * time.Second)
	for _, conn := range conns {
		assertPreauthProbeClosed(t, conn, deadline)
	}
	waitForTCPConnectionCleanup(t, s)

	fresh := dialPreauthProbe(t, addr)
	defer fresh.Close()
	if err := fresh.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	ping, err := netproto.Encode(netproto.MsgPing, netproto.Ping{})
	if err != nil {
		t.Fatal(err)
	}
	if err := netproto.WriteFrame(fresh, ping); err != nil {
		t.Fatal(err)
	}
	pong, err := netproto.ReadFrame(fresh)
	if err != nil {
		t.Fatalf("new client after idle eviction: %v", err)
	}
	if pong.Type != uint16(netproto.MsgPong) {
		t.Fatalf("new client response = %d, want Pong", pong.Type)
	}
}
