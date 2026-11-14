package server

import (
	"errors"
	"net"
	"slices"
	"testing"
	"time"

	"noxa/internal/auth"
	"noxa/internal/broadcast"
	"noxa/internal/config"
	"noxa/internal/netproto"
)

// dialHandover sends a handover login and returns the connection and reply.
func dialHandover(t *testing.T, addr string, request netproto.Authenticate) (net.Conn, netproto.AuthResponse) {
	t.Helper()
	conn := dialRetry(t, addr)
	request.ClientFeatures = []string{netproto.ClientFeatureGoodbye, netproto.ClientFeatureResume, netproto.ClientFeatureHandover}
	request.Handover = true
	send(t, conn, netproto.MsgAuthenticate, request)
	var resp netproto.AuthResponse
	if err := netproto.Decode(readOfType(t, conn, netproto.MsgAuthResponse), &resp); err != nil {
		t.Fatal(err)
	}
	return conn, resp
}

// expectClosed reads until the server closes conn.
func expectClosed(t *testing.T, conn net.Conn, what string) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(waitDeadline))
	for {
		if _, err := netproto.ReadFrame(conn); err != nil {
			var timeout net.Error
			if errors.As(err, &timeout) && timeout.Timeout() {
				t.Fatalf("%s stayed open", what)
			}
			return
		}
	}
}

// expectSilent asserts that conn receives nothing but keepalives for a while.
func expectSilent(t *testing.T, conn net.Conn, what string) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(400 * time.Millisecond))
	defer func() { _ = conn.SetReadDeadline(time.Time{}) }()
	for {
		f, err := netproto.ReadFrame(conn)
		if err != nil {
			return
		}
		if mt := netproto.MessageType(f.Type); mt != netproto.MsgPing {
			t.Fatalf("%s received %s", what, mt)
		}
	}
}

func TestHandoverStandbyIsInvisibleUntilCommitMovesSessionQuietly(t *testing.T) {
	env := startTestEnv(t, nil)
	defer env.stop()
	env.state.AddChannel(testChannel(1))
	env.state.AddChannel(testChannel(2))
	observer, _ := dialAuthed(t, env.addr, "admin-uid")
	defer func() { _ = observer.Close() }()
	send(t, observer, netproto.MsgJoinChannel, netproto.JoinChannel{ChannelID: 1})
	old, first := dialResumable(t, env.addr, userLogin(""))
	defer func() { _ = old.Close() }()
	if !slices.Contains(first.Capabilities, netproto.CapabilityHandover) {
		t.Fatal("the server must advertise handover")
	}
	send(t, old, netproto.MsgJoinChannel, netproto.JoinChannel{ChannelID: 1})
	waitFor(t, "user in channel", func() bool { c, ok := env.state.GetClient(first.ClientID); return ok && c.ChannelID == 1 })
	authed := env.srv.authenticatedClientCount()

	standby, resp := dialHandover(t, env.addr, userLogin(first.ResumeToken))
	defer func() { _ = standby.Close() }()
	if !resp.OK || !resp.Standby || resp.ClientID != first.ClientID || resp.ResumeToken != "" {
		t.Fatalf("standby reply = %+v", resp)
	}
	if env.srv.authenticatedClientCount() != authed {
		t.Fatal("a standby must not become a session")
	}
	// Others' activity does not reach the standby.
	send(t, old, netproto.MsgJoinChannel, netproto.JoinChannel{ChannelID: 2})
	waitFor(t, "user moved", func() bool { c, _ := env.state.GetClient(first.ClientID); return c.ChannelID == 2 })
	expectSilent(t, standby, "the standby")

	send(t, standby, netproto.MsgHandoverCommit, struct{}{})
	var done netproto.AuthResponse
	if err := netproto.Decode(readOfType(t, standby, netproto.MsgAuthResponse), &done); err != nil {
		t.Fatal(err)
	}
	if !done.OK || !done.Resumed || done.ClientID != first.ClientID || done.ResumeToken == "" || done.ResumeToken == first.ResumeToken {
		t.Fatalf("commit reply = %+v", done)
	}
	readOfType(t, standby, netproto.MsgSnapshot)
	expectClosed(t, old, "the old connection")
	if c, ok := env.state.GetClient(first.ClientID); !ok || c.ChannelID != 2 {
		t.Fatalf("moved session = %+v, want channel 2", c)
	}
	awaitSnapshot(t, observer, "the session continue", func(s *broadcast.TreeSnapshot) bool {
		m := snapshotMember(s, first.ClientID)
		return m != nil && !m.Reconnecting && !m.Updating && m.ChannelID == 2
	}, quietFor(t, first.ClientID))
	if _, ok := env.srv.clientByID(first.ClientID); !ok {
		t.Fatal("the old connection's cleanup unregistered the moved session")
	}
}

