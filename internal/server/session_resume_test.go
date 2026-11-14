package server

import (
	"encoding/json"
	"net"
	"slices"
	"testing"
	"time"

	"noxa/internal/auth"
	"noxa/internal/broadcast"
	"noxa/internal/netproto"
	"noxa/internal/state"
)

// --- store ------------------------------------------------------------------

func TestResumeStoreTokenIsBoundSingleUseAndRotated(t *testing.T) {
	var r resumeStore
	owner := &Client{ID: "c-1"}
	token := r.issue(owner, "alice", resumeBinding("alice", "key-a"))
	if token == "" {
		t.Fatal("no token issued")
	}
	if !r.park(owner, &resumeParked{clientID: "c-1", uniqueID: "alice", channelID: 7}, time.Now()) {
		t.Fatal("a session holding a token must park")
	}
	for _, binding := range []string{resumeBinding("alice", "key-b"), resumeBinding("mallory", "key-a"), resumeBinding("guest:x", "")} {
		if _, _, ok := r.claim(token, binding); ok {
			t.Fatalf("token honored for binding %q", binding)
		}
	}
	if _, _, ok := r.claim(token+"x", resumeBinding("alice", "key-a")); ok {
		t.Fatal("a malformed token was honored")
	}
	id, owner2, ok := r.claim(token, resumeBinding("alice", "key-a"))
	if !ok || id != "c-1" || owner2 != owner {
		t.Fatalf("claim = %q %v %v, want c-1 after mismatches left it untouched", id, owner2, ok)
	}
	if parked := r.takeParked("c-1"); parked == nil || parked.channelID != 7 {
		t.Fatalf("parked state = %+v", parked)
	}
	if _, _, ok := r.claim(token, resumeBinding("alice", "key-a")); ok {
		t.Fatal("a token must be single use")
	}
	resumed := &Client{ID: "c-1"}
	rotated := r.issue(resumed, "alice", resumeBinding("alice", "key-a"))
	if rotated == "" || rotated == token {
		t.Fatal("a resume must rotate the token")
	}
	if r.len() != 1 {
		t.Fatalf("store holds %d entries, want 1", r.len())
	}
}

func TestResumeStoreExpiresAfterGraceWindow(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	r := resumeStore{now: func() time.Time { return now }}
	owner := &Client{ID: "c-1"}
	token := r.issue(owner, "alice", "alice|k")
	at := now
	r.park(owner, &resumeParked{clientID: "c-1"}, at)
	if r.expire("c-1", at.Add(time.Second)) {
		t.Fatal("a timer of another park must not expire this one")
	}
	now = at.Add(resumeGraceWindow + time.Second)
	if _, _, ok := r.claim(token, "alice|k"); ok {
		t.Fatal("a token is honored after the grace window")
	}
	if r.len() != 0 {
		t.Fatal("an expired claim must drop the entry")
	}

	token = r.issue(owner, "alice", "alice|k")
	r.park(owner, &resumeParked{clientID: "c-1"}, now)
	if !r.expire("c-1", now) || r.expire("c-1", now) {
		t.Fatal("expiry must end a parked entry exactly once")
	}
	if _, _, ok := r.claim(token, "alice|k"); ok {
		t.Fatal("an expired entry was resumed")
	}
}

func TestResumeStoreRevocationAndBound(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	r := resumeStore{now: func() time.Time { return now }}
	a, b := &Client{ID: "a"}, &Client{ID: "b"}
	tokenA := r.issue(a, "alice", "alice|1")
	r.issue(b, "bob", "bob|1")
	r.revoke(&Client{ID: "a"}) // another session with the same ID cannot revoke it
	if r.len() != 2 {
		t.Fatal("revoke must check the owner")
	}
	r.revoke(a)
	if _, _, ok := r.claim(tokenA, "alice|1"); ok {
		t.Fatal("a revoked token was honored")
	}
	r.park(b, &resumeParked{}, now)
	r.revokeAccount("bob")
	if r.len() != 0 {
		t.Fatal("revokeAccount must drop parked entries")
	}

	// Full of parked entries: expired ones make room.
	for i := 0; i < resumeTokenMax; i++ {
		c := &Client{ID: "p" + string(rune(i))}
		r.issue(c, "x", "x|")
		r.park(c, &resumeParked{}, now)
	}
	now = now.Add(resumeGraceWindow + time.Second)
	if r.issue(&Client{ID: "fresh"}, "x", "x|") == "" || r.len() != 1 {
		t.Fatalf("expired entries were not evicted: %d", r.len())
	}
	for i := 1; i < resumeTokenMax; i++ {
		r.issue(&Client{ID: "l" + string(rune(i))}, "x", "x|")
	}
	if r.issue(&Client{ID: "over"}, "x", "x|") != "" {
		t.Fatal("the store grew past its bound")
	}
}

