package server

import (
	"net"
	"sync/atomic"
	"testing"

	"noxa/internal/auth"
	"noxa/internal/netproto"
	"noxa/internal/state"
)

func authReply(t *testing.T, conn net.Conn) netproto.AuthResponse {
	t.Helper()
	var resp netproto.AuthResponse
	if err := netproto.Decode(readOfType(t, conn, netproto.MsgAuthResponse), &resp); err != nil {
		t.Fatal(err)
	}
	return resp
}

// A password login that presents someone else's identity key without proving
// it must not bind it, and the key's owner keeps signing in as themselves.
func TestPasswordLoginDoesNotBindUnprovenKey(t *testing.T) {
	env := startTestEnv(t, nil)
	defer env.stop()
	victim := newLinkTestKeys(t)
	attacker := newLinkTestKeys(t)

	conn := dialRetry(t, env.addr)
	defer func() { _ = conn.Close() }()
	send(t, conn, netproto.MsgAuthenticate, netproto.Authenticate{Username: "user-uid", Password: "pw", PublicKey: victim.public})
	resp := authReply(t, conn)
	if !resp.OK {
		t.Fatalf("password login: %s", resp.Reason)
	}
	if key := mustServerClient(t, env, resp.ClientID).sessionKey(); key != "" {
		t.Fatal("an unproven key became the session's signing key")
	}

	// A proof made with another key does not count either.
	forged := dialRetry(t, env.addr)
	defer func() { _ = forged.Close() }()
	sendProvenPasswordAuth(t, forged, netproto.Authenticate{Username: "user-uid", Password: "pw", PublicKey: victim.public}, attacker.private)
	if resp := authReply(t, forged); !resp.OK {
		t.Fatalf("password login with a bad proof: %s", resp.Reason)
	}

	env.auth.mu.Lock()
	bound := len(env.auth.bindings)
	env.auth.mu.Unlock()
	if bound != 0 {
		t.Fatalf("unproven key bound: %v", env.auth.bindings)
	}
	_, guest := keyLogin(t, env.addr, victim)
	if !guest.OK || !guest.Guest || guest.UniqueID != victim.uniqueID {
		t.Fatalf("victim key login = %+v, want its own guest identity", guest)
	}
}

// Before authentication only IP bans are revealed; an account ban is applied
// once the credentials are proven.
func TestAccountBanIsNotRevealedBeforeAuthentication(t *testing.T) {
	env := startTestEnv(t, nil)
	defer env.stop()
	victim := newLinkTestKeys(t)
	env.auth.setBan("user-uid", &auth.Ban{ID: 1, Reason: "spam"})
	env.auth.setBan(victim.uniqueID, &auth.Ban{ID: 2, Reason: "spam"})

	conn := dialRetry(t, env.addr)
	defer func() { _ = conn.Close() }()
	send(t, conn, netproto.MsgAuthenticate, netproto.Authenticate{Username: "user-uid", Password: "wrong"})
	if resp := authReply(t, conn); resp.Reason != "invalid credentials" {
		t.Fatalf("wrong password for a banned account = %q, want invalid credentials", resp.Reason)
	}
	send(t, conn, netproto.MsgAuthenticate, netproto.Authenticate{Username: "user-uid"})
	readOfType(t, conn, netproto.MsgAuthChallenge) // no ban answer before a signature
	send(t, conn, netproto.MsgAuthenticate, netproto.Authenticate{Username: "user-uid", Password: "pw"})
	if resp := authReply(t, conn); resp.OK || resp.Reason != "banned: spam" {
		t.Fatalf("banned account after authentication = %+v", resp)
	}

	// Guest signature path: a bad signature learns nothing, a good one is refused.
	guest := dialRetry(t, env.addr)
	defer func() { _ = guest.Close() }()
	send(t, guest, netproto.MsgAuthenticate, netproto.Authenticate{Username: victim.uniqueID, Anonymous: true})
	readOfType(t, guest, netproto.MsgAuthChallenge)
	send(t, guest, netproto.MsgAuthSignature, netproto.AuthSignature{PublicKey: victim.public, Signature: make([]byte, 64)})
	if resp := authReply(t, guest); resp.Reason != "invalid credentials" {
		t.Fatalf("bad signature for a banned key = %q, want invalid credentials", resp.Reason)
	}
	if resp := keyLoginOn(t, guest, victim); resp.OK || resp.Reason != "banned: spam" {
		t.Fatalf("banned key after its signature = %+v", resp)
	}

	// IP bans still apply before any credential check.
	env.auth.setBan("127.0.0.1", &auth.Ban{ID: 3})
	ipConn := dialRetry(t, env.addr)
	defer func() { _ = ipConn.Close() }()
	send(t, ipConn, netproto.MsgAuthenticate, netproto.Authenticate{Username: "admin-uid", Password: "wrong"})
	if resp := authReply(t, ipConn); resp.Reason != "banned" {
		t.Fatalf("IP-banned login = %q, want banned", resp.Reason)
	}
}

