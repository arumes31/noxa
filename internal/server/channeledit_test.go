package server

import (
	"context"
	"encoding/json"
	"testing"

	"noxa/internal/authorization"
	"noxa/internal/netproto"
)

func TestPrioritySpeakerDenied(t *testing.T) {
	env := startTestEnv(t, nil)
	defer env.stop()

	conn, _ := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = conn.Close() }()

	send(t, conn, netproto.MsgPrioritySpeaker, netproto.PrioritySpeaker{Active: true})
	if got := readError(t, conn); got.Code != errCodePermissionDenied {
		t.Fatalf("priority speaker denial: %+v", got)
	}
}

func TestPrioritySpeakerOK(t *testing.T) {
	backend := serverRoleFixture()
	backend.policy.Roles[0].Permissions = append(backend.policy.Roles[0].Permissions, authorization.PrioritySpeaker)
	authority, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	env := startTestEnvDeps(t, nil, nil, func(d *Deps) { d.Authority = authority })
	defer env.stop()

	conn, clientID := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = conn.Close() }()
	env.state.AddChannel(testChannel(1))
	if err := env.state.MoveClient(clientID, 1); err != nil {
		t.Fatal(err)
	}

	send(t, conn, netproto.MsgPrioritySpeaker, netproto.PrioritySpeaker{Active: true})
	data := readEventOfType(t, conn, eventPrioritySpeakerChanged)
	var ev struct {
		ClientID string `json:"client_id"`
		Active   bool   `json:"active"`
	}
	if err := json.Unmarshal(data, &ev); err != nil {
		t.Fatalf("decode priority-speaker event: %v", err)
	}
	if ev.ClientID != clientID || !ev.Active {
		t.Fatalf("priority-speaker event = %+v, want client %s active", ev, clientID)
	}

	if current, ok := env.state.GetClient(clientID); !ok || !current.PrioritySpeaker {
		t.Fatal("priority-speaker state was not stored")
	}

	send(t, conn, netproto.MsgPrioritySpeaker, netproto.PrioritySpeaker{Active: false})
	readEventOfType(t, conn, eventPrioritySpeakerChanged)
	if current, ok := env.state.GetClient(clientID); !ok || current.PrioritySpeaker {
		t.Fatal("priority-speaker state was not cleared")
	}
}