func TestResumeBindingEphemeralGuest(t *testing.T) {
	if resumeBinding("guest:aa", "") != resumeBinding("guest:bb", "") {
		t.Fatal("ephemeral guests have no stable identity; the token is their secret")
	}
	if resumeBinding("guest:aa", "") == resumeBinding("alice", "") || resumeBinding("keyguest", "k1") == resumeBinding("keyguest", "k2") {
		t.Fatal("binding must separate accounts and keys")
	}
}

func TestPresenceKeepsReconnectingPlaceWithItsOwnLabel(t *testing.T) {
	var p presenceTracker
	now := time.Unix(1_000_000, 0)
	p.noteDeparture("c-1", "alice|dev", netproto.DepartureReconnecting, 7, false, now)
	p.noteUpdating("alice|dev", broadcast.ClientInfo{ClientID: "c-1", ChannelID: 7}, netproto.DepartureReconnecting, false, now)
	snap := updatingSnapshot()
	p.decorate(snap, func(int64) bool { return true }, "bob|dev", now.Add(90*time.Second))
	if c := snap.RootChannels[0].Clients; len(c) != 1 || !c[0].Reconnecting || c[0].Updating || len(snap.Departures) != 0 {
		t.Fatalf("kept place = %+v departures %v, want a quiet reconnecting badge", c, snap.Departures)
	}
	p.dropKept("alice|dev", "c-1")
	after := updatingSnapshot(&broadcast.ClientInfo{ClientID: "c-1", ChannelID: 7})
	p.decorate(after, func(int64) bool { return true }, "bob|dev", now.Add(91*time.Second))
	if len(after.RootChannels[0].Clients) != 1 || after.RootChannels[0].Clients[0].Reconnecting || len(after.Departures) != 0 || len(after.Reconnected) != 0 || len(after.Returned) != 0 {
		t.Fatalf("after resume: %+v %v %v %v", after.RootChannels[0].Clients, after.Departures, after.Reconnected, after.Returned)
	}
}

// --- end to end -----------------------------------------------------------

