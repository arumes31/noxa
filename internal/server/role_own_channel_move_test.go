package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"noxa/internal/authorization"
	"noxa/internal/broadcast"
	"noxa/internal/netproto"
)

func TestRoleSnapshotRetainsOwnMoveCauseAcrossQueuedStructuralEvents(t *testing.T) {
	backend := serverRoleFixture()
	backend.policy.Roles[0].Permissions = []authorization.Capability{authorization.ViewChannel, authorization.Connect}
	backend.policy.Channels = append(backend.policy.Channels, authorization.ChannelPolicy{ChannelID: 2})
	authority, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	env := startTestEnvDeps(t, nil, nil, func(d *Deps) { d.Authority = authority })
	defer env.stop()
	for _, id := range []int64{1, 2} {
		env.state.AddChannel(testChannel(id))
	}
	conn, id := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = conn.Close() }()
	client, _ := env.srv.clientByID(id)
	otherConn, otherID := dialAuthed(t, env.addr, "admin-uid")
	defer func() { _ = otherConn.Close() }()
	other, _ := env.srv.clientByID(otherID)
	evaluator, err := authorization.NewRoleEvaluator(backend.policy)
	if err != nil {
		t.Fatal(err)
	}
	if err := env.srv.moveClient(t.Context(), id, 2, "private-actor"); err != nil {
		t.Fatal(err)
	}
	for _, recipient := range []*Client{client, other} {
		for _, kind := range []string{eventNicknameChanged, eventUserMoved, eventAudioStateChanged, eventKicked} {
			// Kicking an unrelated member can be queued ahead of our move.
			eventID := id
			if kind == eventKicked {
				eventID = "unrelated"
			}
			payload, err := eventEnvelope(kind, userEvent{ClientID: eventID, ChannelID: 2, FromChannelID: 999, ByClientID: "private-actor"})
			if err != nil {
				t.Fatal(err)
			}
			frame, err := env.srv.roleBroadcastFrame(recipient, payload, evaluator)
			if err != nil || frame == nil {
				t.Fatalf("%s: %v", kind, err)
			}
			var snap broadcast.TreeSnapshot
			if err := json.Unmarshal(frame.Payload, &snap); err != nil {
				t.Fatal(err)
			}
			if recipient == client {
				if snap.OwnChannelMove == nil || !snap.OwnChannelMove.Forced || snap.OwnChannelMove.ChannelID != 2 {
					t.Fatalf("%s lost own forced move: %s", kind, frame.Payload)
				}
			} else if snap.OwnChannelMove != nil {
				t.Fatalf("%s disclosed someone else's cause", kind)
			}
			if strings.Contains(string(frame.Payload), "private-actor") || strings.Contains(string(frame.Payload), "from_channel_id") {
				t.Fatal("move actor/source leaked")
			}
		}
	}
	if err := env.srv.moveClient(t.Context(), id, 1, id); err != nil {
		t.Fatal(err)
	}
	payload, _ := eventEnvelope(eventUserMoved, userEvent{ClientID: id})
	frame, err := env.srv.roleBroadcastFrame(client, payload, evaluator)
	if err != nil {
		t.Fatal(err)
	}
	var snap broadcast.TreeSnapshot
	if err := json.Unmarshal(frame.Payload, &snap); err != nil {
		t.Fatal(err)
	}
	if snap.OwnChannelMove == nil || snap.OwnChannelMove.Forced || snap.OwnChannelMove.ChannelID != 1 {
		t.Fatal("voluntary move retained an old forced cause")
	}
	// Integration moderation has authoritative actor identity but no desktop ID.
	if err := env.srv.moveRoleMember(t.Context(), evaluator, 2, "user-uid", "", netproto.MoveClient{ClientID: otherID, ChannelID: 2}); err != nil {
		t.Fatal(err)
	}
	adminSnap := env.srv.roleSnapshotWithOwnMove(other, evaluator)
	if adminSnap.OwnChannelMove == nil || !adminSnap.OwnChannelMove.Forced {
		t.Fatal("integration moderator move was classified as voluntary")
	}
	// A fresh session and a stale destination never inherit a move cause.
	if env.srv.roleSnapshotWithOwnMove(&Client{ID: id, UserID: 2, UniqueID: "user-uid"}, evaluator).OwnChannelMove != nil {
		t.Fatal("new session inherited move cause")
	}
	client.rememberOwnChannelMove(1, 2, true)
	if env.srv.roleSnapshotWithOwnMove(client, evaluator).OwnChannelMove != nil {
		t.Fatal("stale destination retained move cause")
	}
}
