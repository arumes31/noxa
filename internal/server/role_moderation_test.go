package server

import (
	"context"
	"testing"

	"noxa/internal/auth"
	"noxa/internal/authorization"
	"noxa/internal/netproto"
)

func TestRoleMoveRequiresVisibleTargetAndBothScopes(t *testing.T) {
	for _, tc := range []struct {
		name      string
		invisible bool
		denyScope int64
	}{
		{name: "hidden target", invisible: true},
		{name: "source denied", denyScope: 1},
		{name: "destination denied", denyScope: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			backend := serverRoleFixture()
			backend.policy.OwnerID = 3
			backend.policy.Roles[0].Permissions = []authorization.Capability{authorization.ViewChannel, authorization.Connect}
			backend.policy.Roles = append(backend.policy.Roles, authorization.Role{ID: 20, Name: "Mover", Position: 1, Permissions: []authorization.Capability{authorization.MoveMembers}})
			backend.policy.Members = []authorization.RoleMember{{UserID: 1, RoleIDs: []int64{20}}}
			backend.policy.Channels = append(backend.policy.Channels, authorization.ChannelPolicy{ChannelID: 2})
			if tc.denyScope != 0 {
				backend.policy.Channels[tc.denyScope-1].Overrides = []authorization.RoleOverride{{RoleID: 20, Capability: authorization.MoveMembers, Effect: authorization.Deny}}
			}
			authority, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
			if err != nil {
				t.Fatal(err)
			}
			env := startTestEnvDeps(t, nil, nil, func(d *Deps) { d.Authority = authority })
			defer env.stop()
			for id := int64(1); id <= 2; id++ {
				env.state.AddChannel(testChannel(id))
			}
			moderator, _ := dialAuthed(t, env.addr, "admin-uid")
			defer func() { _ = moderator.Close() }()
			member, memberID := dialAuthed(t, env.addr, "user-uid")
			defer func() { _ = member.Close() }()
			if err := env.state.MoveClient(memberID, 1); err != nil {
				t.Fatal(err)
			}
			if tc.invisible {
				env.state.SetStatus(memberID, "invisible", "")
			}
			send(t, moderator, netproto.MsgMoveClient, netproto.MoveClient{ClientID: memberID, ChannelID: 2})
			readRoleMediaDenial(t, moderator)
			if channel, _, _ := env.state.ClientChannelState(memberID); channel != 1 {
				t.Fatalf("denied move changed membership to %d", channel)
			}
		})
	}
}

func TestRoleJoinEnforcesPasswordAndOwnerCapacityLimit(t *testing.T) {
	backend := serverRoleFixture()
	backend.policy.Roles[0].Permissions = []authorization.Capability{authorization.ViewChannel, authorization.Connect}
	authority, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	env := startTestEnvDeps(t, nil, nil, func(d *Deps) { d.Authority = authority })
	defer env.stop()
	channel := testChannel(1)
	channel.MaxClients = 1
	channel.PasswordHash, err = auth.HashPassword("channel-password")
	if err != nil {
		t.Fatal(err)
	}
	env.state.AddChannel(channel)
	pub, _ := testX25519(t)
	member, _ := dialSubscriptionClient(t, env.addr, "admin-uid", pub)
	defer func() { _ = member.Close() }()
	send(t, member, netproto.MsgJoinChannel, netproto.JoinChannel{ChannelID: 1, Password: "wrong"})
	if got := readError(t, member); got.Code != errCodePermissionDenied {
		t.Fatalf("legacy admin bypassed password: %+v", got)
	}
	send(t, member, netproto.MsgJoinChannel, netproto.JoinChannel{ChannelID: 1, Password: "channel-password"})
	readChannelKeyFor(t, member, 1)
	owner, ownerID := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = owner.Close() }()
	send(t, owner, netproto.MsgJoinChannel, netproto.JoinChannel{ChannelID: 1})
	if got := readError(t, owner); got.Message != "channel is full" {
		t.Fatalf("owner capacity check: %+v", got)
	}
	if ch, _, _ := env.state.ClientChannelState(ownerID); ch != 0 {
		t.Fatal("failed join moved the owner")
	}
}

func TestRoleModerationProtectsOwnerAndTargetsDestinationAccess(t *testing.T) {
	backend := serverRoleFixture()
	backend.policy.Roles[0].Permissions = []authorization.Capability{authorization.ViewChannel, authorization.Connect}
	backend.policy.Roles = append(backend.policy.Roles, authorization.Role{ID: 20, Name: "Moderator", Position: 1, Permissions: []authorization.Capability{authorization.KickMembers}})
	backend.policy.Members = []authorization.RoleMember{{UserID: 1, RoleIDs: []int64{20}}}
	backend.policy.Channels = append(backend.policy.Channels, authorization.ChannelPolicy{ChannelID: 2, Overrides: []authorization.RoleOverride{{RoleID: 10, Capability: authorization.Connect, Effect: authorization.Deny}}})
	authority, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	env := startTestEnvDeps(t, nil, nil, func(d *Deps) { d.Authority = authority })
	defer env.stop()
	env.state.AddChannel(testChannel(1))
	env.state.AddChannel(testChannel(2))
	member, memberID := dialAuthed(t, env.addr, "admin-uid")
	defer func() { _ = member.Close() }()
	owner, ownerID := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = owner.Close() }()
	send(t, member, netproto.MsgKickClient, netproto.KickClient{ClientID: ownerID, FromServer: true})
	if got := readError(t, member); got.Code != errCodePermissionDenied {
		t.Fatalf("owner was not protected: %+v", got)
	}
	send(t, owner, netproto.MsgMoveClient, netproto.MoveClient{ClientID: memberID, ChannelID: 2})
	if got := readError(t, owner); got.Code != errCodePermissionDenied {
		t.Fatalf("member moved without destination access: %+v", got)
	}
	send(t, owner, netproto.MsgKickClient, netproto.KickClient{ClientID: memberID, FromServer: true})
	readEventOfType(t, member, eventKicked)
}