// dialResumable logs in like a resume-capable desktop and returns the reply.
func dialResumable(t *testing.T, addr string, request netproto.Authenticate) (net.Conn, netproto.AuthResponse) {
	t.Helper()
	conn := dialRetry(t, addr)
	request.ClientFeatures = []string{netproto.ClientFeatureGoodbye, netproto.ClientFeatureResume}
	send(t, conn, netproto.MsgAuthenticate, request)
	var resp netproto.AuthResponse
	if err := netproto.Decode(readOfType(t, conn, netproto.MsgAuthResponse), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.OK {
		t.Fatalf("login failed: %s", resp.Reason)
	}
	readOfType(t, conn, netproto.MsgSnapshot)
	return conn, resp
}

func userLogin(token string) netproto.Authenticate {
	return netproto.Authenticate{Username: "user-uid", Password: "pw", ResumeToken: token}
}

// snapshotMember finds clientID in a snapshot.
func snapshotMember(s *broadcast.TreeSnapshot, clientID string) *broadcast.ClientInfo {
	var find func([]*broadcast.ChannelNode) *broadcast.ClientInfo
	find = func(nodes []*broadcast.ChannelNode) *broadcast.ClientInfo {
		for _, node := range nodes {
			for _, c := range node.Clients {
				if c.ClientID == clientID {
					return c
				}
			}
			if c := find(node.Children); c != nil {
				return c
			}
		}
		return nil
	}
	if c := find(s.RootChannels); c != nil {
		return c
	}
	for _, c := range s.UnassignedClients {
		if c.ClientID == clientID {
			return c
		}
	}
	return nil
}

// awaitSnapshot reads the observer's snapshots until cond holds. Every
// snapshot passed over is handed to seen, so a test can assert that no
// intermediate one announced anything.
func awaitSnapshot(t *testing.T, conn net.Conn, what string, cond func(*broadcast.TreeSnapshot) bool, seen func(*broadcast.TreeSnapshot)) {
	t.Helper()
	deadline := time.Now().Add(waitDeadline)
	for time.Now().Before(deadline) {
		f := readFrame(t, conn)
		if netproto.MessageType(f.Type) != netproto.MsgSnapshot {
			continue
		}
		var snap broadcast.TreeSnapshot
		if err := json.Unmarshal(f.Payload, &snap); err != nil {
			t.Fatal(err)
		}
		if seen != nil {
			seen(&snap)
		}
		if cond(&snap) {
			return
		}
	}
	t.Fatalf("observer never saw %s", what)
}

func quietFor(t *testing.T, id string) func(*broadcast.TreeSnapshot) {
	return func(s *broadcast.TreeSnapshot) {
		t.Helper()
		if reason, ok := s.Departures[id]; ok || slices.Contains(s.Reconnected, id) || slices.Contains(s.Returned, id) {
			t.Fatalf("a resumed session was announced: departure %q reconnected %v returned %v", reason, s.Reconnected, s.Returned)
		}
	}
}

func TestSessionResumeKeepsClientIDChannelAndVoiceStateQuietly(t *testing.T) {
	env := startTestEnv(t, nil)
	defer env.stop()
	env.state.AddChannel(testChannel(1))
	observer, _ := dialAuthed(t, env.addr, "admin-uid")
	defer func() { _ = observer.Close() }()
	send(t, observer, netproto.MsgJoinChannel, netproto.JoinChannel{ChannelID: 1})

	user, first := dialResumable(t, env.addr, userLogin(""))
	if first.ResumeToken == "" || first.Resumed {
		t.Fatalf("login reply: token %q resumed %v", first.ResumeToken, first.Resumed)
	}
	id := first.ClientID
	send(t, user, netproto.MsgJoinChannel, netproto.JoinChannel{ChannelID: 1})
	waitFor(t, "user in channel", func() bool { c, ok := env.state.GetClient(id); return ok && c.ChannelID == 1 })
	env.state.SetAudioState(id, true, false)
	muted := true
	_, _ = env.state.SetServerVoiceState(id, &muted, nil)
	env.voice.mu.Lock()
	env.voice.watchPairs = func(clientID string) ([][2]string, [][2]string) {
		if clientID != id {
			return nil, nil
		}
		return [][2]string{{"c-publisher", "screen"}}, [][2]string{{"c-viewer", "cam"}}
	}
	env.voice.mu.Unlock()

	_ = user.Close() // a drop: no goodbye although the client promised one
	awaitSnapshot(t, observer, "a reconnecting badge", func(s *broadcast.TreeSnapshot) bool {
		m := snapshotMember(s, id)
		return m != nil && m.Reconnecting && !m.Updating && m.ChannelID == 1
	}, quietFor(t, id))

	resumed, second := dialResumable(t, env.addr, userLogin(first.ResumeToken))
	defer func() { _ = resumed.Close() }()
	if !second.Resumed || second.ClientID != id {
		t.Fatalf("resume reply: resumed %v client %q, want %q", second.Resumed, second.ClientID, id)
	}
	if second.ResumeToken == "" || second.ResumeToken == first.ResumeToken {
		t.Fatal("the resume must rotate the token")
	}
	c, ok := env.state.GetClient(id)
	if !ok || c.ChannelID != 1 || !c.SelfMuted || !c.ServerMuted {
		t.Fatalf("resumed state = %+v, want channel 1, self and server muted", c)
	}
	awaitSnapshot(t, observer, "the badge clear", func(s *broadcast.TreeSnapshot) bool {
		m := snapshotMember(s, id)
		return m != nil && !m.Reconnecting && m.ChannelID == 1
	}, quietFor(t, id))
	if watch := readEventOfType(t, resumed, eventStreamWatchResume); string(watch) != `{"publisher_id":"c-publisher","slot":"screen"}` {
		t.Fatalf("watch resume = %s", watch)
	}
	// The client rejoins its channel as usual: acknowledged without a move.
	send(t, resumed, netproto.MsgJoinChannel, netproto.JoinChannel{ChannelID: 1, AckRequested: true})
	readOfType(t, resumed, netproto.MsgChannelJoined)
	if c, _ := env.state.GetClient(id); c.ChannelID != 1 {
		t.Fatal("the rejoin moved the session")
	}
	env.voice.mu.Lock()
	joined := slices.ContainsFunc(env.voice.joins, func(j [2]any) bool { return j[0] == id && j[1] == int64(1) })
	env.voice.mu.Unlock()
	if !joined {
		t.Fatal("the voice router did not get the restored membership")
	}

	// The spent token cannot be replayed.
	again, third := dialResumable(t, env.addr, userLogin(first.ResumeToken))
	defer func() { _ = again.Close() }()
	if third.Resumed || third.ClientID == id {
		t.Fatal("a spent token resumed a session")
	}
}

func TestSessionResumeAfterUpdateGoodbyeShowsUpdating(t *testing.T) {
	env := startTestEnv(t, nil)
	defer env.stop()
	env.state.AddChannel(testChannel(1))
	observer, _ := dialAuthed(t, env.addr, "admin-uid")
	defer func() { _ = observer.Close() }()
	send(t, observer, netproto.MsgJoinChannel, netproto.JoinChannel{ChannelID: 1})
	user, first := dialResumable(t, env.addr, userLogin(""))
	send(t, user, netproto.MsgJoinChannel, netproto.JoinChannel{ChannelID: 1})
	waitFor(t, "user in channel", func() bool { c, ok := env.state.GetClient(first.ClientID); return ok && c.ChannelID == 1 })
	send(t, user, netproto.MsgGoodbye, netproto.Goodbye{Reason: netproto.GoodbyeReasonUpdate})
	goodbyeSeen(t, env, first.ClientID)
	_ = user.Close()
	awaitSnapshot(t, observer, "an updating badge", func(s *broadcast.TreeSnapshot) bool {
		m := snapshotMember(s, first.ClientID)
		return m != nil && m.Updating && !m.Reconnecting
	}, quietFor(t, first.ClientID))
	resumed, second := dialResumable(t, env.addr, userLogin(first.ResumeToken))
	defer func() { _ = resumed.Close() }()
	if !second.Resumed || second.ClientID != first.ClientID {
		t.Fatal("an updated client must resume its session")
	}
}

func TestSessionResumeRejectsAnotherIdentity(t *testing.T) {
	env := startTestEnv(t, nil)
	defer env.stop()
	user, first := dialResumable(t, env.addr, userLogin(""))
	_ = user.Close()
	waitFor(t, "session parked", func() bool { _, ok := env.state.GetClient(first.ClientID); return !ok })
	other, resp := dialResumable(t, env.addr, netproto.Authenticate{Username: "admin-uid", Password: "pw", ResumeToken: first.ResumeToken})
	defer func() { _ = other.Close() }()
	if resp.Resumed || resp.ClientID == first.ClientID {
		t.Fatal("another account resumed the session")
	}
	// The rightful owner can still resume: a mismatch does not spend it.
	back, again := dialResumable(t, env.addr, userLogin(first.ResumeToken))
	defer func() { _ = back.Close() }()
	if !again.Resumed || again.ClientID != first.ClientID {
		t.Fatal("the owner lost its resume to a mismatched attempt")
	}
}

func TestExplicitLeaveAndKickRevokeResumeToken(t *testing.T) {
	env := startTestEnv(t, nil)
	defer env.stop()
	user, first := dialResumable(t, env.addr, userLogin(""))
	send(t, user, netproto.MsgGoodbye, netproto.Goodbye{})
	goodbyeSeen(t, env, first.ClientID)
	_ = user.Close()
	waitFor(t, "session gone", func() bool { _, ok := env.state.GetClient(first.ClientID); return !ok })
	waitFor(t, "token revoked", func() bool { return env.srv.resume.len() == 0 })
	back, resp := dialResumable(t, env.addr, userLogin(first.ResumeToken))
	if resp.Resumed {
		t.Fatal("a plain leave stayed resumable")
	}

	admin, _ := dialAuthed(t, env.addr, "admin-uid")
	defer func() { _ = admin.Close() }()
	send(t, admin, netproto.MsgKickClient, netproto.KickClient{ClientID: resp.ClientID, FromServer: true})
	waitFor(t, "kicked session gone", func() bool { _, ok := env.state.GetClient(resp.ClientID); return !ok })
	_ = back.Close()
	waitFor(t, "kicked token revoked", func() bool { return env.srv.resume.len() == 1 }) // the admin's own
	again, after := dialResumable(t, env.addr, userLogin(resp.ResumeToken))
	defer func() { _ = again.Close() }()
	if after.Resumed {
		t.Fatal("a kicked session was resumed")
	}
}

func TestSessionResumeExpiresIntoLeave(t *testing.T) {
	env := startTestEnv(t, nil)
	defer env.stop()
	user, first := dialResumable(t, env.addr, userLogin(""))
	_ = user.Close()
	var at time.Time
	var key string
	waitFor(t, "session parked", func() bool {
		env.srv.resume.mu.Lock()
		defer env.srv.resume.mu.Unlock()
		entry := env.srv.resume.byClient[first.ClientID]
		if entry == nil || entry.parked == nil {
			return false
		}
		at = entry.at
		return true
	})
	env.srv.presence.mu.Lock()
	for k, kept := range env.srv.presence.updating {
		if kept.ghost.ClientID == first.ClientID {
			key = k
		}
	}
	env.srv.presence.mu.Unlock()
	if key == "" {
		t.Fatal("no kept place for the parked session")
	}
	env.srv.expireUpdatingDeparture(key, first.ClientID, "user-uid", "user", at)
	if env.srv.resume.len() != 0 {
		t.Fatal("the grace window ended but the token is still held")
	}
	back, resp := dialResumable(t, env.addr, userLogin(first.ResumeToken))
	defer func() { _ = back.Close() }()
	if resp.Resumed {
		t.Fatal("resumed after the grace window")
	}
}

func TestSessionResumeTakesOverHalfOpenConnection(t *testing.T) {
	env := startTestEnv(t, nil)
	defer env.stop()
	env.state.AddChannel(testChannel(1))
	observer, _ := dialAuthed(t, env.addr, "admin-uid")
	defer func() { _ = observer.Close() }()
	send(t, observer, netproto.MsgJoinChannel, netproto.JoinChannel{ChannelID: 1})
	old, first := dialResumable(t, env.addr, userLogin(""))
	defer func() { _ = old.Close() }()
	send(t, old, netproto.MsgJoinChannel, netproto.JoinChannel{ChannelID: 1})
	waitFor(t, "user in channel", func() bool { c, ok := env.state.GetClient(first.ClientID); return ok && c.ChannelID == 1 })

	// The old socket is still open (half-open from the client's view).
	resumed, second := dialResumable(t, env.addr, userLogin(first.ResumeToken))
	defer func() { _ = resumed.Close() }()
	if !second.Resumed || second.ClientID != first.ClientID {
		t.Fatal("a resume must take over a half-open connection")
	}
	if c, ok := env.state.GetClient(first.ClientID); !ok || c.ChannelID != 1 {
		t.Fatal("the takeover lost the channel")
	}
	awaitSnapshot(t, observer, "the member back in channel 1", func(s *broadcast.TreeSnapshot) bool {
		m := snapshotMember(s, first.ClientID)
		return m != nil && !m.Reconnecting && m.ChannelID == 1
	}, quietFor(t, first.ClientID))
	_ = old.SetReadDeadline(time.Now().Add(waitDeadline))
	for {
		if _, err := netproto.ReadFrame(old); err != nil {
			break // closed by the takeover
		}
	}
	if _, ok := env.srv.clientByID(first.ClientID); !ok {
		t.Fatal("the old connection's cleanup unregistered the resumed session")
	}
}

func TestEphemeralGuestResumesItsIdentity(t *testing.T) {
	env := startTestEnv(t, nil)
	defer env.stop()
	guest, first := dialResumable(t, env.addr, netproto.Authenticate{Anonymous: true, Nickname: "visitor"})
	if !first.Guest || first.ResumeToken == "" {
		t.Fatalf("guest login: guest %v token %q", first.Guest, first.ResumeToken)
	}
	_ = guest.Close()
	waitFor(t, "guest parked", func() bool { _, ok := env.state.GetClient(first.ClientID); return !ok })
	back, second := dialResumable(t, env.addr, netproto.Authenticate{Anonymous: true, Nickname: "visitor", ResumeToken: first.ResumeToken})
	defer func() { _ = back.Close() }()
	if !second.Resumed || second.ClientID != first.ClientID || second.UniqueID != first.UniqueID {
		t.Fatalf("guest resume: %+v, want client %q identity %q", second, first.ClientID, first.UniqueID)
	}
}

func TestLegacyClientKeepsLostConnectionAnnouncement(t *testing.T) {
	env := startTestEnv(t, nil)
	defer env.stop()
	conn := dialRetry(t, env.addr)
	send(t, conn, netproto.MsgAuthenticate, netproto.Authenticate{Username: "user-uid", Password: "pw", ClientFeatures: []string{netproto.ClientFeatureGoodbye}})
	var resp netproto.AuthResponse
	if err := netproto.Decode(readOfType(t, conn, netproto.MsgAuthResponse), &resp); err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	waitFor(t, "token revoked", func() bool { return env.srv.resume.len() == 0 })
	env.srv.presence.mu.Lock()
	defer env.srv.presence.mu.Unlock()
	if len(env.srv.presence.updating) != 0 || env.srv.presence.departures[resp.ClientID].reason != netproto.DepartureConnectionLost {
		t.Fatalf("a client without the resume feature must keep the lost-connection cue: %v", env.srv.presence.departures)
	}
}

// A resumed session is restored into a password channel it had entered and
// rejoins it without the password (clients never keep it), while a changed
// password keeps it out.
func TestSessionResumeKeepsPasswordChannelOnlyWhileUnchanged(t *testing.T) {
	env := startTestEnv(t, nil)
	defer env.stop()
	hash, err := auth.HashPassword("door")
	if err != nil {
		t.Fatal(err)
	}
	locked := testChannel(1)
	locked.PasswordHash = hash
	env.state.AddChannel(locked)
	user, first := dialResumable(t, env.addr, userLogin(""))
	send(t, user, netproto.MsgJoinChannel, netproto.JoinChannel{ChannelID: 1, Password: "door"})
	waitFor(t, "user in channel", func() bool { c, ok := env.state.GetClient(first.ClientID); return ok && c.ChannelID == 1 })
	_ = user.Close()
	waitFor(t, "session parked", func() bool { _, ok := env.state.GetClient(first.ClientID); return !ok })
	resumed, second := dialResumable(t, env.addr, userLogin(first.ResumeToken))
	if c, _ := env.state.GetClient(second.ClientID); c.ChannelID != 1 {
		t.Fatal("the unchanged password channel was not restored")
	}
	send(t, resumed, netproto.MsgJoinChannel, netproto.JoinChannel{ChannelID: 1, AckRequested: true})
	readOfType(t, resumed, netproto.MsgChannelJoined)

	_ = resumed.Close()
	waitFor(t, "session parked again", func() bool { _, ok := env.state.GetClient(second.ClientID); return !ok })
	changed, err := auth.HashPassword("new door")
	if err != nil {
		t.Fatal(err)
	}
	env.state.UpdateChannel(1, state.ChannelUpdate{PasswordHash: &changed})
	back, third := dialResumable(t, env.addr, userLogin(second.ResumeToken))
	defer func() { _ = back.Close() }()
	if c, _ := env.state.GetClient(third.ClientID); !third.Resumed || c.ChannelID != 0 {
		t.Fatalf("after a password change: resumed %v channel %d, want the lobby", third.Resumed, c.ChannelID)
	}
}

// goodbyeSeen waits until the server read the goodbye: closing with unread
// data can reset the socket and lose it, which a real client avoids by
// draining before it closes.
func goodbyeSeen(t *testing.T, env *testEnv, clientID string) {
	t.Helper()
	waitFor(t, "goodbye received", func() bool {
		c, ok := env.srv.clientByID(clientID)
		return ok && c.saidGoodbye.Load()
	})
}

// A channel join shows the member as connecting until the SFU reports its
// peer connected; a lost peer sets it again, a resume and a kept place too.
func TestVoiceConnectingFlipsOnJoinPeerAndResume(t *testing.T) {
	env := startTestEnv(t, nil)
	defer env.stop()
	env.state.AddChannel(testChannel(1))
	observer, _ := dialAuthed(t, env.addr, "admin-uid")
	defer func() { _ = observer.Close() }()
	user, first := dialResumable(t, env.addr, userLogin(""))
	id := first.ClientID
	connecting := func(want bool) func(*broadcast.TreeSnapshot) bool {
		return func(s *broadcast.TreeSnapshot) bool {
			m := snapshotMember(s, id)
			return m != nil && m.ChannelID == 1 && m.Connecting == want
		}
	}
	send(t, user, netproto.MsgJoinChannel, netproto.JoinChannel{ChannelID: 1})
	awaitSnapshot(t, observer, "connecting after the join", connecting(true), nil)
	env.srv.onPeerState(id, true)
	awaitSnapshot(t, observer, "connected once the peer is", connecting(false), nil)
	env.srv.onPeerState(id, false)
	awaitSnapshot(t, observer, "connecting again after a lost peer", connecting(true), nil)
	env.srv.onPeerState(id, true)
	awaitSnapshot(t, observer, "connected again", connecting(false), nil)

	_ = user.Close()
	awaitSnapshot(t, observer, "a kept place connecting", func(s *broadcast.TreeSnapshot) bool {
		m := snapshotMember(s, id)
		return m != nil && m.Reconnecting && m.Connecting
	}, nil)
	resumed, second := dialResumable(t, env.addr, userLogin(first.ResumeToken))
	defer func() { _ = resumed.Close() }()
	if !second.Resumed {
		t.Fatal("not resumed")
	}
	awaitSnapshot(t, observer, "a resumed member connecting", func(s *broadcast.TreeSnapshot) bool {
		m := snapshotMember(s, id)
		return m != nil && !m.Reconnecting && m.Connecting
	}, nil)
	env.srv.onPeerState(id, true)
	awaitSnapshot(t, observer, "the resumed peer connected", connecting(false), nil)
	if env.srv.voiceConnectingNow("nobody") {
		t.Fatal("unknown sessions are never connecting")
	}
}

func TestVoiceConnectingEndsAfterGraceAndOnLeave(t *testing.T) {
	var s TCPServer
	s.markVoiceConnecting("c-1", false)
	at := s.connecting.since["c-1"]
	s.clearVoiceConnecting("c-1", at.Add(time.Second)) // another mark's timer
	if !s.voiceConnectingNow("c-1") {
		t.Fatal("a stale grace timer ended a newer mark")
	}
	s.clearVoiceConnecting("c-1", at)
	if s.voiceConnectingNow("c-1") {
		t.Fatal("the grace did not end the mark of a member without voice")
	}
	s.markVoiceConnecting("c-2", false)
	s.forgetVoiceConnecting("c-2")
	if s.voiceConnectingNow("c-2") {
		t.Fatal("leaving the channel must drop the mark")
	}
}
