package server

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"

	"noxa/internal/authorization"
	"noxa/internal/netproto"
)

func TestRoleChannelIconsRequireTargetAndSourceAccess(t *testing.T) {
	for _, kind := range []netproto.MessageType{netproto.MsgChannelIconSet, netproto.MsgRoleChannelIconSet} {
		t.Run(kind.String(), func(t *testing.T) { testRoleChannelIconAccess(t, kind) })
	}
}

func testRoleChannelIconAccess(t *testing.T, kind netproto.MessageType) {
	backend := serverRoleFixture()
	backend.policy.Roles[0].Permissions = []authorization.Capability{authorization.ViewChannel, authorization.ManageChannels}
	backend.policy.Channels = append(backend.policy.Channels, authorization.ChannelPolicy{ChannelID: 2, Overrides: []authorization.RoleOverride{{RoleID: 10, Capability: authorization.ViewChannel, Effect: authorization.Deny}}})
	authority, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	env := startTestEnvDeps(t, nil, nil, func(d *Deps) { d.Authority = authority })
	defer env.stop()
	for _, id := range []int64{1, 2} {
		env.state.AddChannel(testChannel(id))
	}
	owner, _ := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = owner.Close() }()
	member, _ := dialAuthed(t, env.addr, "admin-uid")
	defer func() { _ = member.Close() }()
	set := func(conn net.Conn, request netproto.ChannelIconSet) {
		t.Helper()
		send(t, conn, kind, request)
		if kind == netproto.MsgRoleChannelIconSet {
			var saved netproto.RoleChannelIconSaved
			if err := netproto.Decode(readOfType(t, conn, netproto.MsgRoleChannelIconSaved), &saved); err != nil || saved.ChannelID != request.ChannelID {
				t.Fatalf("icon acknowledgement: %+v %v", saved, err)
			}
		}
	}
	if kind == netproto.MsgRoleChannelIconSet {
		for _, request := range []netproto.ChannelIconSet{{ChannelID: 0}, {ChannelID: 1, CopyFromChannelID: -1}, {ChannelID: 1, DataBase64: "invalid"}, {ChannelID: 1, DataBase64: b64(tinyPNG), CopyFromChannelID: 2}} {
			send(t, owner, kind, request)
			if got := readError(t, owner); got.Code != errCodeMalformed {
				t.Fatal(got)
			}
		}
		broken := filepath.Join(env.srv.cfg.FileRoot, "icons")
		if err := os.WriteFile(broken, []byte("not a directory"), 0600); err != nil {
			t.Fatal(err)
		}
		send(t, owner, kind, netproto.ChannelIconSet{ChannelID: 1, DataBase64: b64(tinyPNG)})
		if got := readError(t, owner); got.Code != errCodeUnavailable {
			t.Fatal(got)
		}
		if ch, _ := env.state.GetChannel(1); ch.HasIcon {
			t.Fatal("failed storage published an icon")
		}
		if err := os.Remove(broken); err != nil {
			t.Fatal(err)
		}
	}
	// The role owner has no legacy admin bit, but can set a hidden channel's icon.
	set(owner, netproto.ChannelIconSet{ChannelID: 2, DataBase64: b64(tinyPNG)})
	send(t, owner, netproto.MsgChannelIconGet, netproto.ChannelIconGet{ChannelID: 2})
	var icon netproto.ChannelIconData
	if err := netproto.Decode(readOfType(t, owner, netproto.MsgChannelIconData), &icon); err != nil || icon.DataBase64 != b64(tinyPNG) {
		t.Fatalf("owner icon: %+v %v", icon, err)
	}
	for _, request := range []struct {
		kind netproto.MessageType
		body any
	}{
		{netproto.MsgChannelIconGet, netproto.ChannelIconGet{ChannelID: 2}},
		{kind, netproto.ChannelIconSet{ChannelID: 2, DataBase64: b64(tinyPNG)}},
		{kind, netproto.ChannelIconSet{ChannelID: 1, CopyFromChannelID: 2}},
	} {
		send(t, member, request.kind, request.body)
		if got := readError(t, member); got.Code != errCodePermissionDenied {
			t.Fatalf("hidden icon request %v: %+v", request.kind, got)
		}
	}
	if ch, _ := env.state.GetChannel(1); ch.HasIcon {
		t.Fatal("hidden source copied into visible channel")
	}
	set(owner, netproto.ChannelIconSet{ChannelID: 1, CopyFromChannelID: 2})
	send(t, owner, netproto.MsgChannelIconGet, netproto.ChannelIconGet{ChannelID: 1})
	if err := netproto.Decode(readOfType(t, owner, netproto.MsgChannelIconData), &icon); err != nil || icon.DataBase64 != b64(tinyPNG) {
		t.Fatalf("authorized copy: %+v %v", icon, err)
	}
	send(t, member, netproto.MsgChannelIconGet, netproto.ChannelIconGet{ChannelID: 1})
	if err := netproto.Decode(readOfType(t, member, netproto.MsgChannelIconData), &icon); err != nil || icon.DataBase64 != b64(tinyPNG) {
		t.Fatalf("visible icon: %+v %v", icon, err)
	}
	// Revocation applies to a subsequent read even on the same connection.
	_, err = authority.ChangeRolePolicy(t.Context(), 2, authorization.RoleChange{Kind: authorization.ChannelAccessSet, ExpectedRevision: 1,
		Channel: authorization.ChannelPolicy{ChannelID: 1, Overrides: []authorization.RoleOverride{{RoleID: 10, Capability: authorization.ViewChannel, Effect: authorization.Deny}}}})
	if err != nil {
		t.Fatal(err)
	}
	send(t, member, netproto.MsgChannelIconGet, netproto.ChannelIconGet{ChannelID: 1})
	if got := readError(t, member); got.Code != errCodePermissionDenied {
		t.Fatalf("revoked icon: %+v", got)
	}
	send(t, member, kind, netproto.ChannelIconSet{ChannelID: 1, DataBase64: b64(tinyPNG)})
	if got := readError(t, member); got.Code != errCodePermissionDenied {
		t.Fatalf("revoked icon save: %+v", got)
	}
}

