package server

import (
	"context"
	"net"
	"testing"
	"testing/synctest"
	"time"

	"go.uber.org/zap"
	"voicx/internal/config"
	"voicx/internal/netproto"
)

// Regression for filling admission slots with idle or partial-frame peers.
// Virtual time proves the absolute deadline frees capacity before the ordinary
// 90-second inactivity timeout, and that a fresh connection can use it.
func TestPreAuthIdleConnectionHold(t *testing.T) {
	for _, partial := range []bool{false, true} {
		name := "idle"
		if partial {
			name = "partial_frame"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s := New(&config.Config{MaxClients: 2, ClientTimeoutSeconds: 90}, zap.NewNop(), nil)
				open := func() (net.Conn, <-chan struct{}) {
					server, peer := net.Pipe()
					done := make(chan struct{})
					go func() { defer close(done); s.handleConn(context.Background(), server) }()
					synctest.Wait()
					return peer, done
				}
				first, firstDone := open()
				defer finishPreauthPipe(first, firstDone)
				second, secondDone := open()
				defer finishPreauthPipe(second, secondDone)
				if partial {
					// Incomplete frame headers must not acquire a fresh deadline.
					if _, err := first.Write([]byte{0}); err != nil {
						t.Fatal(err)
					}
					if _, err := second.Write([]byte{0}); err != nil {
						t.Fatal(err)
					}
				}
				overflow, overflowDone := open()
				defer finishPreauthPipe(overflow, overflowDone)
				frame, err := netproto.ReadFrame(overflow)
				if err != nil {
					t.Fatalf("read admission rejection: %v", err)
				}
				var response netproto.Error
				if frame.Type != uint16(netproto.MsgError) {
					t.Fatalf("overflow reply type = %d", frame.Type)
				}
				if err := netproto.Decode(frame, &response); err != nil {
					t.Fatal(err)
				}
				if response.Code != errCodeUnavailable || response.Message != "server is full" {
					t.Fatalf("overflow response = %+v", response)
				}
				<-overflowDone
				if got := s.clientCount(); got != 2 {
					t.Fatalf("occupied slots = %d, want 2", got)
				}
				time.Sleep(10*time.Second - time.Nanosecond)
				synctest.Wait()
				if got := s.clientCount(); got != 2 {
					t.Fatalf("slots before deadline = %d, want 2", got)
				}
				time.Sleep(time.Nanosecond)
				assertPreauthExpired(t, s, firstDone)
				assertPreauthExpired(t, s, secondDone)
				fresh, freshDone := open()
				defer finishPreauthPipe(fresh, freshDone)
				if got := s.clientCount(); got != 1 {
					t.Fatalf("recovered admission slots = %d, want 1", got)
				}
				ping, err := netproto.Encode(netproto.MsgPing, netproto.Ping{})
				if err != nil {
					t.Fatal(err)
				}
				if err := netproto.WriteFrame(fresh, ping); err != nil {
					t.Fatal(err)
				}
				pong, err := netproto.ReadFrame(fresh)
				if err != nil || pong.Type != uint16(netproto.MsgPong) {
					t.Fatalf("fresh connection did not receive Pong: frame=%+v err=%v", pong, err)
				}
			})
		})
	}
}
