package server

import (
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"

	"noxa/internal/authorization"
	"noxa/internal/config"
	"noxa/internal/netproto"
)

func TestRoleLoginDoesNotDiscloseGlobalKeyMOTDOrAnnouncement(t *testing.T) {
	backend := serverRoleFixture()
	authority, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	env := startTestEnvDeps(t, nil, func(c *config.Config) { c.ServerInfoMOTD = true }, func(d *Deps) { d.Authority = authority })
	defer env.stop()
	for _, key := range []string{"motd", "announcement"} {
		gen, sealed, err := env.srv.chatKeys.seal(t.Context(), 0, "private announcement")
		if err != nil {
			t.Fatal(err)
		}
		if err := env.chat.SetServerSetting(t.Context(), key, sealed, gen); err != nil {
			t.Fatal(err)
		}
	}
	for _, identity := range []string{"admin-uid", "user-uid"} {
		pub, _ := testX25519(t)
		conn := dialRetry(t, env.addr)
		defer func() { _ = conn.Close() }()
		send(t, conn, netproto.MsgAuthenticate, netproto.Authenticate{Username: identity, Password: "pw", X25519PublicKey: b64e(pub[:]), AuthorizationModels: []string{netproto.AuthorizationModelRolesV1}})
		var response netproto.AuthResponse
		if err := netproto.Decode(readOfType(t, conn, netproto.MsgAuthResponse), &response); err != nil {
			t.Fatal(err)
		}
		allowed := identity == "user-uid"
		if (len(response.ChatKeys) > 0) != allowed || (response.MOTD != "") != allowed {
			t.Fatalf("%s handshake leaked or omitted global access: %+v", identity, response)
		}
		send(t, conn, netproto.MsgServerInfoQuery, netproto.ServerInfoQuery{})
		var info netproto.ServerInfoResponse
		if err := netproto.Decode(readOfType(t, conn, netproto.MsgServerInfoResponse), &info); err != nil || (info.MOTD != "") != allowed {
			t.Fatalf("%s server info: %+v %v", identity, info, err)
		}
	}
	// Inspect all frames through the post-login barrier, so an unsolicited
	// announcement cannot hide behind a helper that skips unrelated events.
	pub, _ := testX25519(t)
	conn := dialRetry(t, env.addr)
	defer func() { _ = conn.Close() }()
	send(t, conn, netproto.MsgAuthenticate, netproto.Authenticate{Username: "admin-uid", Password: "pw", X25519PublicKey: b64e(pub[:]), AuthorizationModels: []string{netproto.AuthorizationModelRolesV1}})
	send(t, conn, netproto.MsgPing, netproto.Ping{})
	if err := conn.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	for {
		frame, err := netproto.ReadFrame(conn)
		if err != nil {
			t.Fatal(err)
		}
		if netproto.MessageType(frame.Type) == netproto.MsgPong {
			break
		}
		if netproto.MessageType(frame.Type) == netproto.MsgEvent {
			var event struct {
				Type string `json:"type"`
			}
			if err := json.Unmarshal(frame.Payload, &event); err != nil {
				t.Fatal(err)
			}
			if event.Type == eventAnnouncement {
				t.Fatal("login sent a protected announcement")
			}
		}
	}
}

func TestRoleClientMetadataFiltersHiddenMembersAndSensitiveFields(t *testing.T) {
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
	member, memberID := dialAuthed(t, env.addr, "admin-uid")
	defer func() { _ = member.Close() }()
	owner, ownerID := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = owner.Close() }()
	query := func(conn net.Conn, target string) netproto.ClientInfoResponse {
		t.Helper()
		send(t, conn, netproto.MsgClientInfoQuery, netproto.ClientInfoQuery{ClientID: target})
		var result netproto.ClientInfoResponse
		if err := netproto.Decode(readOfType(t, conn, netproto.MsgClientInfoResponse), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	if got := query(member, ownerID); got.IP != "" || got.ConnectedAt != 0 || got.BytesIn != 0 {
		t.Fatalf("legacy admin received sensitive fields: %+v", got)
	}
	if got := query(member, memberID); got.IP == "" || got.ConnectedAt == 0 {
		t.Fatalf("self information missing: %+v", got)
	}
	if got := query(owner, memberID); got.IP == "" || got.ConnectedAt == 0 {
		t.Fatalf("role owner lacks metadata: %+v", got)
	}
	if err := env.state.MoveClient(ownerID, 1); err != nil {
		t.Fatal(err)
	}
	send(t, member, netproto.MsgClientInfoQuery, netproto.ClientInfoQuery{ClientID: ownerID})
	if got := readError(t, member); got.Code != errCodeNotFound {
		t.Fatalf("hidden member response: %+v", got)
	}
	send(t, member, netproto.MsgServerInfoQuery, netproto.ServerInfoQuery{})
	var info netproto.ServerInfoResponse
	if err := netproto.Decode(readOfType(t, member, netproto.MsgServerInfoResponse), &info); err != nil || info.ChannelsOnline != 0 || info.ClientsOnline != 1 {
		t.Fatalf("hidden population exposed: %+v %v", info, err)
	}
}
