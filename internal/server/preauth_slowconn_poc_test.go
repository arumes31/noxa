package server

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"testing"
	"time"

	"go.uber.org/zap"

	"voicx/internal/config"
)

// TestPreAuthIdleConnectionHold demonstrates that an unauthenticated
// connection that sends nothing is held until the (default 90s) global
// inactivity timeout fires in pingLoop, and that there is no shorter
// pre-auth handshake deadline. With the default max_clients of 512, an
// unauthenticated attacker can occupy every connection slot by opening
// ~512 idle connections within a single 15-second ping window and holding
// them for ~90 seconds per refresh wave, denying service to real clients.
func TestPreAuthIdleConnectionHold(t *testing.T) {
	addr := freePort(t)
	cfg := &config.Config{
		TCPAddr:              addr,
		MaxClients:           64,
		ClientTimeoutSeconds: 90,
	}
	s := New(cfg, zap.NewNop(), nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	startErr := make(chan error, 1)
	go func() { startErr <- s.Start(ctx) }()

	var dial = func(id string) net.Conn {
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			c, err := (&net.Dialer{}).DialContext(ctx, "tcp", addr)
			if err == nil {
				return c
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("%s: dial failed", id)
		return nil
	}

	// Phase 1: open 70 idle connections (more than max_clients=64). None of
	// them ever sends a frame or authenticates.
	conns := make([]net.Conn, 0, 70)
	for i := 0; i < 70; i++ {
		conns = append(conns, dial(fmt.Sprintf("idle-%d", i)))
	}
	defer func() {
		for _, c := range conns {
			_ = c.Close()
		}
	}()

	// Phase 2: give the server a moment to accept, then confirm it has
	// registered all 70 connections and (should have) rejected the overflow.
	time.Sleep(300 * time.Millisecond)
	count := s.clientCount()
	t.Logf("registered idle connections: %d (max_clients=%d)", count, cfg.MaxClients)

	// Phase 3: try to send a Ping from connection #70 (over the cap). It
	// must NOT get a Pong; the overflow connections should have been told
	// "server is full". Read for 2s on the last connection.
	extra := conns[len(conns)-1]
	var hdr [6]byte
	binary.BigEndian.PutUint32(hdr[:4], 2)
	binary.BigEndian.PutUint16(hdr[4:6], uint16(8)) // MsgPing
	_ = extra.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _ = extra.Write(hdr[:])
	buf := make([]byte, 64)
	n, _ := extra.Read(buf)
	t.Logf("overflow connection read %d bytes: %q", n, buf[:n])

	// Phase 4: the decisive check — after 20s (> one 15s ping tick, well
	// before the 90s inactivity timeout), every idle connection is still
	// registered. No pre-auth deadline exists.
	time.Sleep(20 * time.Second)
	time.Sleep(100 * time.Millisecond)
	count2 := s.clientCount()
	t.Logf("after 20s: %d idle connections still held", count2)
	if count2 < 64 {
		t.Fatalf("expected idle connections to be held pre-auth; count dropped to %d", count2)
	}

	cancel()
	select {
	case <-startErr:
	case <-time.After(5 * time.Second):
		t.Fatal("server did not stop")
	}
}
