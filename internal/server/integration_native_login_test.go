//go:build integration

package server

import (
	"context"
	"net"
	"testing"
	"time"

	"go.uber.org/zap"
	"noxa/internal/auth"
	"noxa/internal/authorization"
	"noxa/internal/netproto"
)

// An integration account with a scope ceiling gets no member session; one
// without a ceiling, and an ordinary member, sign in as before.
func TestIntegrationScopeCeilingRefusesMemberSession(t *testing.T) {
	db := integrationManagementStore(t)
	a := auth.New(db, zap.NewNop())
	const password = "integration-password"
	register := func(name string) *auth.User {
		t.Helper()
		uid, err := a.RegisterUser(t.Context(), name, password)
		if err != nil {
			t.Fatal(err)
		}
		u, err := a.LookupUser(t.Context(), uid)
		if err != nil {
			t.Fatal(err)
		}
		return u
	}
	owner, scoped, empty, full, member := register("owner"), register("bot-scoped"), register("bot-empty"), register("bot-full"), register("member")
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.DB().ExecContext(t.Context(), query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec("UPDATE users SET integration_enabled=TRUE WHERE id IN ($1,$2,$3,$4)", owner.ID, scoped.ID, empty.ID, full.ID)
	exec("UPDATE users SET integration_scopes='{events:read}' WHERE id=$1", scoped.ID)
	exec("UPDATE users SET integration_scopes='{}' WHERE id=$1", empty.ID)

	backend := serverRoleFixture()
	backend.policy.OwnerID = owner.ID
	authority, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	env := startTestEnvDeps(t, nil, nil, func(d *Deps) { d.Authority, d.Auth = authority, a })
	defer env.stop()

	login := func(u *auth.User) (net.Conn, netproto.AuthResponse) {
		t.Helper()
		conn := dialRetry(t, env.addr)
		t.Cleanup(func() { _ = conn.Close() })
		send(t, conn, netproto.MsgAuthenticate, netproto.Authenticate{Username: u.UniqueID, Password: password, AuthorizationModels: []string{netproto.AuthorizationModelRolesV1}})
		var resp netproto.AuthResponse
		if err := netproto.Decode(readOfType(t, conn, netproto.MsgAuthResponse), &resp); err != nil {
			t.Fatal(err)
		}
		return conn, resp
	}
	refused := func(u *auth.User) {
		t.Helper()
		if _, resp := login(u); resp.OK || resp.Reason != integrationOnlyReason {
			t.Fatalf("%s member login = %+v, want refusal %q", u.Nickname, resp, integrationOnlyReason)
		}
		if len(env.srv.sessionsOf(u.UniqueID)) != 0 {
			t.Fatalf("%s has a member session", u.Nickname)
		}
	}
	admitted := func(u *auth.User) net.Conn {
		t.Helper()
		conn, resp := login(u)
		if !resp.OK {
			t.Fatalf("%s member login refused: %q", u.Nickname, resp.Reason)
		}
		return conn
	}

	refused(scoped)
	refused(empty)
	admitted(member)
	bot := admitted(full)
	// The ceiling still admits the account on the integration interfaces.
	if _, err := a.AuthenticateIntegration(t.Context(), scoped.UniqueID, password, "127.0.0.1"); err != nil {
		t.Fatalf("scoped integration login: %v", err)
	}

	// A ceiling set while the account has a member session ends it.
	principal, err := a.AuthenticateIntegration(t.Context(), owner.UniqueID, password, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.srv.SetIntegrationScopes(t.Context(), principal, netproto.IntegrationScopesSet{UniqueID: full.UniqueID, Scopes: []string{auth.ScopeEventsRead}}); err != nil {
		t.Fatal(err)
	}
	_ = bot.SetReadDeadline(time.Now().Add(waitDeadline))
	for {
		if _, err := netproto.ReadFrame(bot); err != nil {
			if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
				t.Fatal("member session survived the new ceiling")
			}
			break
		}
	}
	refused(full)
	if _, err := env.srv.SetIntegrationScopes(t.Context(), principal, netproto.IntegrationScopesSet{UniqueID: full.UniqueID, Full: true}); err != nil {
		t.Fatal(err)
	}
	admitted(full)
}
