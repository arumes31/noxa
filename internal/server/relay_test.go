package server

import (
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"

	pion "github.com/pion/turn/v5"
	"noxa/internal/authorization"
	"noxa/internal/netproto"
	"noxa/internal/turn"
)

func TestRelayAuthenticatedSessionIsolation(t *testing.T) {
	roles := serverRoleFixture()
	roles.policy.Roles[0].Permissions = []authorization.Capability{authorization.ViewChannel, authorization.Connect}
	authority, err := authorization.NewAuthority(t.Context(), roles, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	relay := turn.NewManager("test", 32, 1<<20)
	defer relay.Close()
	env := startTestEnvDeps(t, nil, nil, func(d *Deps) { d.Authority = authority; d.Relay = relay })
	defer env.stop()
	one, oneID := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = one.Close() }()
	two, twoID := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = two.Close() }()
	other, _ := dialAuthed(t, env.addr, "admin-uid")
	defer func() { _ = other.Close() }()
	refresh := func(conn net.Conn) netproto.RelayResponse {
		t.Helper()
		send(t, conn, netproto.MsgRelayRequest, netproto.RelayRequest{Action: "refresh"})
		var out netproto.RelayResponse
		if err := netproto.Decode(readOfType(t, conn, netproto.MsgRelayResponse), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	a, b := refresh(one), refresh(two)
	if a.SessionID != oneID || b.SessionID != twoID || a.ICEServers[0].Username == b.ICEServers[0].Username {
		t.Fatal("sessions share credentials")
	}
	// Forged identity cannot select another session during issuance.
	send(t, one, netproto.MsgRelayRequest, netproto.RelayRequest{Action: "refresh", SessionID: twoID})
	if readError(t, one).Code != errCodeMalformed {
		t.Fatal("forged identity accepted")
	}
	// Lower-ranked member cannot revoke the owner.
	send(t, other, netproto.MsgRelayRequest, netproto.RelayRequest{Action: "revoke", SessionID: oneID})
	if readError(t, other).Code != errCodePermissionDenied {
		t.Fatal("hierarchy bypass")
	}
	send(t, one, netproto.MsgRelayRequest, netproto.RelayRequest{Action: "revoke", SessionID: oneID})
	readOfType(t, one, netproto.MsgRelayResponse)
	send(t, one, netproto.MsgRelayRequest, netproto.RelayRequest{Action: "refresh"})
	if readError(t, one).Code != errCodePermissionDenied {
		t.Fatal("revoked session refreshed")
	}
	auth := func(lease netproto.RelayResponse) bool {
		_, _, ok := relay.Authenticate(&pion.RequestAttributes{Username: lease.ICEServers[0].Username, Realm: "test"})
		return ok
	}
	if auth(a) || !auth(b) {
		t.Fatal("wrong device revoked")
	}
	refresh(two)
	_ = two.Close()
	deadline := time.Now().Add(time.Second)
	for auth(b) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if auth(b) {
		t.Fatal("disconnect retained relay")
	}
}
func TestRelayRejectsUntrustedPrincipals(t *testing.T) {
	roles := serverRoleFixture()
	e, err := authorization.NewRoleEvaluator(roles.policy)
	if err != nil {
		t.Fatal(err)
	}
	s := &TCPServer{}
	for _, test := range []struct {
		name                  string
		userID                int64
		bot, pending, revoked bool
	}{
		{name: "guest"}, {name: "bot", userID: 1, bot: true}, {name: "rules", userID: 1, pending: true}, {name: "revoked", userID: 1, revoked: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			c := &Client{}
			c.setIdentity("u", "n", test.userID, test.bot)
			c.setRulesPending(test.pending)
			if test.revoked {
				c.revokeSession()
			}
			if s.relayAllowed(e, c) {
				t.Fatal("unauthorized relay")
			}
		})
	}
}

type relayBlockingAudit struct {
	AuditStore
	entered chan struct{}
	release chan struct{}
	calls   atomic.Int32
}

func (a *relayBlockingAudit) AuditScoped(ctx context.Context, _, _, _, _ string, _ []int64) {
	if a.calls.Add(1) == 1 {
		close(a.entered)
	}
	select {
	case <-a.release:
	case <-ctx.Done():
	}
}
func TestRelayRevocationReleasesPolicyBeforeAuditAndDeduplicates(t *testing.T) {
	roles := serverRoleFixture()
	authority, err := authorization.NewAuthority(t.Context(), roles, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	relay := turn.NewManager("test", 32, 1<<20)
	defer relay.Close()
	audit := &relayBlockingAudit{entered: make(chan struct{}), release: make(chan struct{})}
	env := startTestEnvDeps(t, nil, nil, func(d *Deps) { d.Authority = authority; d.Relay = relay; d.Groups = audit })
	defer env.stop()
	conn, id := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = conn.Close() }()
	send(t, conn, netproto.MsgRelayRequest, netproto.RelayRequest{Action: "revoke"})
	select {
	case <-audit.entered:
	case <-time.After(time.Second):
		t.Fatal("audit not reached")
	}
	// Both realtime readers and policy mutations must proceed during slow I/O.
	readErr := authority.TryWithPolicy(func(*authorization.RoleEvaluator) error { return nil })
	ctx, cancel := context.WithTimeout(t.Context(), 250*time.Millisecond)
	writeErr := authority.WithExclusivePolicy(ctx, func(*authorization.RoleEvaluator) error { return nil })
	cancel()
	close(audit.release)
	if readErr != nil || writeErr != nil {
		t.Fatalf("audit retained policy lease: read=%v write=%v", readErr, writeErr)
	}
	readOfType(t, conn, netproto.MsgRelayResponse)
	send(t, conn, netproto.MsgRelayRequest, netproto.RelayRequest{Action: "revoke"})
	if readError(t, conn).Message != "relay revocation rate limit exceeded" {
		t.Fatal("unbounded revocation requests")
	}
	client, _ := env.srv.clientByID(id)
	client.mu.Lock()
	client.relayRevokeAfter = time.Time{}
	client.mu.Unlock()
	send(t, conn, netproto.MsgRelayRequest, netproto.RelayRequest{Action: "revoke"})
	readOfType(t, conn, netproto.MsgRelayResponse)
	if audit.calls.Load() != 1 {
		t.Fatal("no-op revocation wrote another audit row")
	}
}