func TestHandoverStandbyTimesOutLeavingSessionUntouched(t *testing.T) {
	previous := handoverStandbyWindow
	handoverStandbyWindow = 300 * time.Millisecond
	defer func() { handoverStandbyWindow = previous }()
	env := startTestEnv(t, nil)
	defer env.stop()
	old, first := dialResumable(t, env.addr, userLogin(""))
	defer func() { _ = old.Close() }()

	standby, resp := dialHandover(t, env.addr, userLogin(first.ResumeToken))
	if !resp.Standby {
		t.Fatalf("standby reply = %+v", resp)
	}
	expectClosed(t, standby, "an expired standby")
	if _, ok := env.state.GetClient(first.ClientID); !ok {
		t.Fatal("an expired standby ended the session")
	}
	send(t, old, netproto.MsgPing, netproto.Ping{})
	readOfType(t, old, netproto.MsgPong)
	// Nothing was spent: the token still starts a new handover.
	again, retry := dialHandover(t, env.addr, userLogin(first.ResumeToken))
	defer func() { _ = again.Close() }()
	if !retry.Standby {
		t.Fatalf("a timed-out standby consumed the token: %+v", retry)
	}
}

func TestHandoverRejections(t *testing.T) {
	env := startTestEnv(t, nil)
	defer env.stop()
	old, first := dialResumable(t, env.addr, userLogin(""))

	pending, resp := dialHandover(t, env.addr, userLogin(first.ResumeToken))
	defer func() { _ = pending.Close() }()
	if !resp.Standby {
		t.Fatalf("standby reply = %+v", resp)
	}
	for name, request := range map[string]netproto.Authenticate{
		"second attempt": userLogin(first.ResumeToken),
		"other account":  {Username: "admin-uid", Password: "pw", ResumeToken: first.ResumeToken},
		"no token":       userLogin(""),
	} {
		conn, refused := dialHandover(t, env.addr, request)
		if refused.OK || refused.Reason != netproto.ReasonHandoverUnavailable {
			t.Fatalf("%s: reply %+v, want refusal", name, refused)
		}
		expectClosed(t, conn, name)
	}
	// A standby cannot authenticate again on its connection.
	send(t, pending, netproto.MsgAuthenticate, userLogin(first.ResumeToken))
	expectClosed(t, pending, "a re-authenticating standby")
	// The old session leaves deliberately while a standby waits: the commit
	// must not turn into a fresh session.
	var standby net.Conn
	waitFor(t, "standby released", func() bool {
		conn, again := dialHandover(t, env.addr, userLogin(first.ResumeToken))
		if again.Standby {
			standby = conn
			return true
		}
		_ = conn.Close()
		return false
	})
	defer func() { _ = standby.Close() }()
	send(t, old, netproto.MsgGoodbye, netproto.Goodbye{})
	goodbyeSeen(t, env, first.ClientID)
	_ = old.Close()
	waitFor(t, "session gone", func() bool { _, ok := env.state.GetClient(first.ClientID); return !ok })
	authed := env.srv.authenticatedClientCount()
	send(t, standby, netproto.MsgHandoverCommit, struct{}{})
	var refused netproto.AuthResponse
	if err := netproto.Decode(readOfType(t, standby, netproto.MsgAuthResponse), &refused); err != nil || refused.OK || refused.Reason != netproto.ReasonHandoverUnavailable {
		t.Fatalf("commit after leave = %+v %v", refused, err)
	}
	expectClosed(t, standby, "a refused commit")
	if env.srv.authenticatedClientCount() != authed {
		t.Fatal("a refused commit published a session")
	}
}

