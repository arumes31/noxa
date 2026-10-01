package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"noxa/internal/authorization"
	"noxa/internal/netproto"
)

func TestRoleQueuedDeliveryUsesCurrentPolicy(t *testing.T) {
	backend := serverRoleFixture()
	backend.policy.Roles[0].Permissions = []authorization.Capability{authorization.ViewChannel, authorization.Connect, authorization.Speak, authorization.Whisper}
	authority, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	env := startTestEnvDeps(t, nil, nil, func(d *Deps) { d.Authority = authority })
	defer env.stop()
	env.state.AddChannel(testChannel(1))
	conn, id := dialAuthed(t, env.addr, "admin-uid")
	defer func() { _ = conn.Close() }()
	client, _ := env.srv.clientByID(id)
	ownerConn, ownerID := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = ownerConn.Close() }()
	ownerClient, _ := env.srv.clientByID(ownerID)
	for _, id := range []string{client.ID, ownerID} {
		if err := env.state.MoveClient(id, 1); err != nil {
			t.Fatal(err)
		}
	}
	inner, err := eventEnvelope(eventChat, netproto.ChatBroadcast{ChannelID: "1", Text: "queued private content"})
	if err != nil {
		t.Fatal(err)
	}
	queued, err := eventEnvelope(roleChannelDelivery, roleChannelEvent{ChannelID: 1, Payload: inner})
	if err != nil {
		t.Fatal(err)
	}
	before, err := authorization.NewRoleEvaluator(backend.policy)
	if err != nil {
		t.Fatal(err)
	}
	whisper, err := eventEnvelope(eventWhisper, whisperEvent{FromClientID: client.ID, ChannelID: 1, FromNickname: "whisper identity"})
	if err != nil {
		t.Fatal(err)
	}
	if frame, err := env.srv.roleBroadcastFrame(ownerClient, whisper, before); err != nil || frame == nil {
		t.Fatalf("authorized whisper dropped: %v", err)
	}
	for _, kind := range []string{eventSpeakingChanged, eventPrioritySpeakerChanged, eventPosition, eventScreenshareChanged} {
		stale, err := eventEnvelope(kind, map[string]any{"client_id": client.ID, "channel_id": 99})
		if err != nil {
			t.Fatal(err)
		}
		if frame, err := env.srv.roleBroadcastFrame(ownerClient, stale, before); err != nil || frame != nil {
			t.Fatalf("%s stale channel reference leaked: %+v %v", kind, frame, err)
		}
	}
	frame, err := env.srv.roleBroadcastFrame(client, queued, before)
	if err != nil || frame == nil || string(frame.Payload) != string(inner) {
		t.Fatalf("allowed delivery: %+v %v", frame, err)
	}
	next, err := authority.ChangeRolePolicy(t.Context(), 2, authorization.RoleChange{Kind: authorization.ChannelAccessSet, ExpectedRevision: 1, Channel: authorization.ChannelPolicy{ChannelID: 1, Overrides: []authorization.RoleOverride{{RoleID: 10, Capability: authorization.ViewChannel, Effect: authorization.Deny}}}})
	if err != nil {
		t.Fatal(err)
	}
	after, err := authorization.NewRoleEvaluator(next)
	if err != nil {
		t.Fatal(err)
	}
	if frame, err := env.srv.roleBroadcastFrame(ownerClient, whisper, after); err != nil || frame != nil {
		t.Fatalf("revoked queued whisper delivered: %+v %v", frame, err)
	}
	frame, err = env.srv.roleBroadcastFrame(client, queued, after)
	if err != nil || frame != nil {
		t.Fatalf("queued content survived revocation: %+v %v", frame, err)
	}
	// A producer accidentally bypassing the scope envelope also fails closed.
	frame, err = env.srv.roleBroadcastFrame(client, inner, before)
	if err != nil || frame != nil {
		t.Fatalf("unscoped channel payload accepted: %+v %v", frame, err)
	}
	for _, event := range []string{eventUserJoined, eventUserMoved, eventUserLeft, eventChannelCreated, eventChannelUpdated, eventChannelDeleted} {
		payload, err := eventEnvelope(event, map[string]any{"nickname": "private-identity", "name": "private-name", "channel_id": 1, "from_channel_id": 1})
		if err != nil {
			t.Fatal(err)
		}
		frame, err := env.srv.roleBroadcastFrame(client, payload, after)
		if err != nil || frame == nil || frame.Type != uint16(netproto.MsgSnapshot) {
			t.Fatalf("%s: %+v %v", event, frame, err)
		}
		if strings.Contains(string(frame.Payload), "private-") {
			t.Fatalf("%s leaked metadata: %s", event, frame.Payload)
		}
		var snapshot struct {
			TotalChannels int `json:"total_channels"`
		}
		if err := json.Unmarshal(frame.Payload, &snapshot); err != nil || snapshot.TotalChannels != 0 {
			t.Fatalf("hidden channel in snapshot: %s %v", frame.Payload, err)
		}
	}
}
