package server

import (
	"context"
	"net"
	"noxa/internal/authorization"
	"noxa/internal/netproto"
	"testing"
)

func TestVoiceQueueAuthorizationAndAcknowledgement(t *testing.T) {
	env := startTestEnv(t, nil)
	defer env.stop()
	admin, adminID := dialAuthed(t, env.addr, "admin-uid")
	defer func() { _ = admin.Close() }()
	user, userID := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = user.Close() }()
	env.state.AddChannel(testChannel(1))
	env.state.AddChannel(testChannel(2))
	send(t, user, netproto.MsgVoiceQueueRequest, netproto.VoiceQueueRequest{Action: "raise", ChannelID: 1})
	readOfType(t, user, netproto.MsgError)
	send(t, admin, netproto.MsgJoinChannel, netproto.JoinChannel{ChannelID: 1})
	send(t, user, netproto.MsgJoinChannel, netproto.JoinChannel{ChannelID: 1})
	waitFor(t, "voice membership", func() bool {
		a, _ := env.state.GetClient(adminID)
		b, _ := env.state.GetClient(userID)
		return a.ChannelID == 1 && b.ChannelID == 1
	})
	send(t, user, netproto.MsgVoiceQueueRequest, netproto.VoiceQueueRequest{Action: "raise", ChannelID: 1})
	var result netproto.VoiceQueueResult
	if err := netproto.Decode(readOfType(t, user, netproto.MsgVoiceQueueResult), &result); err != nil {
		t.Fatal(err)
	}
	if result.ClientID != userID || result.ChannelID != 1 || result.Action != "raise" {
		t.Fatal(result)
	}
	send(t, user, netproto.MsgVoiceQueueRequest, netproto.VoiceQueueRequest{Action: "clear", ChannelID: 1})
	readOfType(t, user, netproto.MsgError)
	member, _ := env.state.GetClient(userID)
	if member.HandRaisedOrder == 0 {
		t.Fatal("member cleared queue")
	}
	send(t, user, netproto.MsgVoiceQueueRequest, netproto.VoiceQueueRequest{Action: "raise", ChannelID: 1, ClientID: adminID})
	readOfType(t, user, netproto.MsgError)
	send(t, admin, netproto.MsgVoiceQueueRequest, netproto.VoiceQueueRequest{Action: "next", ChannelID: 1})
	if err := netproto.Decode(readOfType(t, admin, netproto.MsgVoiceQueueResult), &result); err != nil {
		t.Fatal(err)
	}
	member, _ = env.state.GetClient(userID)
	if !member.HandCalled || member.HandRaisedOrder != 0 || member.ServerMuted || member.SelfMuted {
		t.Fatalf("bad call: %+v", member)
	}
	if err := env.state.MoveClient(userID, 2); err != nil {
		t.Fatal(err)
	}
	send(t, user, netproto.MsgVoiceQueueRequest, netproto.VoiceQueueRequest{Action: "raise", ChannelID: 1})
	readOfType(t, user, netproto.MsgError)
	member, _ = env.state.GetClient(userID)
	if member.HandRaisedOrder != 0 || member.HandCalled {
		t.Fatal("stale action crossed channel")
	}
}

func TestVoiceQueueModerationRespectsHierarchyAtomically(t *testing.T) {
	backend := serverRoleFixture()
	backend.policy.Roles[0].Permissions = []authorization.Capability{authorization.ViewChannel, authorization.Connect}
	backend.policy.Roles = append(backend.policy.Roles, authorization.Role{ID: 20, Name: "Moderator", Position: 1, Permissions: []authorization.Capability{authorization.MuteMembers}})
	backend.policy.Members = []authorization.RoleMember{{UserID: 1, RoleIDs: []int64{20}}}
	authority, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	env := startTestEnvDeps(t, nil, nil, func(d *Deps) { d.Authority = authority })
	defer env.stop()
	env.state.AddChannel(testChannel(1))
	moderator, mid := dialAuthed(t, env.addr, "admin-uid")
	defer func() { _ = moderator.Close() }()
	owner, oid := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = owner.Close() }()
	for _, id := range []string{mid, oid} {
		if err := env.state.MoveClient(id, 1); err != nil {
			t.Fatal(err)
		}
	}
	raise := func(conn net.Conn) {
		t.Helper()
		send(t, conn, netproto.MsgVoiceQueueRequest, netproto.VoiceQueueRequest{Action: "raise", ChannelID: 1})
		readOfType(t, conn, netproto.MsgVoiceQueueResult)
	}
	raise(owner)
	raise(moderator)
	for _, action := range []string{"lower", "next", "clear"} {
		send(t, moderator, netproto.MsgVoiceQueueRequest, netproto.VoiceQueueRequest{Action: action, ChannelID: 1, ClientID: oid})
		if got := readError(t, moderator); got.Code != errCodePermissionDenied {
			t.Fatal(got)
		}
		a, _ := env.state.GetClient(oid)
		b, _ := env.state.GetClient(mid)
		if a.HandRaisedOrder == 0 || a.HandCalled || b.HandRaisedOrder == 0 || b.HandCalled {
			t.Fatalf("partial mutation for %s", action)
		}
	}
	// Calling one's own hand is allowed, but a moderator may not replace
	// a currently called owner with a lower-ranked speaker.
	send(t, owner, netproto.MsgVoiceQueueRequest, netproto.VoiceQueueRequest{Action: "next", ChannelID: 1})
	readOfType(t, owner, netproto.MsgVoiceQueueResult)
	send(t, moderator, netproto.MsgVoiceQueueRequest, netproto.VoiceQueueRequest{Action: "next", ChannelID: 1})
	readError(t, moderator)
	a, _ := env.state.GetClient(oid)
	if !a.HandCalled {
		t.Fatal("moderator replaced the called owner")
	}
	send(t, owner, netproto.MsgVoiceQueueRequest, netproto.VoiceQueueRequest{Action: "clear", ChannelID: 1})
	readOfType(t, owner, netproto.MsgVoiceQueueResult)
	for _, id := range []string{mid, oid} {
		a, _ := env.state.GetClient(id)
		if a.HandCalled || a.HandRaisedOrder != 0 {
			t.Fatal("owner failed to clear queue")
		}
	}
}
