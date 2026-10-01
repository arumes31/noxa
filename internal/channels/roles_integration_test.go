//go:build integration

package channels

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"go.uber.org/zap"
	"noxa/internal/authorization"
	"noxa/internal/state"
	"noxa/internal/store"
)

// All role configuration lives in a disposable database, never the shared
// database used by legacy channel-manager integration tests.
func roleLifecycleStore(t *testing.T) *store.Store {
	t.Helper()
	base := os.Getenv("NOXA_TEST_DATABASE_URL")
	if base == "" {
		t.Skip("set NOXA_TEST_DATABASE_URL for isolated role lifecycle tests")
	}
	admin, err := sql.Open("postgres", base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	name := fmt.Sprintf("noxa_role_channels_%d", time.Now().UnixNano())
	if _, err := admin.ExecContext(t.Context(), "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := admin.ExecContext(ctx, "DROP DATABASE "+name); err != nil {
			t.Error(err)
		}
	})
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	s, err := store.New(u.String(), zap.NewNop(), 4, 1, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Migrate(); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestRoleChannelReconcileRejectsMismatchedPersistedTreeBeforeEffects(t *testing.T) {
	s := roleLifecycleStore(t)
	var owner, root, child int64
	if err := s.DB().QueryRowContext(t.Context(), `INSERT INTO users(unique_id,nickname) VALUES('owner','Owner') RETURNING id`).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	if err := s.DB().QueryRowContext(t.Context(), `INSERT INTO channels(name,channel_type) VALUES('Root',2) RETURNING id`).Scan(&root); err != nil {
		t.Fatal(err)
	}
	if err := s.DB().QueryRowContext(t.Context(), `INSERT INTO channels(name,channel_type,parent_id) VALUES('Child',2,$1) RETURNING id`, root).Scan(&child); err != nil {
		t.Fatal(err)
	}
	p, err := s.PrepareRolePolicy(t.Context(), owner)
	if err != nil {
		t.Fatal(err)
	}
	sm := state.New(zap.NewNop())
	m := New(s, sm, zap.NewNop())
	m.EnableRoleMode(nil)
	t.Cleanup(m.Close)
	sm.AddChannel(&state.Channel{ChannelID: root, Name: "Original", ChannelType: 2})
	detach := func(DeleteResult) error { t.Fatal("invalid tree reached removal effects"); return nil }
	// Simulate a resource writer outside the lifecycle contract. Neither the
	// parent mismatch nor an extra resource may partially mirror live state.
	if _, err := s.DB().ExecContext(t.Context(), `UPDATE channels SET parent_id=NULL WHERE id=$1`, child); err != nil {
		t.Fatal(err)
	}
	if err := m.ReconcileRoleChannels(t.Context(), p, detach); !errors.Is(err, authorization.ErrAuthorizationUnavailable) {
		t.Fatalf("parent mismatch: %v", err)
	}
	if ch, _ := sm.GetChannel(root); ch.Name != "Original" {
		t.Fatal("parent mismatch partially mirrored state")
	}
	if _, err := s.DB().ExecContext(t.Context(), `UPDATE channels SET parent_id=$1 WHERE id=$2`, root, child); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().ExecContext(t.Context(), `INSERT INTO channels(name,channel_type) VALUES('Extra',2)`); err != nil {
		t.Fatal(err)
	}
	if err := m.ReconcileRoleChannels(t.Context(), p, detach); !errors.Is(err, authorization.ErrAuthorizationUnavailable) {
		t.Fatalf("resource count mismatch: %v", err)
	}
	if len(sm.ListChannels()) != 1 {
		t.Fatal("extra resource partially mirrored state")
	}
}

func TestRoleChannelLifecycleRecoveryAndTimerRevalidation(t *testing.T) {
	s := roleLifecycleStore(t)
	var owner int64
	if err := s.DB().QueryRowContext(t.Context(), `INSERT INTO users(unique_id,nickname) VALUES('owner','Owner') RETURNING id`).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PrepareRolePolicy(t.Context(), owner); err != nil {
		t.Fatal(err)
	}
	sm := state.New(zap.NewNop())
	m := New(s, sm, zap.NewNop())
	m.SetCleanupDelay(time.Hour)
	t.Cleanup(m.Close)
	failDetach := false
	detaches := 0
	a, err := authorization.NewAuthority(t.Context(), s, func(ctx context.Context, _, after *authorization.RoleEvaluator) error {
		return m.ReconcileRoleChannels(ctx, after.Policy(), func(result DeleteResult) error {
			if len(result.Members) > 0 {
				detaches++
				if result.Members[0].ChannelID != result.ChannelIDs[0] {
					t.Fatal("old voice scope lost")
				}
			}
			if failDetach {
				return errors.New("detach failed")
			}
			return nil
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	m.EnableRoleMode(a)
	if _, err := m.LoadIntoState(t.Context()); err != nil {
		t.Fatal(err)
	}
	change := func(c authorization.ChannelTreeChange, record *store.RoleChannelCreate) (authorization.RolePolicy, int64, error) {
		p, err := a.RolePolicy(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		c.ExpectedRevision = p.Revision
		var id int64
		saved, err := a.ChangeLifecyclePolicy(t.Context(), p.Revision, func(ctx context.Context) (authorization.RolePolicy, error) {
			p, created, err := m.ChangeRoleChannel(ctx, owner, c, record)
			id = created
			return p, err
		})
		return saved, id, err
	}
	_, occupied, err := change(authorization.ChannelTreeChange{Kind: authorization.ChannelCreate, Access: authorization.ChannelPolicy{Synced: true}}, &store.RoleChannelCreate{Name: "Occupied", ChannelType: 2})
	if err != nil {
		t.Fatal(err)
	}
	sm.AddClient(&state.Client{ClientID: "voice"})
	if _, err := m.MoveClient("voice", occupied); err != nil {
		t.Fatal(err)
	}
	failDetach = true
	saved, _, err := change(authorization.ChannelTreeChange{Kind: authorization.ChannelDelete, ChannelID: occupied}, nil)
	if !errors.Is(err, authorization.ErrEnforcementPending) || saved.Revision != 3 {
		t.Fatalf("committed deletion: %+v %v", saved, err)
	}
	if id, _, _ := sm.ClientChannelState("voice"); id != occupied {
		t.Fatal("failed detach erased membership")
	}
	if _, err := a.RolePolicy(t.Context()); !errors.Is(err, authorization.ErrAuthorizationUnavailable) {
		t.Fatal("access reopened before cleanup")
	}
	failDetach = false
	if err := a.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	if id, _, _ := sm.ClientChannelState("voice"); id != 0 || detaches != 2 {
		t.Fatalf("retry lost old membership: %d attempts=%d", id, detaches)
	}
	_, temporary, err := change(authorization.ChannelTreeChange{Kind: authorization.ChannelCreate, Temporary: true, Access: authorization.ChannelPolicy{Synced: true}}, &store.RoleChannelCreate{Name: "Temporary", ChannelType: 0})
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	first := m.timers[temporary]
	m.mu.Unlock()
	if first == nil {
		t.Fatal("temporary cleanup not armed")
	}
	if err := a.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	preserved := m.timers[temporary] == first
	m.mu.Unlock()
	if !preserved {
		t.Fatal("unrelated reconciliation restarted cleanup deadline")
	}
	m.StartCleanupWatcher(temporary)
	m.cleanupCallback(temporary, first)
	if _, ok := sm.GetChannel(temporary); !ok {
		t.Fatal("stale timer deleted channel")
	}
	m.mu.Lock()
	current := m.timers[temporary]
	m.mu.Unlock()
	// Bypass MoveClient's timer cancellation to exercise the callback's final
	// occupancy check independently of that optimization.
	if err := sm.MoveClient("voice", temporary); err != nil {
		t.Fatal(err)
	}
	m.cleanupCallback(temporary, current)
	if _, ok := sm.GetChannel(temporary); !ok {
		t.Fatal("occupied channel was pruned")
	}
	if _, err := m.LeaveClient("voice"); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	current = m.timers[temporary]
	m.mu.Unlock()
	if current == nil {
		t.Fatal("cleanup was not rearmed after leave")
	}
	m.cleanupCallback(temporary, current)
	if _, ok := sm.GetChannel(temporary); ok {
		t.Fatal("eligible temporary channel survived cleanup")
	}
	p, err := a.RolePolicy(t.Context())
	if err != nil || p.Revision != 5 || len(p.Channels) != 0 {
		t.Fatalf("cleanup policy not published: %+v %v", p, err)
	}
}
