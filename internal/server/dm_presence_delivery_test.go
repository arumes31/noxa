package server

import (
	"noxa/internal/authorization"
	"testing"
)

func TestQueuedDMPresenceSignalsRespectInvisibility(t *testing.T) {
	backend := serverRoleFixture()
	backend.policy.Roles[0].Permissions = []authorization.Capability{authorization.ViewChannel}
	env := startTestEnv(t, nil)
	defer env.stop()
	actorConn, actorID := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = actorConn.Close() }()
	viewerConn, viewerID := dialAuthed(t, env.addr, "admin-uid")
	defer func() { _ = viewerConn.Close() }()
	viewer, _ := env.srv.clientByID(viewerID)
	e, err := authorization.NewRoleEvaluator(backend.policy)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{eventTyping, eventDMDelivered, eventDMRead, eventConversationTyping, eventConversationRead} {
		payload, err := eventEnvelope(kind, map[string]any{"client_id": actorID, "from_unique_id": "user-uid", "direct": true, "conversation_id": "b8e67721-144f-47df-8606-c4fe1cdf2140"})
		if err != nil {
			t.Fatal(err)
		}
		env.state.SetStatus(actorID, "online", "")
		if f, err := env.srv.roleBroadcastFrame(viewer, payload, e); err != nil || f == nil {
			t.Fatalf("visible signal dropped: %s %v", kind, err)
		}
		if kind == eventTyping {
			wrapped, err := eventEnvelope(roleChannelDelivery, roleChannelEvent{ChannelID: 0, Payload: payload})
			if err != nil {
				t.Fatal(err)
			}
			if f, err := env.srv.roleBroadcastFrame(viewer, wrapped, e); err != nil || f == nil {
				t.Fatalf("visible scoped typing dropped: %v", err)
			}
			env.state.SetStatus(actorID, "invisible", "")
			if f, err := env.srv.roleBroadcastFrame(viewer, wrapped, e); err != nil || f != nil {
				t.Fatalf("invisible scoped typing leaked: %v", err)
			}
		}
		env.state.SetStatus(actorID, "invisible", "")
		if f, err := env.srv.roleBroadcastFrame(viewer, payload, e); err != nil || f != nil {
			t.Fatalf("queued invisible signal leaked: %s %v", kind, err)
		}
	}
}
