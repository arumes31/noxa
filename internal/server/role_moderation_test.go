package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"testing"
	"time"

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

func TestRoleMovePeerRequiresPermissionAndProtectsHigherRoles(t *testing.T) {
	for _, tc := range []struct {
		name                       string
		grant, higher, owner, self bool
	}{
		{name: "permitted peer", grant: true},
		{name: "ungranted peer"},
		{name: "higher role", grant: true, higher: true},
		{name: "owner", grant: true, owner: true},
		{name: "self", grant: true, self: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			backend := serverRoleFixture()
			backend.policy.OwnerID = 3
			if tc.owner {
				backend.policy.OwnerID = 2
			}
			backend.policy.Roles[0].Permissions = []authorization.Capability{authorization.ViewChannel, authorization.Connect}
			memberRole := authorization.Role{ID: 20, Name: "Member", Position: 1}
			if tc.grant {
				memberRole.Permissions = []authorization.Capability{authorization.MoveMembers}
			}
			backend.policy.Roles = append(backend.policy.Roles, memberRole, authorization.Role{ID: 30, Name: "Moderator", Position: 2})
			targetRole := int64(20)
			if tc.higher {
				targetRole = 30
			}
			backend.policy.Members = []authorization.RoleMember{{UserID: 1, RoleIDs: []int64{20}}, {UserID: 2, RoleIDs: []int64{targetRole}}}
			backend.policy.Channels = append(backend.policy.Channels, authorization.ChannelPolicy{ChannelID: 2})
			authority, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
			if err != nil {
				t.Fatal(err)
			}
			env := startTestEnvDeps(t, nil, nil, func(d *Deps) { d.Authority = authority })
			defer env.stop()
			env.state.AddChannel(testChannel(1))
			env.state.AddChannel(testChannel(2))
			actor, actorID := dialAuthed(t, env.addr, "admin-uid")
			defer func() { _ = actor.Close() }()
			target, targetID := dialAuthed(t, env.addr, "user-uid")
			defer func() { _ = target.Close() }()
			if tc.self {
				targetID = actorID
			}
			if err := env.state.MoveClient(targetID, 1); err != nil {
				t.Fatal(err)
			}
			send(t, actor, netproto.MsgMoveClient, netproto.MoveClient{ClientID: targetID, ChannelID: 2, AckRequested: true})
			allowed := tc.grant && !tc.higher && !tc.owner && !tc.self
			if allowed {
				readOfType(t, actor, netproto.MsgClientMoved)
			} else {
				readRoleMediaDenial(t, actor)
			}
			want := int64(1)
			if allowed {
				want = 2
			}
			if channel, _, _ := env.state.ClientChannelState(targetID); channel != want {
				t.Fatalf("channel = %d, want %d", channel, want)
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
	// A busy target writer must not block an authorized removal. Its terminal
	// notice is best effort; the actor's acknowledgement and removal are not.
	target, ok := env.srv.clientByID(memberID)
	if !ok {
		t.Fatal("member missing before kick")
	}
	target.wmu.Lock()
	defer target.wmu.Unlock()
	send(t, owner, netproto.MsgKickClient, netproto.KickClient{ClientID: memberID, FromServer: true, AckRequested: true})
	frame := readOfType(t, owner, netproto.MsgClientRemoved)
	var removed netproto.ClientRemoved
	if err := json.Unmarshal(frame.Payload, &removed); err != nil {
		t.Fatal(err)
	}
	if removed != (netproto.ClientRemoved{ClientID: memberID, FromServer: true}) {
		t.Fatalf("wrong kick acknowledgement: %+v", removed)
	}
	if _, ok := env.state.GetClient(memberID); ok {
		t.Fatal("kicked member remains in state")
	}
	if _, ok := env.srv.clientByID(memberID); ok {
		t.Fatal("kicked member remains registered")
	}
	if _, ok := env.state.GetClient(ownerID); !ok {
		t.Fatal("owner was removed")
	}
	_ = member.SetReadDeadline(time.Now().Add(2 * time.Second))
	for {
		_, err := netproto.ReadFrame(member) // drain already-queued ordinary frames
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("kicked connection did not close cleanly: %v", err)
		}
	}
}

// A presence broadcast queued for a stalled connection holds the shared role
// lease while it waits for that connection's writer. The wait is bounded by
// roleWriteCeiling, so a kick (exclusive policy) still completes.
func TestRoleKickNotBlockedByBroadcastToStalledTarget(t *testing.T) {
	backend := serverRoleFixture()
	backend.policy.Roles[0].Permissions = []authorization.Capability{authorization.ViewChannel, authorization.Connect}
	authority, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	env := startTestEnvDeps(t, nil, nil, func(d *Deps) { d.Authority = authority })
	defer env.stop()
	env.state.AddChannel(testChannel(1))
	member, memberID := dialAuthed(t, env.addr, "admin-uid")
	defer func() { _ = member.Close() }()
	owner, _ := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = owner.Close() }()
	for _, conn := range []net.Conn{member, owner} {
		send(t, conn, netproto.MsgPing, netproto.Ping{})
		readOfType(t, conn, netproto.MsgPong)
	}
	target, ok := env.srv.clientByID(memberID)
	if !ok {
		t.Fatal("member missing")
	}
	target.wmu.Lock()
	defer target.wmu.Unlock()
	// The owner's join is broadcast to the stalled member; its writer blocks
	// on wmu while holding the role lease and metadata lock.
	send(t, owner, netproto.MsgJoinChannel, netproto.JoinChannel{ChannelID: 1})
	send(t, owner, netproto.MsgPing, netproto.Ping{})
	readOfType(t, owner, netproto.MsgPong)
	time.Sleep(200 * time.Millisecond)

	started := time.Now()
	send(t, owner, netproto.MsgKickClient, netproto.KickClient{ClientID: memberID, FromServer: true, AckRequested: true})
	// The stalled member may already be disconnected because its writer fell
	// behind; then the kick finds no session and answers with an error. Either
	// way the kick must not wait behind the stalled broadcast.
	for {
		_ = owner.SetReadDeadline(time.Now().Add(roleWriteCeiling + 3*time.Second))
		frame, err := netproto.ReadFrame(owner)
		if err != nil {
			t.Fatalf("no kick outcome: %v", err)
		}
		if netproto.MessageType(frame.Type) == netproto.MsgError {
			break
		}
		if netproto.MessageType(frame.Type) == netproto.MsgClientRemoved {
			var removed netproto.ClientRemoved
			if err := json.Unmarshal(frame.Payload, &removed); err != nil {
				t.Fatal(err)
			}
			if removed.ClientID != memberID {
				t.Fatalf("wrong kick acknowledgement: %+v", removed)
			}
			break
		}
	}
	_ = owner.SetReadDeadline(time.Time{})
	elapsed := time.Since(started)
	t.Logf("kick completed after %v", elapsed)
	if elapsed > roleWriteCeiling+3*time.Second {
		t.Fatalf("kick waited %v behind a stalled broadcast", elapsed)
	}
	if _, ok := env.srv.clientByID(memberID); ok {
		t.Fatal("kicked member remains registered")
	}
}