// Signature logins do no KDF work, so saturated KDF slots must not block them.
func TestKeyLoginIgnoresSaturatedKDFSlots(t *testing.T) {
	limiter := auth.NewLoginFailureLimiter(auth.LoginFailureLimiterConfig{MaxConcurrentKDF: 1})
	env := startTestEnvDeps(t, nil, nil, func(d *Deps) { d.LoginLimiter = limiter })
	defer env.stop()
	held, ok := limiter.Reserve(auth.LoginFailureScope("tcp", "busy"))
	if !ok {
		t.Fatal("reservation rejected")
	}
	defer held.Cancel()
	conn, resp := keyLogin(t, env.addr, newLinkTestKeys(t))
	defer func() { _ = conn.Close() }()
	if !resp.OK {
		t.Fatalf("key login with saturated KDF slots: %s", resp.Reason)
	}
}

// A server password that already verified costs no further KDF.
func TestKnownServerPasswordSkipsKDF(t *testing.T) {
	var verifications atomic.Int32
	env := startTestEnvDeps(t, nil, nil, func(d *Deps) {
		d.ServerPasswordHash = "server-hash"
		d.VerifyServerPassword = func(password, _ string) error {
			verifications.Add(1)
			if password != "door" {
				return auth.ErrUserNotFound
			}
			return nil
		}
	})
	defer env.stop()
	for i := 0; i < 3; i++ {
		conn := dialRetry(t, env.addr)
		send(t, conn, netproto.MsgAuthenticate, netproto.Authenticate{Username: "user-uid", Password: "pw", ServerPassword: "door"})
		if resp := authReply(t, conn); !resp.OK {
			t.Fatalf("login %d: %s", i, resp.Reason)
		}
		_ = conn.Close()
	}
	conn := dialRetry(t, env.addr)
	defer func() { _ = conn.Close() }()
	send(t, conn, netproto.MsgAuthenticate, netproto.Authenticate{Username: "user-uid", Password: "pw", ServerPassword: "wrong"})
	if resp := authReply(t, conn); resp.OK {
		t.Fatal("a wrong server password was accepted")
	}
	if got := verifications.Load(); got != 2 {
		t.Fatalf("server password KDF runs = %d, want 2 (first success, wrong password)", got)
	}
}

// Rejoining a password channel with a password that already verified takes
// no KDF slot, so members holding both slots cannot lock others out.
func TestKnownChannelPasswordSkipsKDF(t *testing.T) {
	env := startTestEnv(t, nil)
	defer env.stop()
	hash, err := auth.HashPassword("door")
	if err != nil {
		t.Fatal(err)
	}
	locked := testChannel(1)
	locked.PasswordHash = hash
	env.state.AddChannel(locked)
	env.state.AddChannel(testChannel(2))
	conn, id := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = conn.Close() }()
	send(t, conn, netproto.MsgJoinChannel, netproto.JoinChannel{ChannelID: 1, Password: "door"})
	waitFor(t, "user in channel 1", func() bool { c, ok := env.state.GetClient(id); return ok && c.ChannelID == 1 })
	send(t, conn, netproto.MsgJoinChannel, netproto.JoinChannel{ChannelID: 2})
	waitFor(t, "user in channel 2", func() bool { c, ok := env.state.GetClient(id); return ok && c.ChannelID == 2 })

	for _, holder := range []string{"a", "b"} {
		held, ok := env.srv.channelPasswordLimiter.Reserve(auth.LoginFailureScope("channel", holder))
		if !ok {
			t.Fatal("reservation rejected")
		}
		defer held.Cancel()
	}
	send(t, conn, netproto.MsgJoinChannel, netproto.JoinChannel{ChannelID: 1, Password: "door"})
	waitFor(t, "user back in channel 1", func() bool { c, ok := env.state.GetClient(id); return ok && c.ChannelID == 1 })
}

// An invisible admin stays hidden across a resume; a session that is no
// longer allowed to be invisible comes back online with its message.
func TestSessionResumeKeepsInvisibleOnlyForAdmins(t *testing.T) {
	env := startTestEnv(t, nil)
	defer env.stop()
	resume := func(uid, status string) *state.Client {
		t.Helper()
		login := netproto.Authenticate{Username: uid, Password: "pw"}
		conn, first := dialResumable(t, env.addr, login)
		env.state.SetStatus(first.ClientID, status, "brb")
		_ = conn.Close()
		waitFor(t, "session parked", func() bool { _, ok := env.state.GetClient(first.ClientID); return !ok })
		login.ResumeToken = first.ResumeToken
		back, second := dialResumable(t, env.addr, login)
		t.Cleanup(func() { _ = back.Close() })
		if !second.Resumed {
			t.Fatal("session was not resumed")
		}
		c, _ := env.state.GetClient(second.ClientID)
		if c == nil {
			t.Fatal("resumed session missing")
		}
		return c
	}
	if c := resume("admin-uid", "invisible"); c.Status != "invisible" || c.StatusMessage != "brb" {
		t.Fatalf("resumed admin status = %q %q, want invisible brb", c.Status, c.StatusMessage)
	}
	if c := resume("user-uid", "invisible"); c.Status != "" || c.StatusMessage != "brb" {
		t.Fatalf("resumed non-admin status = %q %q, want online brb", c.Status, c.StatusMessage)
	}
}
