package server

import (
	"context"
	"testing"

	"noxa/internal/authorization"
	"noxa/internal/netproto"
	"noxa/internal/store"
)

func TestRoleAdminControlsIgnoreLegacyAdminAndObserveRevocation(t *testing.T) {
	backend := serverRoleFixture()
	authority, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	env := startTestEnvDeps(t, nil, nil, func(d *Deps) { d.Authority = authority })
	defer env.stop()
	env.banAdmin.bans = []store.BanRecord{{ID: 1, Value: "banned-member"}}
	member, _ := dialAuthed(t, env.addr, "admin-uid")
	defer func() { _ = member.Close() }()
	owner, _ := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = owner.Close() }()
	config := netproto.ServerConfig{MaxClients: 100, ClientTimeoutSeconds: 120, OpusBitrate: 64000, OpusFEC: true}
	for _, request := range []struct {
		mt   netproto.MessageType
		body any
	}{
		{netproto.MsgServerConfigQuery, netproto.ServerConfigQuery{}},
		{netproto.MsgServerConfigSet, config},
		{netproto.MsgBanList, netproto.BanList{}},
	} {
		send(t, member, request.mt, request.body)
		if got := readError(t, member); got.Code != errCodePermissionDenied {
			t.Fatalf("%v: %+v", request.mt, got)
		}
	}
	// The owner has no legacy admin flag and can still save settings.
	send(t, owner, netproto.MsgServerConfigSet, config)
	var saved netproto.ServerConfig
	if err := netproto.Decode(readOfType(t, owner, netproto.MsgServerConfigResponse), &saved); err != nil || saved != config {
		t.Fatalf("settings acknowledgement: %+v %v", saved, err)
	}
	if got, _, err := env.chat.GetServerSetting(t.Context(), "default_opus_bitrate"); err != nil || got != "64000" {
		t.Fatalf("persisted settings: %q %v", got, err)
	}
	// A delegated capability takes effect without a new login.
	if _, err := authority.ChangeRolePolicy(t.Context(), 2, authorization.RoleChange{Kind: authorization.RoleUpdate, ExpectedRevision: 1,
		Role: authorization.Role{ID: 10, Name: "@everyone", Permissions: []authorization.Capability{authorization.BanMembers}}}); err != nil {
		t.Fatal(err)
	}
	send(t, member, netproto.MsgBanList, netproto.BanList{})
	var bans netproto.BanListResponse
	if err := netproto.Decode(readOfType(t, member, netproto.MsgBanListResponse), &bans); err != nil || len(bans.Bans) != 1 {
		t.Fatalf("ban list: %+v %v", bans, err)
	}
	if _, err := authority.ChangeRolePolicy(t.Context(), 2, authorization.RoleChange{Kind: authorization.RoleUpdate, ExpectedRevision: 2,
		Role: authorization.Role{ID: 10, Name: "@everyone"}}); err != nil {
		t.Fatal(err)
	}
	send(t, member, netproto.MsgBanList, netproto.BanList{})
	if got := readError(t, member); got.Code != errCodePermissionDenied {
		t.Fatalf("revoked ban-list grant: %+v", got)
	}
}

func TestRemovedPermissionAndTokenMessagesAreUnknown(t *testing.T) {
	backend := serverRoleFixture()
	authority, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	env := startTestEnvDeps(t, nil, nil, func(d *Deps) { d.Authority = authority })
	defer env.stop()
	owner, _ := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = owner.Close() }()
	for _, mt := range []netproto.MessageType{
		3,   // retired channel creation
		11,  // retired channel deletion
		40,  // retired channel edit
		83,  // retired unacknowledged ban removal
		67,  // retired group assignment
		69,  // retired numeric permission write
		71,  // retired permission template
		131, // retired administrator roster
		33,  // retired privilege-token redemption
		112, // retired privilege-token creation
	} {
		send(t, owner, mt, map[string]any{})
		if got := readError(t, owner); got.Code != errCodeUnknown {
			t.Fatalf("removed message %v: %+v", mt, got)
		}
	}
}
