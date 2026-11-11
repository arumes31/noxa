package main

import (
	"net"
	"testing"
	"time"

	"noxa/internal/netproto"
)

// A deliberate disconnect must announce itself so the server reports a leave,
// not a lost connection.
func TestDisconnectSendsGoodbyeBeforeClosing(t *testing.T) {
	client, server := net.Pipe()
	defer func() { _ = server.Close() }()
	m := &connManager{conn: client}
	frames := make(chan *netproto.Frame, 1)
	go func() {
		_ = server.SetReadDeadline(time.Now().Add(2 * time.Second))
		f, err := netproto.ReadFrame(server)
		if err == nil {
			frames <- f
		}
		close(frames)
	}()
	m.disconnect()
	f, ok := <-frames
	if !ok || netproto.MessageType(f.Type) != netproto.MsgGoodbye {
		t.Fatalf("got %v, want a goodbye frame before the close", f)
	}
}

// Without a peer reading, the goodbye must not hold the disconnect for long.
func TestDisconnectGoodbyeIsBounded(t *testing.T) {
	client, server := net.Pipe() // net.Pipe writes block until read
	defer func() { _ = server.Close() }()
	m := &connManager{conn: client}
	start := time.Now()
	m.disconnect()
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("disconnect took %s with an unread goodbye", elapsed)
	}
}
