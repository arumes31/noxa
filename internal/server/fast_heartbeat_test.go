package server

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"testing"
	"time"

	"go.uber.org/zap"

	"noxa/internal/config"
	"noxa/internal/netproto"
)

// silentFor backdates the read loop's last received byte.
func silentFor(c *Client, d time.Duration) {
	c.readWaitSince.Store(int64(time.Since(monoEpoch) - d))
}

func TestFastHeartbeatLost(t *testing.T) {
	c := &Client{}
	silentFor(c, time.Minute)
	if _, lost := c.fastHeartbeatLost(time.Now()); lost {
		t.Fatal("a client without the fast heartbeat keeps the configured timeout")
	}
	c.fastHeartbeat.Store(true)
	silentFor(c, netproto.FastHeartbeatTimeout-time.Second)
	if _, lost := c.fastHeartbeatLost(time.Now()); lost {
		t.Fatal("silence inside the window is not a lost connection")
	}
	silentFor(c, netproto.FastHeartbeatTimeout+time.Millisecond)
	if _, lost := c.fastHeartbeatLost(time.Now()); !lost {
		t.Fatal("silence past the window is a lost connection")
	}
	c.readWaitSince.Store(0) // dispatching a slow request
	if _, lost := c.fastHeartbeatLost(time.Now()); lost {
		t.Fatal("a slow handler is not client silence")
	}
	silentFor(c, time.Minute)
	c.handover = &handoverStandby{deadline: time.Now().Add(time.Minute)}
	if _, lost := c.fastHeartbeatLost(time.Now()); lost {
		t.Fatal("a handover standby keeps the configured timeout")
	}
}

// startPingLoop runs pingLoop for a client whose read loop went silent.
func startPingLoop(t *testing.T, fast bool) (*Client, net.Conn) {
	t.Helper()
	local, peer := net.Pipe()
	t.Cleanup(func() { _ = peer.Close(); _ = local.Close() })
	s := &TCPServer{logger: zap.NewNop(), cfg: &config.Config{ClientTimeoutSeconds: 90}}
	c := &Client{ID: "c", Conn: local}
	c.fastHeartbeat.Store(fast)
	silentFor(c, netproto.FastHeartbeatTimeout+time.Second)
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })
	go s.pingLoop(c, stop)
	return c, peer
}

// An opted-in client that falls silent is closed within one check tick, as a
// lost connection.
func TestPingLoopClosesSilentFastHeartbeatClient(t *testing.T) {
	c, peer := startPingLoop(t, true)
	_ = peer.SetReadDeadline(time.Now().Add(fastHeartbeatCheck + 2*time.Second))
	if _, err := peer.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("silent fast-heartbeat client not closed: %v", err)
	}
	if cause := c.closeCause.Load(); cause == nil || *cause != netproto.ControlCloseServerInactivity {
		t.Fatalf("close cause = %v, want server inactivity", cause)
	}
	if got := c.departureReason(); got != netproto.DepartureConnectionLost {
		t.Fatalf("departure = %q, want a lost connection", got)
	}
}

// An old client with the same silence keeps the 90 s client_timeout_seconds.
func TestPingLoopKeepsLegacyClientTimeout(t *testing.T) {
	_, peer := startPingLoop(t, false)
	_ = peer.SetReadDeadline(time.Now().Add(fastHeartbeatCheck + 2*time.Second))
	if _, err := peer.Read(make([]byte, 1)); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("legacy client closed early: %v", err)
	}
}

// The fast rule arms on the first ping after login, and only for clients that
// announced the feature.
func TestHandlePingArmsFastHeartbeat(t *testing.T) {
	for _, tc := range []struct {
		name              string
		announced, authed bool
		armed             bool
	}{
		{"opted in", true, true, true},
		{"old client", false, true, false},
		{"before login", true, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			local, peer := net.Pipe()
			defer func() { _ = peer.Close(); _ = local.Close() }()
			go func() { _, _ = io.Copy(io.Discard, peer) }()
			s := &TCPServer{logger: zap.NewNop()}
			c := &Client{ID: "c", Conn: local, authed: tc.authed, announcesFastHB: tc.announced}
			if err := s.handlePing(context.Background(), c, nil); err != nil {
				t.Fatal(err)
			}
			if got := c.fastHeartbeat.Load(); got != tc.armed {
				t.Fatalf("armed = %v, want %v", got, tc.armed)
			}
		})
	}
}

// Bytes of a frame still arriving count as activity.
func TestActivityReaderStampsPartialFrames(t *testing.T) {
	local, peer := net.Pipe()
	defer func() { _ = peer.Close(); _ = local.Close() }()
	c := &Client{}
	silentFor(c, time.Minute)
	go func() { _, _ = peer.Write([]byte{0, 0}) }() // half a length prefix
	if _, err := (activityReader{conn: local, client: c}).Read(make([]byte, 4)); err != nil {
		t.Fatal(err)
	}
	c.fastHeartbeat.Store(true)
	if _, lost := c.fastHeartbeatLost(time.Now()); lost {
		t.Fatal("received bytes did not reset the silence")
	}
}
