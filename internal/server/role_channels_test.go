package server

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"noxa/internal/authorization"
	"noxa/internal/channels"
	"noxa/internal/state"
)

// The real manager's resource mirroring is covered against PostgreSQL in the
// channels package. This adapter exercises the server's cleanup ordering.
type deletingRoleChannels struct {
	ChannelBackend
	state       *state.Manager
	beforeErase func()
}

func (m *deletingRoleChannels) ReconcileRoleChannels(_ context.Context, p authorization.RolePolicy, detach func(channels.DeleteResult) error) error {
	keep := map[int64]bool{}
	for _, ch := range p.Channels {
		keep[ch.ChannelID] = true
	}
	deleted := channels.DeleteResult{}
	for _, ch := range m.state.ListChannels() {
		if keep[ch.ChannelID] {
			continue
		}
		deleted.ChannelIDs = append(deleted.ChannelIDs, ch.ChannelID)
		for _, member := range m.state.ChannelMembers(ch.ChannelID) {
			deleted.Members = append(deleted.Members, channels.DeletedMember{ClientID: member.ClientID, ChannelID: ch.ChannelID})
		}
	}
	if err := detach(deleted); err != nil {
		return err
	}
	if len(deleted.ChannelIDs) > 0 && m.beforeErase != nil {
		m.beforeErase()
	}
	m.state.RemoveChannels(deleted.ChannelIDs)
	return nil
}

type retryRoleRecorder struct {
	RecordingBackend
	fail  atomic.Bool
	stops atomic.Int32
}

func (r *retryRoleRecorder) Stop(id int64) error {
	r.stops.Add(1)
	if r.fail.Load() {
		return errors.New("recorder unavailable")
	}
	return r.RecordingBackend.Stop(id)
}

func TestRoleChannelDeletionDetachesBeforeStateRemovalAndRetriesRecording(t *testing.T) {
	backend := serverRoleFixture()
	var env *testEnv
	a, err := authorization.NewAuthority(t.Context(), backend, func(ctx context.Context, before, after *authorization.RoleEvaluator) error {
		return env.srv.reconcileRolePolicy(ctx, before, after)
	})
	if err != nil {
		t.Fatal(err)
	}
	var manager *deletingRoleChannels
	var recording *retryRoleRecorder
	env = startTestEnvDeps(t, nil, nil, func(d *Deps) {
		d.Authority = a
		manager = &deletingRoleChannels{ChannelBackend: d.Channels, state: d.State}
		d.Channels = manager
		recording = &retryRoleRecorder{RecordingBackend: d.Recorder}
		recording.fail.Store(true)
		d.Recorder = recording
	})
	defer env.stop()
	env.state.AddChannel(testChannel(1))
	conn, id := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = conn.Close() }()
	if err := env.state.MoveClient(id, 1); err != nil {
		t.Fatal(err)
	}
	env.state.SetSpeaking(id, true)
	env.state.SetSharing(id, true)
	env.state.SetPrioritySpeaker(id, true)
	env.srv.roleRecordingOwners.Store(int64(1), int64(2))
	manager.beforeErase = func() {
		if ch, _, _ := env.state.ClientChannelState(id); ch != 1 {
			t.Fatal("membership erased before media detach")
		}
		env.voice.mu.Lock()
		defer env.voice.mu.Unlock()
		if len(env.voice.leaves) != 1 || env.voice.leaves[0] != ([2]any{id, int64(1)}) {
			t.Fatalf("old voice scope not detached: %v", env.voice.leaves)
		}
		env.ft.mu.Lock()
		defer env.ft.mu.Unlock()
		if len(env.ft.channelsMarked) != 1 || env.ft.channelsMarked[0] != 1 {
			t.Fatal("files not tombstoned before state removal")
		}
	}
	saved, err := a.ChangeLifecyclePolicy(t.Context(), 1, func(context.Context) (authorization.RolePolicy, error) {
		backend.mu.Lock()
		defer backend.mu.Unlock()
		p, err := authorization.ApplyChannelTreeChange(backend.policy, 2, authorization.ChannelTreeChange{Kind: authorization.ChannelDelete, ExpectedRevision: 1, ChannelID: 1})
		if err == nil {
			backend.policy = p
		}
		return p, err
	})
	if !errors.Is(err, authorization.ErrEnforcementPending) || saved.Revision != 2 {
		t.Fatalf("saved result: %+v %v", saved, err)
	}
	if _, exists := env.state.GetChannel(1); exists {
		t.Fatal("channel was not removed")
	}
	for _, member := range env.state.ListClients() {
		if member.ClientID == id && (member.ChannelID != 0 || member.IsSpeaking || member.PrioritySpeaker || member.Sharing) {
			t.Fatalf("deleted channel activity survived: %+v", member)
		}
	}
	if _, exists := env.srv.roleRecordingOwners.Load(int64(1)); !exists {
		t.Fatal("failed recording cleanup lost retry owner")
	}
	recording.fail.Store(false)
	if err := a.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	if recording.stops.Load() != 2 {
		t.Fatal("recording cleanup did not retry after state removal")
	}
	if _, exists := env.srv.roleRecordingOwners.Load(int64(1)); exists {
		t.Fatal("completed recording cleanup retained owner")
	}
}

func TestRoleChannelDeletionRetriesIconCleanupBeforeStateRemoval(t *testing.T) {
	env := startTestEnv(t, nil)
	defer env.stop()
	env.state.AddChannel(testChannel(1))
	manager := &deletingRoleChannels{state: env.state}
	// A malformed icon directory makes cleanup fail without relying on OS ACLs.
	if err := os.MkdirAll(env.srv.cfg.FileRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	iconDir := filepath.Join(env.srv.cfg.FileRoot, "icons")
	if err := os.WriteFile(iconDir, []byte("invalid directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	policy := authorization.RolePolicy{}
	if err := manager.ReconcileRoleChannels(t.Context(), policy, env.srv.detachRoleDeletedChannels); err == nil {
		t.Fatal("icon cleanup failure was discarded")
	}
	if _, ok := env.state.GetChannel(1); !ok {
		t.Fatal("deleted channel lost cleanup retry state")
	}
	if err := os.Remove(iconDir); err != nil {
		t.Fatal(err)
	}
	if _, err := env.srv.assets().writeImage("icons", "1", ".png", tinyPNG); err != nil {
		t.Fatal(err)
	}
	if _, err := env.srv.assets().writeImage("icons", "2", ".png", tinyPNG); err != nil {
		t.Fatal(err)
	}
	manager.beforeErase = func() {
		if _, _, err := env.srv.assets().readImage("icons", "1"); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("icon remains before state removal: %v", err)
		}
	}
	if err := manager.ReconcileRoleChannels(t.Context(), policy, env.srv.detachRoleDeletedChannels); err != nil {
		t.Fatal(err)
	}
	if _, ok := env.state.GetChannel(1); ok {
		t.Fatal("channel remains after successful cleanup")
	}
	if _, _, err := env.srv.assets().readImage("icons", "2"); err != nil {
		t.Fatalf("unrelated icon changed: %v", err)
	}
	if err := env.srv.detachRoleDeletedChannels(channels.DeleteResult{ChannelIDs: []int64{1}}); err != nil {
		t.Fatalf("repeat cleanup: %v", err)
	}
}