func TestResumeStoreHandoverMarksWithoutSpending(t *testing.T) {
	var r resumeStore
	owner, standby, other := &Client{ID: "c-1"}, &Client{ID: "s-1"}, &Client{ID: "s-2"}
	token := r.issue(owner, "alice", "alice|k")
	if _, ok := r.beginHandover(token, "alice|other", standby); ok {
		t.Fatal("a standby was accepted for another device")
	}
	if id, ok := r.beginHandover(token, "alice|k", standby); !ok || id != "c-1" {
		t.Fatalf("beginHandover = %q %v", id, ok)
	}
	if _, ok := r.beginHandover(token, "alice|k", other); ok {
		t.Fatal("a second standby was accepted while one is pending")
	}
	r.endHandover("c-1", other) // another standby cannot release the mark
	if _, ok := r.beginHandover(token, "alice|k", other); ok {
		t.Fatal("endHandover must check the standby")
	}
	r.endHandover("c-1", standby)
	if _, ok := r.beginHandover(token, "alice|k", other); !ok {
		t.Fatal("a released mark must allow a new standby")
	}
	if id, got, ok := r.claim(token, "alice|k"); !ok || id != "c-1" || got != owner {
		t.Fatal("a standby must not spend the token")
	}
	r.park(owner, &resumeParked{clientID: "c-1"}, time.Now())
	if _, ok := r.beginHandover(token, "alice|k", standby); ok {
		t.Fatal("a claimed session accepted a standby")
	}
}

func TestHandoverStandbyAcceptsOnlyTheCommit(t *testing.T) {
	env := startTestEnv(t, nil)
	defer env.stop()
	old, first := dialResumable(t, env.addr, userLogin(""))
	defer func() { _ = old.Close() }()
	for name, request := range map[string]struct {
		mt  netproto.MessageType
		msg any
	}{
		"device request": {netproto.MsgDeviceRequest, struct{}{}},
		"auth signature": {netproto.MsgAuthSignature, netproto.AuthSignature{UniqueID: "user-uid"}},
		"channel join":   {netproto.MsgJoinChannel, netproto.JoinChannel{ChannelID: 1}},
	} {
		var standby net.Conn
		waitFor(t, "standby for "+name, func() bool {
			conn, resp := dialHandover(t, env.addr, userLogin(first.ResumeToken))
			if resp.Standby {
				standby = conn
				return true
			}
			_ = conn.Close()
			return false
		})
		authed := env.srv.authenticatedClientCount()
		send(t, standby, request.mt, request.msg)
		expectClosed(t, standby, name+" on a standby")
		if env.srv.authenticatedClientCount() != authed {
			t.Fatalf("%s published a session from a standby", name)
		}
	}
	if c, ok := env.state.GetClient(first.ClientID); !ok || c.ClientID != first.ClientID {
		t.Fatal("refused standby requests touched the live session")
	}
}

func TestHandoverCommitRefusedByAdmissionKeepsOldSession(t *testing.T) {
	env := startTestEnvFull(t, nil, func(c *config.Config) { c.MaxClients = 1 })
	defer env.stop()
	old, first := dialResumable(t, env.addr, userLogin(""))
	defer func() { _ = old.Close() }()
	standby, resp := dialHandover(t, env.addr, userLogin(first.ResumeToken))
	defer func() { _ = standby.Close() }()
	if !resp.Standby {
		t.Fatalf("standby reply = %+v", resp)
	}
	env.auth.setBan("user-uid", &auth.Ban{ID: 9, Type: 1, Value: "user-uid", Reason: "spam"})
	send(t, standby, netproto.MsgHandoverCommit, struct{}{})
	var refused netproto.AuthResponse
	if err := netproto.Decode(readOfType(t, standby, netproto.MsgAuthResponse), &refused); err != nil || refused.OK {
		t.Fatalf("banned commit = %+v %v", refused, err)
	}
	expectClosed(t, standby, "a refused commit")
	send(t, old, netproto.MsgPing, netproto.Ping{})
	readOfType(t, old, netproto.MsgPong)
	if _, ok := env.state.GetClient(first.ClientID); !ok {
		t.Fatal("a refused commit closed the old session")
	}

	// Lifting the ban: at the configured limit of one, the commit still
	// takes over its own session's slot.
	env.auth.setBan("user-uid", nil)
	again, retry := dialHandover(t, env.addr, userLogin(first.ResumeToken))
	defer func() { _ = again.Close() }()
	if !retry.Standby {
		t.Fatalf("standby after refusal = %+v", retry)
	}
	send(t, again, netproto.MsgHandoverCommit, struct{}{})
	var done netproto.AuthResponse
	if err := netproto.Decode(readOfType(t, again, netproto.MsgAuthResponse), &done); err != nil || !done.OK || done.ClientID != first.ClientID {
		t.Fatalf("commit at capacity = %+v %v", done, err)
	}
}
