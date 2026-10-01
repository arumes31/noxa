package server

import (
	"context"
	"testing"

	"noxa/internal/authorization"
	"noxa/internal/netproto"
)

func TestRolePokesCheckCapabilityVisibilityAndQueuedRevocation(t *testing.T) {
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
	senderConn, senderID := dialAuthed(t, env.addr, "admin-uid")
	defer func() { _ = senderConn.Close() }()
	targetConn, targetID := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = targetConn.Close() }()
	send(t, senderConn, netproto.MsgPoke, netproto.Poke{ClientID: targetID})
	if got := readError(t, senderConn); got.Code != errCodePermissionDenied {
		t.Fatalf("legacy admin poke: %+v", got)
	}
	p, err := authority.ChangeRolePolicy(t.Context(), 2, authorization.RoleChange{Kind: authorization.RoleUpdate, ExpectedRevision: 1,
		Role: authorization.Role{ID: 10, Name: "@everyone", Permissions: []authorization.Capability{authorization.ViewChannel, authorization.PokeMembers}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := env.state.MoveClient(targetID, 1); err != nil {
		t.Fatal(err)
	}
	send(t, senderConn, netproto.MsgPoke, netproto.Poke{ClientID: targetID})
	if got := readError(t, senderConn); got.Code != errCodeNotFound {
		t.Fatalf("hidden target poke: %+v", got)
	}
	if err := env.state.LeaveChannel(targetID); err != nil {
		t.Fatal(err)
	}
	env.state.SetStatus(targetID, "invisible", "")
	send(t, senderConn, netproto.MsgPoke, netproto.Poke{ClientID: targetID})
	if got := readError(t, senderConn); got.Code != errCodeNotFound {
		t.Fatalf("invisible target poke: %+v", got)
	}
	env.state.SetStatus(targetID, "online", "")
	send(t, senderConn, netproto.MsgPoke, netproto.Poke{ClientID: targetID, Message: "visible"})
	readEventOfType(t, targetConn, eventPoke)
	send(t, senderConn, netproto.MsgPoke, netproto.Poke{ClientID: targetID})
	if got := readError(t, senderConn); got.Code != errCodeMalformed {
		t.Fatalf("poke cooldown: %+v", got)
	}
	queued, err := eventEnvelope(eventPoke, pokeEvent{FromClientID: senderID, Message: "queued"})
	if err != nil {
		t.Fatal(err)
	}
	target, _ := env.srv.clientByID(targetID)
	before, err := authorization.NewRoleEvaluator(p)
	if err != nil {
		t.Fatal(err)
	}
	if frame, err := env.srv.roleBroadcastFrame(target, queued, before); err != nil || frame == nil {
		t.Fatalf("allowed poke dropped: %v", err)
	}
	if err := env.state.MoveClient(targetID, 1); err != nil {
		t.Fatal(err)
	}
	if frame, err := env.srv.roleBroadcastFrame(target, queued, before); err != nil || frame != nil {
		t.Fatalf("queued poke ignored hidden target: %v", err)
	}
	if err := env.state.LeaveChannel(targetID); err != nil {
		t.Fatal(err)
	}
	p, err = authority.ChangeRolePolicy(t.Context(), 2, authorization.RoleChange{Kind: authorization.RoleUpdate, ExpectedRevision: 2,
		Role: authorization.Role{ID: 10, Name: "@everyone", Permissions: []authorization.Capability{authorization.ViewChannel}}})
	if err != nil {
		t.Fatal(err)
	}
	after, err := authorization.NewRoleEvaluator(p)
	if err != nil {
		t.Fatal(err)
	}
	if frame, err := env.srv.roleBroadcastFrame(target, queued, after); err != nil || frame != nil {
		t.Fatalf("revoked queued poke delivered: %v", err)
	}
}
