package main

import (
	"net"
	"testing"
	"time"

	"noxa/internal/netproto"
)

// startProbeLoop runs a read loop on one end of a pipe. respond decides how
// the peer answers each frame it reads.
func startProbeLoop(t *testing.T, respond func(peer net.Conn, f *netproto.Frame)) (*connManager, chan struct{}) {
	t.Helper()
	local, peer := net.Pipe()
	t.Cleanup(func() { _ = peer.Close(); _ = local.Close() })
	cm := newTestConnManager()
	cm.readTimeout = time.Hour // the probe, not the regular deadline, must act
	cm.conn = local
	go func() {
		for {
			f, err := netproto.ReadFrame(peer)
			if err != nil {
				return
			}
			respond(peer, f)
		}
	}()
	done := make(chan struct{})
	go func() { defer close(done); cm.readLoop(local) }()
	return cm, done
}

func TestProbeLivenessClosesASilentConnectionQuickly(t *testing.T) {
	cm, done := startProbeLoop(t, func(net.Conn, *netproto.Frame) {})
	start := time.Now()
	if !cm.probeLiveness(150 * time.Millisecond) {
		t.Fatal("probe did not start on an open connection")
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("silent connection survived the resume probe")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("silent connection closed after %s, want about the probe timeout", elapsed)
	}
	if cm.connected() {
		t.Fatal("manager still reports the dead connection as connected")
	}
}

func TestProbeLivenessKeepsALiveConnection(t *testing.T) {
	cm, done := startProbeLoop(t, func(peer net.Conn, f *netproto.Frame) {
		if netproto.MessageType(f.Type) == netproto.MsgPing {
			pong, err := netproto.Encode(netproto.MsgPong, netproto.Pong{})
			if err == nil {
				_ = netproto.WriteFrame(peer, pong)
			}
		}
	})
	if !cm.probeLiveness(300 * time.Millisecond) {
		t.Fatal("probe did not start on an open connection")
	}
	select {
	case <-done:
		t.Fatal("a connection that answered the probe was closed")
	case <-time.After(time.Second):
	}
	if !cm.connected() {
		t.Fatal("live connection lost after the probe")
	}
}

func TestProbeLivenessWithoutConnection(t *testing.T) {
	if newTestConnManager().probeLiveness(time.Second) {
		t.Fatal("probe reported success without a connection")
	}
}