func TestRoleAvatarsFollowMemberVisibilityAndUploadCapability(t *testing.T) {
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
	owner, ownerID := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = owner.Close() }()
	member, _ := dialAuthed(t, env.addr, "admin-uid")
	defer func() { _ = member.Close() }()
	send(t, member, netproto.MsgAvatarSet, netproto.AvatarSet{DataBase64: b64(tinyPNG)})
	if got := readError(t, member); got.Code != errCodePermissionDenied {
		t.Fatalf("legacy admin upload: %+v", got)
	}
	send(t, owner, netproto.MsgAvatarSet, netproto.AvatarSet{DataBase64: b64(tinyPNG)})
	send(t, owner, netproto.MsgAvatarGet, netproto.AvatarGet{UniqueID: "user-uid"})
	readOfType(t, owner, netproto.MsgAvatarData)
	send(t, member, netproto.MsgAvatarGet, netproto.AvatarGet{UniqueID: "user-uid"})
	readOfType(t, member, netproto.MsgAvatarData)
	if err := env.state.MoveClient(ownerID, 1); err != nil {
		t.Fatal(err)
	}
	send(t, member, netproto.MsgAvatarGet, netproto.AvatarGet{UniqueID: "user-uid"})
	if got := readError(t, member); got.Code != errCodeNotFound {
		t.Fatalf("hidden avatar: %+v", got)
	}
	send(t, owner, netproto.MsgAvatarGet, netproto.AvatarGet{UniqueID: "user-uid"})
	readOfType(t, owner, netproto.MsgAvatarData)
	if err := env.state.LeaveChannel(ownerID); err != nil {
		t.Fatal(err)
	}
	env.state.SetStatus(ownerID, "invisible", "")
	send(t, member, netproto.MsgAvatarGet, netproto.AvatarGet{UniqueID: "user-uid"})
	if got := readError(t, member); got.Code != errCodeNotFound {
		t.Fatalf("invisible avatar: %+v", got)
	}
	if _, err := env.srv.assets().writeAvatar("offline-uid", ".png", tinyPNG); err != nil {
		t.Fatal(err)
	}
	send(t, member, netproto.MsgAvatarGet, netproto.AvatarGet{UniqueID: "offline-uid"})
	if got := readError(t, member); got.Code != errCodeNotFound {
		t.Fatalf("offline avatar: %+v", got)
	}
	_, err = authority.ChangeRolePolicy(t.Context(), 2, authorization.RoleChange{Kind: authorization.RoleUpdate, ExpectedRevision: 1,
		Role: authorization.Role{ID: 10, Name: "@everyone", Permissions: []authorization.Capability{authorization.ViewChannel, authorization.UploadAvatar}}})
	if err != nil {
		t.Fatal(err)
	}
	send(t, member, netproto.MsgAvatarSet, netproto.AvatarSet{DataBase64: b64(tinyPNG)})
	send(t, member, netproto.MsgAvatarGet, netproto.AvatarGet{UniqueID: "admin-uid"})
	var avatar netproto.AvatarData
	if err := netproto.Decode(readOfType(t, member, netproto.MsgAvatarData), &avatar); err != nil || avatar.DataBase64 != b64(tinyPNG) {
		t.Fatalf("granted avatar upload: %+v %v", avatar, err)
	}
}
