package server

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"go.uber.org/zap"

	"voicx/internal/config"
)

// A peer that sends no frame must still expire and release its admission slot.
// This is the regression for the old zero-lastActive timeout bypass.
func TestPreAuthIdleConnectionExpiresWithoutSendingFrame(t *testing.T) {
	s, addr, admitted := startPreauthProbeServer(t, &config.Config{
		TCPAddr:              "127.0.0.1:0",
		MaxClients:           16,
		ClientTimeoutSeconds: 1,
	})
	conn := dialPreauthProbe(t, addr)
	defer func() { _ = conn.Close() }()
	select {
	case <-admitted:
	case <-time.After(3 * time.Second):
		t.Fatal("idle connection was not admitted")
	}
	if s.clientCount() != 1 {
		t.Fatal("idle connection did not hold an admission slot")
	}
	assertPreauthProbeClosed(t, conn, time.Now().Add(3*time.Second))
	waitForTCPConnectionCleanup(t, s)
}

func startPreauthProbeServer(t *testing.T, cfg *config.Config) (*TCPServer, string, <-chan struct{}) {
	t.Helper()
	s := New(cfg, zap.NewNop(), nil)
	admitted := make(chan struct{}, cfg.MaxClients+1)
	s.beforeHandle = func() { admitted <- struct{}{} }
	ctx, cancel := context.WithCancel(context.Background())
	startErr := make(chan error, 1)
	go func() { startErr <- s.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		shutdownCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if err := s.Shutdown(shutdownCtx); err != nil {
			t.Errorf("shutdown: %v", err)
		}
		select {
		case err := <-startErr:
			if err != nil {
				t.Errorf("Start: %v", err)
			}
		case <-shutdownCtx.Done():
			t.Error("server did not stop")
		}
	})
	select {
	case <-s.started:
	case <-time.After(3 * time.Second):
		t.Fatal("listener did not start")
	}
	s.lifecycleMu.Lock()
	addr := s.listener.Addr().String()
	s.lifecycleMu.Unlock()
	return s, addr, admitted
}

func dialPreauthProbe(t *testing.T, addr string) net.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	return conn
}

func assertPreauthProbeClosed(t *testing.T, conn net.Conn, deadline time.Time) {
	t.Helper()
	if err := conn.SetReadDeadline(deadline); err != nil {
		t.Fatal(err)
	}
	if n, err := conn.Read(make([]byte, 1)); n != 0 || !errors.Is(err, io.EOF) {
		t.Fatalf("server did not close idle pre-auth connection: bytes=%d, error=%v", n, err)
	}
}
