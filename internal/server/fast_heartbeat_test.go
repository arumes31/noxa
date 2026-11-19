package server

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"slices"
	"testing"
	"time"

	"go.uber.org/zap"

	"noxa/internal/broadcast"
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
	silentFor(c, netproto.FastHeartbeatTimeout-300*time.Millisecond)
	if _, lost := c.fastHeartbeatLost(time.Now()); lost {
		t.Fatal("silence inside the window is not a lost connection")
	}
	silentFor(c, netproto.FastHeartbeatTimeout+300*time.Millisecond)
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

// End to end: a resumable desktop that announced the feature and pinged once
// is dropped within the window after it falls silent. Its kept place is
// marked lost_aloud for the observers, and a resume clears it. An old client
// with the same silence stays.
func TestFastHeartbeatDropsSilentDesktopEndToEnd(t *testing.T) {
	env := startTestEnvFull(t, nil, func(c *config.Config) { c.ClientTimeoutSeconds = 90 })
	defer env.stop()
	env.state.AddChannel(testChannel(1))
	observer, _ := dialAuthed(t, env.addr, "admin-uid")
	defer func() { _ = observer.Close() }()
	send(t, observer, netproto.MsgJoinChannel, netproto.JoinChannel{ChannelID: 1})
	login := func(username, token string, features ...string) (net.Conn, netproto.AuthResponse) {
		conn := dialRetry(t, env.addr)
		t.Cleanup(func() { _ = conn.Close() })
		send(t, conn, netproto.MsgAuthenticate, netproto.Authenticate{Username: username, Password: "pw", ResumeToken: token, ClientFeatures: features})
		var resp netproto.AuthResponse
		if err := netproto.Decode(readOfType(t, conn, netproto.MsgAuthResponse), &resp); err != nil || !resp.OK {
			t.Fatalf("login: %v %+v", err, resp)
		}
		if !slices.Contains(resp.Capabilities, netproto.CapabilityFastHeartbeat) {
			t.Fatalf("capabilities %v lack %s", resp.Capabilities, netproto.CapabilityFastHeartbeat)
		}
		send(t, conn, netproto.MsgPing, netproto.Ping{})
		readOfType(t, conn, netproto.MsgPong)
		return conn, resp
	}
	_, legacy := login("admin-uid", "", netproto.ClientFeatureGoodbye, netproto.ClientFeatureResume)
	desktop := []string{netproto.ClientFeatureGoodbye, netproto.ClientFeatureResume, netproto.ClientFeatureFastHeartbeat}
	fast, first := login("user-uid", "", desktop...)
	id := first.ClientID
	send(t, fast, netproto.MsgJoinChannel, netproto.JoinChannel{ChannelID: 1})
	waitFor(t, "desktop in channel", func() bool { c, ok := env.state.GetClient(id); return ok && c.ChannelID == 1 })
	silentAt := time.Now()
	awaitSnapshot(t, observer, "an announced drop", func(s *broadcast.TreeSnapshot) bool {
		m := snapshotMember(s, id)
		return m != nil && m.Reconnecting && m.LostAloud && m.ChannelID == 1
	}, nil)
	if took := time.Since(silentAt); took < netproto.FastHeartbeatTimeout-time.Second || took > netproto.FastHeartbeatTimeout+fastHeartbeatCheck+3*time.Second {
		t.Fatalf("dropped after %s, want %s plus one check tick", took, netproto.FastHeartbeatTimeout)
	}
	if _, ok := env.state.GetClient(legacy.ClientID); !ok {
		t.Fatal("an old client was dropped by the fast heartbeat window")
	}
	_, second := login("user-uid", first.ResumeToken, desktop...)
	if !second.Resumed || second.ClientID != id {
		t.Fatalf("resume: resumed %v client %q, want %q", second.Resumed, second.ClientID, id)
	}
	awaitSnapshot(t, observer, "the mark cleared by the resume", func(s *broadcast.TreeSnapshot) bool {
		m := snapshotMember(s, id)
		return m != nil && !m.Reconnecting && !m.LostAloud && m.ChannelID == 1
	}, nil)
}

// An armed client gets a server ping every few seconds, so a slow handler
// holding back its pong cannot trip the client's short read timeout.
func TestPingLoopPingsArmedClientOften(t *testing.T) {
	local, peer := net.Pipe()
	t.Cleanup(func() { _ = peer.Close(); _ = local.Close() })
	s := &TCPServer{logger: zap.NewNop(), cfg: &config.Config{ClientTimeoutSeconds: 90}}
	c := &Client{ID: "c", Conn: local}
	c.fastHeartbeat.Store(true) // readWaitSince 0: the read loop is busy dispatching
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })
	go s.pingLoop(c, stop)
	_ = peer.SetReadDeadline(time.Now().Add(fastServerPingInterval + fastHeartbeatCheck + 3*time.Second))
	f, err := netproto.ReadFrame(peer)
	if err != nil || netproto.MessageType(f.Type) != netproto.MsgPing {
		t.Fatalf("no server ping within %s: %v", fastServerPingInterval, err)
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

// A desktop whose socket simply closes without a goodbye (a PC shutdown) is
// announced at once, not after the resume grace or the silence window.
func TestDesktopClosedWithoutGoodbyeIsLostAloud(t *testing.T) {
	env := startTestEnvFull(t, nil, func(c *config.Config) { c.ClientTimeoutSeconds = 90 })
	defer env.stop()
	env.state.AddChannel(testChannel(1))
	observer, _ := dialAuthed(t, env.addr, "admin-uid")
	defer func() { _ = observer.Close() }()
	send(t, observer, netproto.MsgJoinChannel, netproto.JoinChannel{ChannelID: 1})
	conn := dialRetry(t, env.addr)
	send(t, conn, netproto.MsgAuthenticate, netproto.Authenticate{Username: "user-uid", Password: "pw",
		ClientFeatures: []string{netproto.ClientFeatureGoodbye, netproto.ClientFeatureResume, netproto.ClientFeatureFastHeartbeat}})
	var resp netproto.AuthResponse
	if err := netproto.Decode(readOfType(t, conn, netproto.MsgAuthResponse), &resp); err != nil || !resp.OK {
		t.Fatalf("login: %v %+v", err, resp)
	}
	send(t, conn, netproto.MsgPing, netproto.Ping{}) // arms the fast heartbeat
	readOfType(t, conn, netproto.MsgPong)
	send(t, conn, netproto.MsgJoinChannel, netproto.JoinChannel{ChannelID: 1})
	waitFor(t, "desktop in channel", func() bool { c, ok := env.state.GetClient(resp.ClientID); return ok && c.ChannelID == 1 })
	closedAt := time.Now()
	_ = conn.Close()
	awaitSnapshot(t, observer, "an announced drop", func(s *broadcast.TreeSnapshot) bool {
		m := snapshotMember(s, resp.ClientID)
		return m != nil && m.Reconnecting && m.LostAloud && m.ChannelID == 1
	}, nil)
	if took := time.Since(closedAt); took >= netproto.FastHeartbeatTimeout {
		t.Fatalf("announced after %s, want at once", took)
	}
}
