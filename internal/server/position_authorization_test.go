package server

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"noxa/internal/authorization"
	"noxa/internal/broadcast"
	"noxa/internal/netproto"
	"noxa/internal/state"
)

func positionControlFixture(t *testing.T) (*TCPServer, *Client, *Client, <-chan []byte, authorization.RolePolicy) {
	t.Helper()
	backend := serverRoleFixture()
	backend.policy.Roles[0].Permissions = []authorization.Capability{authorization.ViewChannel, authorization.Connect}
	backend.policy.Channels = append(backend.policy.Channels, authorization.ChannelPolicy{ChannelID: 2})
	authority, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	sm := state.New(testLogger())
	for _, id := range []int64{1, 2} {
		sm.AddChannel(testChannel(id))
	}
	sender := &Client{ID: "sender", UserID: 1, UniqueID: "sender-uid"}
	recipient := &Client{ID: "recipient", UserID: 3, UniqueID: "recipient-uid"}
	for _, member := range []*Client{sender, recipient} {
		sm.AddClient(&state.Client{ClientID: member.ID, UserID: member.UserID, UniqueID: member.UniqueID})
		if err := sm.MoveClient(member.ID, 1); err != nil {
			t.Fatal(err)
		}
	}
	b := broadcast.New(testLogger(), sm)
	t.Cleanup(b.Close)
	out, err := b.Register(recipient.ID)
	if err != nil {
		t.Fatal(err)
	}
	return &TCPServer{deps: &Deps{Authority: authority, State: sm, Broadcast: b}}, sender, recipient, out, backend.policy
}

func readPositionQueue(t *testing.T, out <-chan []byte) positionEvent {
	t.Helper()
	select {
	case payload := <-out:
		var envelope struct {
			Type string        `json:"type"`
			Data positionEvent `json:"data"`
		}
		if err := json.Unmarshal(payload, &envelope); err != nil || envelope.Type != eventPosition {
			t.Fatalf("position event=%s error=%v", payload, err)
		}
		return envelope.Data
	default:
		t.Fatal("position was not synchronously queued")
		return positionEvent{}
	}
}

func assertPositionQueueEmpty(t *testing.T, out <-chan []byte) {
	t.Helper()
	select {
	case payload := <-out:
		t.Fatalf("unexpected queued position: %s", payload)
	default:
	}
}

func TestPositionServerRejectsInvalidCoordinatesAndContext(t *testing.T) {
	env := startTestEnv(t, nil)
	defer env.stop()
	conn, id := dialAuthed(t, env.addr, "admin-uid")
	defer func() { _ = conn.Close() }()
	client, ok := env.srv.clientByID(id)
	if !ok {
		t.Fatal("authenticated client missing")
	}
	env.state.AddChannel(testChannel(1))
	if err := env.state.MoveClient(id, 1); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		message netproto.PositionUpdate
	}{
		{"missing channel", netproto.PositionUpdate{Context: "map"}},
		{"negative channel", netproto.PositionUpdate{ChannelID: -1, Context: "map"}},
		{"missing context", netproto.PositionUpdate{ChannelID: 1}},
		{"context too long", netproto.PositionUpdate{ChannelID: 1, Context: strings.Repeat("a", 129)}},
		{"invalid UTF8 context", netproto.PositionUpdate{ChannelID: 1, Context: string([]byte{0xff})}},
		{"x outside range", netproto.PositionUpdate{ChannelID: 1, Context: "map", X: 1_000_001}},
		{"y outside range", netproto.PositionUpdate{ChannelID: 1, Context: "map", Y: -1_000_001}},
		{"z outside range", netproto.PositionUpdate{ChannelID: 1, Context: "map", Z: 1_000_001}},
		{"NaN", netproto.PositionUpdate{ChannelID: 1, Context: "map", X: math.NaN()}},
		{"positive infinity", netproto.PositionUpdate{ChannelID: 1, Context: "map", Y: math.Inf(1)}},
		{"negative infinity", netproto.PositionUpdate{ChannelID: 1, Context: "map", Z: math.Inf(-1)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Call the handler directly so invalid UTF-8 and nonfinite numbers
			// reach its validation before JSON encoding can normalize/reject them.
			if err := env.srv.rolePositionUpdate(t.Context(), client, tc.message); err != nil {
				t.Fatal(err)
			}
			if got := readError(t, conn); got.Code != errCodeMalformed || got.Message != "invalid position" {
				t.Fatalf("invalid position response=%+v", got)
			}
			client.mu.Lock()
			last := client.lastPositionAt
			client.mu.Unlock()
			if !last.IsZero() {
				t.Fatal("invalid position consumed the update rate limit")
			}
		})
	}
}

