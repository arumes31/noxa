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
	"noxa/internal/store"
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
	if got := query(member, ownerID); got.PingMs != -1 {
		t.Fatalf("unmeasured member ping = %d, want -1", got.PingMs)
	}
	target, ok := env.srv.clientByID(ownerID)
	if !ok {
		t.Fatal("connected member missing")
	}
	target.mu.Lock()
	target.rttKnown, target.rttNs = true, int64(73*time.Millisecond)
	target.mu.Unlock()
	ownerState, _ := env.state.GetClient(ownerID)
	ownerState.ConnectedAt = time.Now().Add(-time.Minute)
	env.state.AddClient(ownerState)
	if got := query(member, ownerID); got.PingMs != 73 {
		t.Errorf("visible member ping = %d, want 73 without sensitive-metadata permission", got.PingMs)
	}
	if got := query(member, ownerID); got.IP != "" || got.Port != 0 || got.ConnectedAt != 0 || got.IdleSeconds != 0 || got.BytesIn != 0 || got.BytesOut != 0 {
		t.Fatalf("ordinary member received sensitive fields: %+v", got)
	}
	if got := query(member, memberID); got.IP == "" || got.ConnectedAt == 0 {
		t.Fatalf("self information missing: %+v", got)
	}
	if got := query(owner, memberID); got.IP == "" || got.ConnectedAt == 0 {
		t.Fatalf("role owner lacks metadata: %+v", got)
	}
	env.state.SetStatus(ownerID, "invisible", "")
	send(t, member, netproto.MsgClientInfoQuery, netproto.ClientInfoQuery{ClientID: ownerID})
	if got := readError(t, member); got.Code != errCodeNotFound {
		t.Fatalf("invisible member response: %+v", got)
	}
	env.state.SetStatus(ownerID, "online", "")
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

func TestClientInfoIdleIgnoresControlTraffic(t *testing.T) {
	env := startTestEnv(t, nil)
	defer env.stop()
	conn, clientID := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = conn.Close() }()

	// Backdate this silent connection instead of waiting on wall-clock sleeps.
	member, _ := env.state.GetClient(clientID)
	member.ConnectedAt = time.Now().Add(-time.Minute)
	env.state.AddClient(member)
	before := queryClientInfo(t, conn, clientID)
	if before.IdleSeconds < 60 {
		t.Fatalf("silent connection idle = %d, want at least 60", before.IdleSeconds)
	}
	send(t, conn, netproto.MsgPing, netproto.Ping{})
	readOfType(t, conn, netproto.MsgPong)
	send(t, conn, netproto.MsgChatSend, netproto.ChatSend{Text: "still not speaking"})
	after := queryClientInfo(t, conn, clientID)
	if after.IdleSeconds < before.IdleSeconds || after.BytesIn <= before.BytesIn {
		t.Fatalf("control traffic reset voice idle or lost byte accounting: before=%+v after=%+v", before, after)
	}

	env.srv.onSpeakingChanged(clientID, true)
	if info := queryClientInfo(t, conn, clientID); info.IdleSeconds != 0 {
		t.Fatalf("speaking client idle = %d, want 0", info.IdleSeconds)
	}
	env.srv.onSpeakingChanged(clientID, false)
	member, _ = env.state.GetClient(clientID)
	if member.LastSpokeAt.IsZero() {
		t.Fatal("stopping speech did not record the start of voice idle")
	}
	member.LastSpokeAt = time.Now().Add(-15 * time.Second)
	env.state.AddClient(member)
	if info := queryClientInfo(t, conn, clientID); info.IdleSeconds < 15 || info.IdleSeconds >= before.IdleSeconds {
		t.Fatalf("idle should be measured from the end of speech: %+v", info)
	}
	member.LastSpokeAt = time.Now().Add(time.Minute)
	env.state.AddClient(member)
	if info := queryClientInfo(t, conn, clientID); info.IdleSeconds != 0 {
		t.Fatalf("clock change produced negative idle: %+v", info)
	}
}

// The address of an IP ban is a remote address: the ban list shows it only to
// a reader that also holds ViewRemoteAddresses. Other ban managers get a
// masked value and still see the row, so they can lift it by ID.
func TestBanListMasksAddressesWithoutViewRemoteAddresses(t *testing.T) {
	for name, tc := range map[string]struct {
		permissions []authorization.Capability
		want        string
	}{
		"ban members only":          {[]authorization.Capability{authorization.BanMembers}, "hidden"},
		"ban members and addresses": {[]authorization.Capability{authorization.BanMembers, authorization.ViewRemoteAddresses}, "203.0.113.9"},
	} {
		t.Run(name, func(t *testing.T) {
			backend := serverRoleFixture()
			backend.policy.Roles[0].Permissions = []authorization.Capability{authorization.ViewChannel, authorization.Connect}
			backend.policy.Roles = append(backend.policy.Roles, authorization.Role{ID: 20, Name: "moderator", Position: 1, Permissions: tc.permissions})
			backend.policy.Members = []authorization.RoleMember{{UserID: 1, RoleIDs: []int64{20}}}
			authority, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
			if err != nil {
				t.Fatal(err)
			}
			env := startTestEnvDeps(t, nil, nil, func(d *Deps) { d.Authority = authority })
			defer env.stop()
			env.banAdmin.mu.Lock()
			env.banAdmin.bans = []store.BanRecord{{ID: 7, Type: 0, Value: "203.0.113.9"}, {ID: 6, Type: 1, Value: "guest-uid"}}
			env.banAdmin.mu.Unlock()
			moderator, _ := dialAuthed(t, env.addr, "admin-uid")
			defer func() { _ = moderator.Close() }()
			send(t, moderator, netproto.MsgBanList, netproto.BanList{})
			var got netproto.BanListResponse
			if err := netproto.Decode(readOfType(t, moderator, netproto.MsgBanListResponse), &got); err != nil {
				t.Fatal(err)
			}
			if len(got.Bans) != 2 || got.Bans[0].ID != 7 || got.Bans[0].Type != 0 || got.Bans[0].Value != tc.want {
				t.Fatalf("IP ban row = %+v, want value %q", got.Bans, tc.want)
			}
			if got.Bans[1].Value != "guest-uid" {
				t.Fatalf("unique-ID ban value = %q, want it unmasked", got.Bans[1].Value)
			}
		})
	}
}
