package main

import (
	"bytes"
	"net"
	"testing"
	"time"

	"noxa/internal/netproto"
)

// Only a server that advertised heartbeat_fast_v1 gets the fast heartbeat and
// the short read timeout; others keep 15 s / 45 s.
func TestFastHeartbeatOnlyWithServerCapability(t *testing.T) {
	cm := newTestConnManager()
	if got := cm.heartbeatPeriod(); got != defaultHeartbeatInterval {
		t.Fatalf("legacy heartbeat = %s, want %s", got, defaultHeartbeatInterval)
	}
	if left := time.Until(cm.nextReadDeadline()); left < defaultReadTimeout-time.Second {
		t.Fatalf("legacy read deadline in %s, want about %s", left, defaultReadTimeout)
	}
	cm.mu.Lock()
	cm.fastHeartbeat = true
	cm.mu.Unlock()
	if got := cm.heartbeatPeriod(); got != netproto.FastHeartbeatInterval {
		t.Fatalf("fast heartbeat = %s, want %s", got, netproto.FastHeartbeatInterval)
	}
	if left := time.Until(cm.nextReadDeadline()); left > fastReadTimeout || left < fastReadTimeout-time.Second {
		t.Fatalf("fast read deadline in %s, want about %s", left, fastReadTimeout)
	}
	if fastReadTimeout <= netproto.FastHeartbeatTimeout {
		t.Fatal("the client must not give up before the server's own window")
	}
	cm.heartbeatInterval = 10 * time.Millisecond // shorter test settings stay
	if got := cm.heartbeatPeriod(); got != 10*time.Millisecond {
		t.Fatalf("short heartbeat = %s", got)
	}
}

// A frame trickling in slower than the read timeout is not a dead link: every
// received byte renews the deadline.
func TestReadLoopKeepsASlowFrameAlive(t *testing.T) {
	local, peer := net.Pipe()
	t.Cleanup(func() { _ = peer.Close(); _ = local.Close() })
	cm := newTestConnManager()
	cm.readTimeout = 150 * time.Millisecond
	cm.conn = local
	done := make(chan struct{})
	go func() { defer close(done); cm.readLoop(local) }()
	pong, err := netproto.Encode(netproto.MsgPong, netproto.Pong{})
	if err != nil {
		t.Fatal(err)
	}
	var wire bytes.Buffer
	if err := netproto.WriteFrame(&wire, pong); err != nil {
		t.Fatal(err)
	}
	// Three frames at one byte per 50 ms: each takes well past 150 ms.
	for range 3 {
		for _, b := range wire.Bytes() {
			time.Sleep(50 * time.Millisecond)
			if _, err := peer.Write([]byte{b}); err != nil {
				t.Fatalf("slow frame cut off: %v", err)
			}
		}
	}
	select {
	case <-done:
		t.Fatal("read loop closed a slow but live connection")
	default:
	}
	// Silence still ends it within the read timeout.
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("silent connection survived the read timeout")
	}
}
