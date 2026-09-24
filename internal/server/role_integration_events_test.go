package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"go.uber.org/zap"
	"noxa/internal/authorization"
	"noxa/internal/config"
	"noxa/internal/eventbus"
	"noxa/internal/state"
)

func integrationEventFixture(t *testing.T) (*TCPServer, *authorization.RoleEvaluator) {
	t.Helper()
	p := serverRoleFixture().policy
	p.OwnerID = 99
	p.Roles[0].Permissions = []authorization.Capability{authorization.ViewChannel, authorization.Connect, authorization.Speak}
	p.Channels = []authorization.ChannelPolicy{
		{ChannelID: 1, Overrides: []authorization.RoleOverride{{RoleID: 10, Capability: authorization.ViewChannel, Effect: authorization.Deny}}},
		{ChannelID: 2, ParentID: 1},
	}
	e, err := authorization.NewRoleEvaluator(p)
	if err != nil {
		t.Fatal(err)
	}
	sm := state.New(zap.NewNop())
	sm.AddChannel(&state.Channel{ChannelID: 1, Name: "private-parent"})
	sm.AddChannel(&state.Channel{ChannelID: 2, ParentID: 1, Name: "visible-child"})
	sm.AddClient(&state.Client{ClientID: "speaker", UserID: 3, UniqueID: "speaker-uid", Nickname: "Speaker"})
	if err := sm.MoveClient("speaker", 2); err != nil {
		t.Fatal(err)
	}
	sm.SetSpeaking("speaker", true)
	return New(&config.Config{}, zap.NewNop(), &Deps{State: sm}), e
}

func TestIntegrationStructuralEventsDiscardRawMetadata(t *testing.T) {
	s, e := integrationEventFixture(t)
	for _, kind := range []string{eventUserJoined, eventUserLeft, eventUserMoved, eventChannelCreated, eventChannelDeleted, eventChannelUpdated, eventStatusChanged, eventMemberVoiceChanged, eventKicked} {
		result, err := s.projectIntegrationEvent(context.Background(), e, 5, "viewer", eventbus.Event{Type: kind, Seq: 999, Data: json.RawMessage(`{"reason":"private-reason","from_channel_id":1,"nickname":"private-nickname"}`)})
		if err != nil || result.Snapshot == nil || result.Speaking != nil {
			t.Fatalf("%s: %+v %v", kind, result, err)
		}
		data, err := json.Marshal(result.Snapshot)
		if err != nil || strings.Contains(string(data), "private-") || len(result.Snapshot.RootChannels) != 1 || result.Snapshot.RootChannels[0].ParentID != 0 {
			t.Fatalf("%s leaked hidden parent/transition metadata: %s %v", kind, data, err)
		}
	}
}

func TestIntegrationSpeakingUsesCurrentStateAndVisibility(t *testing.T) {
	for _, kind := range []string{"valid", "hidden", "invisible", "stale channel", "stale speaking", "missing channel", "missing speaking", "malformed", "unknown", "direct chat"} {
		t.Run(kind, func(t *testing.T) {
			s, e := integrationEventFixture(t)
			event := eventbus.Event{Type: eventSpeakingChanged, Data: json.RawMessage(`{"client_id":"speaker","channel_id":2,"speaking":true,"nickname":"untrusted-name"}`)}
			switch kind {
			case "hidden":
				if err := s.deps.State.MoveClient("speaker", 1); err != nil {
					t.Fatal(err)
				}
				event.Data = json.RawMessage(`{"client_id":"speaker","channel_id":1,"speaking":true}`)
			case "invisible":
				s.deps.State.SetStatus("speaker", "invisible", "")
			case "stale channel":
				event.Data = json.RawMessage(`{"client_id":"speaker","channel_id":1,"speaking":true}`)
			case "stale speaking":
				s.deps.State.SetSpeaking("speaker", false)
			case "missing channel":
				event.Data = json.RawMessage(`{"client_id":"speaker","speaking":true}`)
			case "missing speaking":
				event.Data = json.RawMessage(`{"client_id":"speaker","channel_id":2}`)
			case "malformed":
				event.Data = json.RawMessage(`{`)
			case "unknown":
				event.Type = "future-event"
			case "direct chat":
				event.Type = eventChat
			}
			result, err := s.projectIntegrationEvent(t.Context(), e, 5, "viewer", event)
			if kind == "valid" {
				if err != nil || result.Speaking == nil || result.Speaking.ClientID != "speaker" || result.Speaking.ChannelID != 2 || !result.Speaking.Speaking {
					t.Fatalf("valid event: %+v %v", result, err)
				}
			} else if result.Speaking != nil || result.Snapshot != nil {
				t.Fatalf("%s leaked event: %+v", kind, result)
			}
		})
	}
}

func TestIntegrationSnapshotClearsUnauthorizedSpeaking(t *testing.T) {
	for _, kind := range []string{"allowed", "muted", "speak revoked", "unassigned"} {
		t.Run(kind, func(t *testing.T) {
			s, e := integrationEventFixture(t)
			switch kind {
			case "muted":
				muted := true
				if _, err := s.deps.State.SetServerVoiceState("speaker", &muted, nil); err != nil {
					t.Fatal(err)
				}
			case "speak revoked":
				p := e.Policy()
				p.Channels[1].Overrides = []authorization.RoleOverride{{RoleID: p.EveryoneID, Capability: authorization.Speak, Effect: authorization.Deny}}
				var err error
				e, err = authorization.NewRoleEvaluator(p)
				if err != nil {
					t.Fatal(err)
				}
			case "unassigned":
				if err := s.deps.State.LeaveChannel("speaker"); err != nil {
					t.Fatal(err)
				}
			}
			// An owner viewer must not override the source's speaking permission.
			snapshot, err := s.integrationSnapshot(t.Context(), e, 99, "owner")
			if err != nil {
				t.Fatal(err)
			}
			members := snapshot.UnassignedClients
			stack := snapshot.RootChannels
			for len(stack) > 0 {
				node := stack[len(stack)-1]
				stack = append(stack[:len(stack)-1], node.Children...)
				members = append(members, node.Clients...)
			}
			if len(members) != 1 || members[0].IsSpeaking != (kind == "allowed") {
				t.Fatalf("%s: %+v", kind, members)
			}
		})
	}
}
