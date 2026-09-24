// botbadge_test.go covers the bot flag reaching the in-memory client at
// authentication time (180).
package server

import (
	"context"
	"testing"

	"noxa/internal/authorization"
	"noxa/internal/netproto"
	"noxa/internal/state"
)

func TestRoleBotIdentityGrantsNoAuthority(t *testing.T) {
	for _, bot := range []bool{false, true} {
		t.Run(map[bool]string{false: "account_member", true: "account_bot"}[bot], func(t *testing.T) {
			backend := serverRoleFixture()
			a, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
			if err != nil {
				t.Fatal(err)
			}
			env := startTestEnvDeps(t, nil, nil, func(d *Deps) {
				d.Authority = a
				d.Auth.(*fakeAuth).users["admin-uid"].IsBot = bot
			})
			defer env.stop()
			conn, _ := dialAuthed(t, env.addr, "admin-uid")
			defer func() { _ = conn.Close() }()
			if got := stateClientFor(t, env, "admin-uid").IsBot; got != bot {
				t.Fatalf("bot badge = %v, want %v", got, bot)
			}
			send(t, conn, netproto.MsgServerBannerSet, netproto.ServerBannerSet{DataBase64: b64(tinyPNG)})
			var denied netproto.Error
			if err := netproto.Decode(readOfType(t, conn, netproto.MsgError), &denied); err != nil || denied.Code != errCodePermissionDenied {
				t.Fatalf("bot gained server authority: %+v, %v", denied, err)
			}
		})
	}
}

// stateClientFor returns the in-memory state client of a connected user.
func stateClientFor(t *testing.T, env *testEnv, uniqueID string) *state.Client {
	t.Helper()
	c := srvClient(t, env, uniqueID)
	sc, ok := env.state.GetClient(c.ID)
	if !ok {
		t.Fatalf("no state client for %s", uniqueID)
	}
	return sc
}

// TestAuthSetsBotFlag verifies account metadata reaches the authenticated
// session and every later snapshot.
func TestAuthSetsBotFlag(t *testing.T) {
	env := startTestEnvDeps(t, nil, nil, func(d *Deps) {
		d.Auth.(*fakeAuth).users["user-uid"].IsBot = true
	})
	defer env.stop()
	conn, _ := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = conn.Close() }()

	if !stateClientFor(t, env, "user-uid").IsBot {
		t.Fatalf("bot account is not flagged in state")
	}
}

// TestAuthLeavesNonBotsUnflagged verifies ordinary account metadata never
// creates a bot badge.
func TestAuthLeavesNonBotsUnflagged(t *testing.T) {
	env := startTestEnv(t, nil)
	defer env.stop()

	userConn, _ := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = userConn.Close() }()
	if stateClientFor(t, env, "user-uid").IsBot {
		t.Errorf("plain user is flagged as a bot")
	}

	adminConn, _ := dialAuthed(t, env.addr, "admin-uid")
	defer func() { _ = adminConn.Close() }()
	if stateClientFor(t, env, "admin-uid").IsBot {
		t.Errorf("server admin is flagged as a bot")
	}
}

// TestGuestAuthLeavesBotUnflagged: guests have no users row, so the flag can
// never be granted to them.
func TestGuestAuthLeavesBotUnflagged(t *testing.T) {
	env := startTestEnv(t, nil)
	defer env.stop()
	conn, _ := dialGuest(t, env.addr, "g", "")
	defer func() { _ = conn.Close() }()

	for _, c := range env.state.ListClients() {
		if c.IsBot {
			t.Fatalf("guest %s flagged as a bot", c.UniqueID)
		}
	}
}
