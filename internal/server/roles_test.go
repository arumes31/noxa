package server

import (
	"context"
	"errors"
	"sync"
	"testing"

	"noxa/internal/authorization"
	"noxa/internal/netproto"
)

type memoryRoleStore struct {
	mu     sync.Mutex
	policy authorization.RolePolicy
	fail   bool
}

func (s *memoryRoleStore) RolePolicy(context.Context) (authorization.RolePolicy, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail {
		return authorization.RolePolicy{}, errors.New("private database detail")
	}
	return s.policy, nil
}

func (s *memoryRoleStore) ChangeRolePolicy(_ context.Context, actorID int64, change authorization.RoleChange) (authorization.RolePolicy, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail {
		return authorization.RolePolicy{}, errors.New("private database detail")
	}
	if change.Kind == authorization.RoleCreate {
		change.Role.ID = 50
	}
	next, err := authorization.ApplyRoleChange(s.policy, actorID, change)
	if err == nil {
		s.policy = next
	}
	return next, err
}

func serverRoleFixture() *memoryRoleStore {
	return &memoryRoleStore{policy: authorization.RolePolicy{
		Revision: 1, OwnerID: 2, EveryoneID: 10,
		Roles:    []authorization.Role{{ID: 10, Name: "@everyone"}},
		Channels: []authorization.ChannelPolicy{{ChannelID: 1}},
	}}
}

func TestRoleAPIUsesProtectedOwner(t *testing.T) {
	for _, tt := range []struct {
		name, identity string
		backend        bool
		code           uint16
	}{
		{"owner can manage", "user-uid", true, 0},
		{"non-owner cannot manage", "admin-uid", true, errCodePermissionDenied},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store := serverRoleFixture()
			env := startTestEnvDeps(t, nil, nil, func(d *Deps) {
				if tt.backend {
					authority, err := authorization.NewAuthority(t.Context(), store, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
					if err != nil {
						t.Fatal(err)
					}
					d.Authority = authority
				} else {
					d.Authority = nil
				}
			})
			defer env.stop()
			conn, _ := dialAuthed(t, env.addr, tt.identity)
			defer func() { _ = conn.Close() }()
			send(t, conn, netproto.MsgRoleQuery, netproto.RoleQuery{})
			if tt.code != 0 {
				got := readError(t, conn)
				if got.Code != tt.code || got.Message == "private database detail" {
					t.Fatalf("unexpected error: %+v", got)
				}
				return
			}
			var response netproto.RoleState
			if err := netproto.Decode(readOfType(t, conn, netproto.MsgRoleState), &response); err != nil {
				t.Fatal(err)
			}
			if response.ActorID != 2 || response.Policy.OwnerID != 2 || len(response.Capabilities) == 0 {
				t.Fatalf("unexpected state: %+v", response)
			}
		})
	}
}

func TestRoleAPIAcknowledgesCommitAndConflict(t *testing.T) {
	store := serverRoleFixture()
	authority, err := authorization.NewAuthority(t.Context(), store, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	env := startTestEnvDeps(t, nil, nil, func(d *Deps) { d.Authority = authority })
	defer env.stop()
	conn, _ := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = conn.Close() }()
	change := authorization.RoleChange{Kind: authorization.RoleCreate, ExpectedRevision: 1, Role: authorization.Role{Name: "Moderator", Permissions: []authorization.Capability{authorization.ManageRoles}}}
	send(t, conn, netproto.MsgRoleChange, change)
	var ack netproto.RoleChangeResult
	if err := netproto.Decode(readOfType(t, conn, netproto.MsgRoleChangeResult), &ack); err != nil {
		t.Fatal(err)
	}
	if ack.Revision != 2 || ack.CreatedRoleID != 50 {
		t.Fatalf("unexpected acknowledgement: %+v", ack)
	}
	send(t, conn, netproto.MsgRoleChange, change)
	if got := readError(t, conn); got.Code != errCodeConflict {
		t.Fatalf("expected conflict: %+v", got)
	}
	send(t, conn, netproto.MsgAccessCheck, netproto.AccessCheck{UserID: 1, ChannelID: 1, Capability: authorization.ViewChannel, ExpectedRevision: 2})
	var preview netproto.AccessCheckResult
	if err := netproto.Decode(readOfType(t, conn, netproto.MsgAccessCheckResult), &preview); err != nil {
		t.Fatal(err)
	}
	if preview.Decision.Allowed || preview.Decision.Revision != 2 {
		t.Fatalf("preview disagrees with policy: %+v", preview)
	}
}

func TestRoleAPIRejectsForgedActor(t *testing.T) {
	store := serverRoleFixture()
	authority, err := authorization.NewAuthority(t.Context(), store, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	env := startTestEnvDeps(t, nil, nil, func(d *Deps) { d.Authority = authority })
	defer env.stop()
	conn, _ := dialAuthed(t, env.addr, "admin-uid")
	defer func() { _ = conn.Close() }()
	send(t, conn, netproto.MsgRoleChange, map[string]any{"actor_id": 2, "user_id": 2, "kind": "role_delete", "role_id": 10, "expected_revision": 1})
	if got := readError(t, conn); got.Code != errCodePermissionDenied {
		t.Fatalf("forged identity accepted: %+v", got)
	}
}

func TestRoleAPIAcknowledgesSavedChangeWhenEnforcementFails(t *testing.T) {
	store := serverRoleFixture()
	authority, err := authorization.NewAuthority(t.Context(), store, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error {
		return errors.New("key rotation failed")
	})
	if err != nil {
		t.Fatal(err)
	}
	env := startTestEnvDeps(t, nil, nil, func(d *Deps) { d.Roles = store; d.Authority = authority })
	defer env.stop()
	conn, _ := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = conn.Close() }()
	send(t, conn, netproto.MsgRoleChange, authorization.RoleChange{Kind: authorization.RoleCreate, ExpectedRevision: 1, Role: authorization.Role{Name: "Member"}})
	var ack netproto.RoleChangeResult
	if err := netproto.Decode(readOfType(t, conn, netproto.MsgRoleChangeResult), &ack); err != nil {
		t.Fatal(err)
	}
	if ack.Revision != 2 || ack.CreatedRoleID != 50 || !ack.EnforcementPending {
		t.Fatalf("unexpected acknowledgement: %+v", ack)
	}
	client, ok := env.srv.clientByUniqueID("user-uid")
	if !ok {
		t.Fatal("control connection closed before acknowledgement")
	}
	payload, err := eventEnvelope(eventUserJoined, userEvent{Nickname: "queued private name"})
	if err != nil {
		t.Fatal(err)
	}
	if err := env.srv.writeRoleBroadcast(client, payload); err != nil {
		t.Fatalf("failed policy closed control delivery: %v", err)
	}
	send(t, conn, netproto.MsgRoleQuery, netproto.RoleQuery{})
	if got := readError(t, conn); got.Code != errCodeUnavailable {
		t.Fatalf("failed authority served data: %+v", got)
	}
}