func TestPositionServerRateLimitsSameChannelUpdates(t *testing.T) {
	srv, sender, _, out, _ := positionControlFixture(t)
	message := netproto.PositionUpdate{ChannelID: 1, Context: "map", X: 1}
	if err := srv.rolePositionUpdate(t.Context(), sender, message); err != nil {
		t.Fatal(err)
	}
	if got := readPositionQueue(t, out); got.X != 1 || got.Context != "map" || got.ChannelID != 1 {
		t.Fatalf("first position=%+v", got)
	}
	sender.mu.Lock()
	sender.lastPositionAt = time.Now()
	sender.mu.Unlock()
	message.X = 2
	if err := srv.rolePositionUpdate(t.Context(), sender, message); err != nil {
		t.Fatal(err)
	}
	assertPositionQueueEmpty(t, out)
	// Advance the stored timestamp across the window without a timing sleep.
	sender.mu.Lock()
	sender.lastPositionAt = time.Now().Add(-time.Second)
	sender.mu.Unlock()
	message.X = 3
	if err := srv.rolePositionUpdate(t.Context(), sender, message); err != nil {
		t.Fatal(err)
	}
	if got := readPositionQueue(t, out); got.X != 3 {
		t.Fatalf("rate limit did not recover: %+v", got)
	}
}

func TestPositionServerIgnoresLateWrongChannelUpdate(t *testing.T) {
	srv, sender, recipient, out, _ := positionControlFixture(t)
	for _, client := range []*Client{sender, recipient} {
		if err := srv.deps.State.MoveClient(client.ID, 2); err != nil {
			t.Fatal(err)
		}
	}
	if err := srv.rolePositionUpdate(t.Context(), sender, netproto.PositionUpdate{ChannelID: 1, Context: "old-map", X: 123}); err != nil {
		t.Fatal(err)
	}
	assertPositionQueueEmpty(t, out)
	sender.mu.Lock()
	last := sender.lastPositionAt
	sender.mu.Unlock()
	if !last.IsZero() {
		t.Fatal("late position consumed the new channel's rate limit")
	}
	if err := srv.rolePositionUpdate(t.Context(), sender, netproto.PositionUpdate{ChannelID: 2, Context: "new-map", X: 456}); err != nil {
		t.Fatal(err)
	}
	if got := readPositionQueue(t, out); got.ChannelID != 2 || got.Context != "new-map" || got.X != 456 {
		t.Fatalf("new-channel position=%+v", got)
	}
}

func TestPositionQueuedDeliveryDropsAfterMembershipChange(t *testing.T) {
	for _, change := range []string{"recipient leaves", "recipient moves", "recipient disconnects", "sender moves", "both move", "connect revoked", "view revoked"} {
		t.Run(change, func(t *testing.T) {
			srv, sender, recipient, _, policy := positionControlFixture(t)
			before, err := authorization.NewRoleEvaluator(policy)
			if err != nil {
				t.Fatal(err)
			}
			payload, err := eventEnvelope(eventPosition, positionEvent{ClientID: sender.ID, ChannelID: 1, Context: "map", X: 77})
			if err != nil {
				t.Fatal(err)
			}
			if frame, err := srv.roleBroadcastFrame(recipient, payload, before); err != nil || frame == nil {
				t.Fatalf("initial same-channel position rejected: %v", err)
			}
			switch change {
			case "recipient leaves":
				if err := srv.deps.State.LeaveChannel(recipient.ID); err != nil {
					t.Fatal(err)
				}
			case "recipient moves":
				if err := srv.deps.State.MoveClient(recipient.ID, 2); err != nil {
					t.Fatal(err)
				}
			case "recipient disconnects":
				srv.deps.State.RemoveClient(recipient.ID)
			case "sender moves":
				if err := srv.deps.State.MoveClient(sender.ID, 2); err != nil {
					t.Fatal(err)
				}
			case "both move":
				for _, client := range []*Client{sender, recipient} {
					if err := srv.deps.State.MoveClient(client.ID, 2); err != nil {
						t.Fatal(err)
					}
				}
			case "connect revoked", "view revoked":
				capability := authorization.Connect
				if change == "view revoked" {
					capability = authorization.ViewChannel
				}
				policy.Revision++
				policy.Channels[0].Overrides = []authorization.RoleOverride{{UserID: recipient.UserID, Capability: capability, Effect: authorization.Deny}}
			}
			after, err := authorization.NewRoleEvaluator(policy)
			if err != nil {
				t.Fatal(err)
			}
			if frame, err := srv.roleBroadcastFrame(recipient, payload, after); err != nil || frame != nil {
				t.Fatalf("queued position leaked after %s: frame=%v error=%v", change, frame, err)
			}
		})
	}
}
