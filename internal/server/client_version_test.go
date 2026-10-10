package server

import (
	"context"
	"net"
	"strings"
	"testing"

	"noxa/internal/authorization"
	"noxa/internal/netproto"
)

func dialVersionedClient(t *testing.T, addr, identity, version string) (net.Conn, string) {
	t.Helper()
	conn := dialRetry(t, addr)
	t.Cleanup(func() { _ = conn.Close() })
	send(t, conn, netproto.MsgAuthenticate, netproto.Authenticate{Username: identity, Password: "pw", ClientVersion: version})
	var response netproto.AuthResponse
	if err := netproto.Decode(readOfType(t, conn, netproto.MsgAuthResponse), &response); err != nil {
		t.Fatal(err)
	}
	if !response.OK {
		t.Fatalf("versioned authentication failed: %+v", response)
	}
	readOfType(t, conn, netproto.MsgSnapshot)
	return conn, response.ClientID
}

func TestClientVersionVisibleToMembersWithoutSensitiveMetadata(t *testing.T) {
	backend := serverRoleFixture()
	backend.policy.Roles[0].Permissions = []authorization.Capability{authorization.ViewChannel}
	backend.policy.Channels[0].Overrides = []authorization.RoleOverride{{RoleID: 10, Capability: authorization.ViewChannel, Effect: authorization.Deny}}
	authority, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	env := startTestEnvDeps(t, nil, nil, func(d *Deps) { d.Authority = authority })
	defer env.stop()
	env.state.AddChannel(testChannel(1))
	member, memberID := dialVersionedClient(t, env.addr, "admin-uid", "")
	owner, ownerID := dialVersionedClient(t, env.addr, "user-uid", "0.5.39-test+123abcd")
	got := queryClientInfo(t, member, ownerID)
	if got.ClientVersion != "0.5.39-test+123abcd" {
		t.Fatalf("ordinary member sees client version %q", got.ClientVersion)
	}
	if got.IP != "" || got.Port != 0 || got.BytesIn != 0 || got.BytesOut != 0 || got.ConnectedAt != 0 || got.IdleSeconds != 0 {
		t.Fatalf("client version disclosed sensitive metadata: %+v", got)
	}
	if got := queryClientInfo(t, owner, memberID); got.ClientVersion != "" {
		t.Fatalf("legacy client acquired invented version %q", got.ClientVersion)
	}
	env.state.SetStatus(ownerID, "invisible", "")
	send(t, member, netproto.MsgClientInfoQuery, netproto.ClientInfoQuery{ClientID: ownerID})
	if got := readError(t, member); got.Code != errCodeNotFound {
		t.Fatalf("invisible member version exposed: %+v", got)
	}
	env.state.SetStatus(ownerID, "online", "")
	if err := env.state.MoveClient(ownerID, 1); err != nil {
		t.Fatal(err)
	}
	send(t, member, netproto.MsgClientInfoQuery, netproto.ClientInfoQuery{ClientID: ownerID})
	if got := readError(t, member); got.Code != errCodeNotFound {
		t.Fatalf("hidden-channel member version exposed: %+v", got)
	}
}

func TestClientVersionAuthenticationBounds(t *testing.T) {
	env := startTestEnv(t, nil)
	defer env.stop()
	for _, version := range []string{"", "0.5.39 (Windows amd64)", strings.Repeat("v", 100)} {
		t.Run("accept "+version, func(t *testing.T) {
			conn, id := dialVersionedClient(t, env.addr, "user-uid", version)
			if got := queryClientInfo(t, conn, id); got.ClientVersion != version {
				t.Fatalf("version = %q, want %q", got.ClientVersion, version)
			}
		})
	}
	for _, tc := range []struct{ name, version string }{
		{"too long", strings.Repeat("v", 101)},
		{"newline", "0.5.39\nforged"},
		{"tab", "0.5.39\tforged"},
		{"NUL", "0.5.39\x00"},
		{"DEL", "0.5.39\x7f"},
	} {
		t.Run("reject "+tc.name, func(t *testing.T) {
			conn := dialRetry(t, env.addr)
			defer func() { _ = conn.Close() }()
			send(t, conn, netproto.MsgAuthenticate, netproto.Authenticate{Username: "user-uid", Password: "pw", ClientVersion: tc.version})
			if got := readError(t, conn); got.Code != errCodeMalformed || got.OriginType != uint16(netproto.MsgAuthenticate) {
				t.Fatalf("invalid version authentication: %+v", got)
			}
		})
	}
}

func TestClientVersionDoesNotExposePendingAuthentication(t *testing.T) {
	env := startTestEnv(t, nil)
	defer env.stop()
	owner, _ := dialVersionedClient(t, env.addr, "admin-uid", "0.5.39")
	pending := dialRetry(t, env.addr)
	defer func() { _ = pending.Close() }()
	send(t, pending, netproto.MsgAuthenticate, netproto.Authenticate{Username: "user-uid", ClientVersion: "0.5.39-pending"})
	readOfType(t, pending, netproto.MsgAuthChallenge)
	var pendingID string
	env.srv.mu.RLock()
	for id, client := range env.srv.clients {
		if client.Conn.RemoteAddr().String() == pending.LocalAddr().String() {
			pendingID = id
			break
		}
	}
	env.srv.mu.RUnlock()
	if pendingID == "" {
		t.Fatal("pending authentication fixture missing")
	}
	send(t, owner, netproto.MsgClientInfoQuery, netproto.ClientInfoQuery{ClientID: pendingID})
	if got := readError(t, owner); got.Code != errCodeNotFound {
		t.Fatalf("pending client version exposed before authentication: %+v", got)
	}
}
