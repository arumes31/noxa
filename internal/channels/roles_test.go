package channels

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.uber.org/zap"
	"noxa/internal/authorization"
	"noxa/internal/state"
)

type cleanupPolicyBackend struct{}

func (cleanupPolicyBackend) RolePolicy(context.Context) (authorization.RolePolicy, error) {
	return authorization.RolePolicy{Revision: 1, OwnerID: 1, EveryoneID: 10,
		Roles:    []authorization.Role{{ID: 10, Name: "@everyone"}},
		Channels: []authorization.ChannelPolicy{{ChannelID: 1}},
	}, nil
}

func (cleanupPolicyBackend) ChangeRolePolicy(context.Context, int64, authorization.RoleChange) (authorization.RolePolicy, error) {
	return authorization.RolePolicy{}, authorization.ErrRoleInvalid
}

func TestRoleCleanupCannotRearmAfterClose(t *testing.T) {
	a, err := authorization.NewAuthority(t.Context(), cleanupPolicyBackend{}, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	sm := state.New(zap.NewNop())
	sm.AddChannel(&state.Channel{ChannelID: 1, ChannelType: 0})
	m := New(nil, sm, zap.NewNop())
	m.SetCleanupDelay(time.Hour)
	m.EnableRoleMode(a)
	t.Cleanup(m.Close)
	m.StartCleanupWatcher(1)
	m.mu.Lock()
	expected := m.timers[1]
	m.mu.Unlock()
	if expected == nil {
		t.Fatal("mode setup permanently stopped cleanup")
	}
	// A callback delayed behind a failed write sees the unavailable authority
	// only after shutdown canceled its timer. Its retry must remain canceled.
	_, err = a.ChangeLifecyclePolicy(t.Context(), 1, func(context.Context) (authorization.RolePolicy, error) {
		return authorization.RolePolicy{}, errors.New("ambiguous write")
	})
	if err == nil {
		t.Fatal("expected failed write")
	}
	m.Close()
	m.cleanupRoleChannel(a, 1, expected)
	m.StartCleanupWatcher(1)
	if m.CleanupTimersCount() != 0 {
		t.Fatal("cleanup restarted after shutdown")
	}
}

func TestRoleChannelReconcilePreservesMembershipUntilCleanupSucceeds(t *testing.T) {
	sm := state.New(zap.NewNop())
	m := New(nil, sm, zap.NewNop())
	m.EnableRoleMode(nil)
	t.Cleanup(m.Close)
	sm.AddChannel(&state.Channel{ChannelID: 1, Name: "Deleted", ChannelType: 2})
	sm.AddChannel(&state.Channel{ChannelID: 2, ParentID: 1, Name: "Kept", ChannelType: 2, HasIcon: true})
	sm.AddClient(&state.Client{ClientID: "speaker"})
	sm.AddClient(&state.Client{ClientID: "stays"})
	for id, channel := range map[string]int64{"speaker": 1, "stays": 2} {
		if err := sm.MoveClient(id, channel); err != nil {
			t.Fatal(err)
		}
	}
	persisted := []*state.Channel{{ChannelID: 2, Name: "Moved", ChannelType: 2}, {ChannelID: 3, Name: "New", ChannelType: 2}}
	retained := map[int64]bool{2: true, 3: true}
	failed := errors.New("file revocation failed")
	calls := 0
	cleanup := func(result DeleteResult) error {
		calls++
		if len(result.ChannelIDs) != 1 || result.ChannelIDs[0] != 1 || len(result.Members) != 1 || result.Members[0] != (DeletedMember{ClientID: "speaker", ChannelID: 1}) {
			t.Fatalf("lost deletion consequences: %+v", result)
		}
		if ch, _, _ := sm.ClientChannelState("speaker"); ch != 1 {
			t.Fatal("membership removed before detach")
		}
		if calls == 1 {
			return failed
		}
		return nil
	}
	if err := m.reconcileRoleChannelState(t.Context(), persisted, retained, cleanup); !errors.Is(err, failed) {
		t.Fatal(err)
	}
	if ch, _ := sm.GetChannel(2); ch.Name != "Kept" || ch.ParentID != 1 {
		t.Fatal("failed cleanup partially mirrored tree")
	}
	if err := m.reconcileRoleChannelState(t.Context(), persisted, retained, cleanup); err != nil {
		t.Fatal(err)
	}
	if _, ok := sm.GetChannel(1); ok {
		t.Fatal("deleted channel retained")
	}
	if ch, _, _ := sm.ClientChannelState("speaker"); ch != 0 {
		t.Fatal("deleted membership retained")
	}
	if ch, _, _ := sm.ClientChannelState("stays"); ch != 2 {
		t.Fatal("retained member detached")
	}
	if ch, _ := sm.GetChannel(2); ch.ParentID != 0 || ch.Name != "Moved" || !ch.HasIcon {
		t.Fatalf("mirror lost state: %+v", ch)
	}
	if err := m.reconcileRoleChannelState(t.Context(), persisted, retained, cleanup); err != nil || calls != 2 {
		t.Fatalf("retry was not idempotent: %v calls=%d", err, calls)
	}
}

func TestRoleModeRejectsLegacyWritersAndUnconfiguredCleanup(t *testing.T) {
	sm := state.New(zap.NewNop())
	m := New(nil, sm, zap.NewNop())
	t.Cleanup(m.Close)
	sm.AddChannel(&state.Channel{ChannelID: 1, ChannelType: 0})
	m.SetCleanupDelay(time.Hour)
	m.StartCleanupWatcher(1)
	m.mu.Lock()
	old := m.timers[1]
	m.mu.Unlock()
	m.EnableRoleMode(nil)
	m.cleanupCallback(1, old)
	m.StartCleanupWatcher(1)
	if m.CleanupTimersCount() != 0 {
		t.Fatal("legacy cleanup rearmed")
	}
	if _, ok := sm.GetChannel(1); !ok {
		t.Fatal("legacy timer deleted a role channel")
	}
	if _, err := m.CreateChannel(t.Context(), ChannelSpec{Name: "Blocked", Type: ChannelTypePermanent}); !errors.Is(err, ErrRoleLifecycleRequired) {
		t.Fatal(err)
	}
	if _, err := m.DeleteChannelSubtree(t.Context(), 1); !errors.Is(err, ErrRoleLifecycleRequired) {
		t.Fatal(err)
	}
	if err := m.SetChannelType(t.Context(), 1, ChannelTypePermanent); !errors.Is(err, ErrRoleLifecycleRequired) {
		t.Fatal(err)
	}
	topic := "Blocked"
	if err := m.UpdateChannel(t.Context(), 1, ChannelUpdate{Topic: &topic}); !errors.Is(err, ErrRoleLifecycleRequired) {
		t.Fatal(err)
	}
}
