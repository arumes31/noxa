package server

import (
	"context"
	"net"
	"testing"
	"time"

	"go.uber.org/zap"

	"voicx/internal/config"
)

// TestPreAuthIdleNeverTimesOut shows that a pre-auth connection that never
// sends a frame has a zero lastActive timestamp, so the ClientTimeoutSeconds
// check in pingLoop (`!last.IsZero()`) never fires: the connection, its
// goroutine, and its max_clients slot are held forever (bounded only by
// OS-level TCP timeout, typically hours).
func TestPreAuthIdleNeverTimesOut(t *testing.T) {
	addr := freePort(t)
	cfg := &config.Config{
		TCPAddr:              addr,
		MaxClients:           16,
		ClientTimeoutSeconds: 1, // very aggressive: 1s inactivity timeout
	}
	s := New(cfg, zap.NewNop(), nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = s.Start(ctx) }()

	var conn net.Conn
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		c, err := (&net.Dialer{}).DialContext(ctx, "tcp", addr)
		if err == nil {
			conn = c
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if conn == nil {
		t.Fatal("dial failed")
	}
	defer conn.Close()

	// Wait well beyond ClientTimeoutSeconds (1s) and several 15s ping ticks
	// would be ideal; 18s covers one full ping tick with the 1s timeout.
	time.Sleep(18 * time.Second)
	if n := s.clientCount(); n != 1 {
		t.Fatalf("expected idle pre-auth connection to never time out, count=%d", n)
	}
	t.Log("confirmed: idle pre-auth connection with zero lastActive never hits ClientTimeoutSeconds")
}