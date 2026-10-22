//go:build integration

package channels

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"
	"noxa/internal/authorization"
	"noxa/internal/state"
)

func TestPublicPoolWorkerRecoversCommittedPolicyAndKeepsOccupiedRooms(t *testing.T) {
	s := roleLifecycleStore(t)
	var owner int64
	if err := s.DB().QueryRowContext(t.Context(), `INSERT INTO users(unique_id,nickname) VALUES('owner','Owner') RETURNING id`).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PrepareRolePolicy(t.Context(), owner); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ActivatePreparedRolePolicy(t.Context(), "owner", func() (uint16, []byte, error) { return 1, []byte("test-key"), nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnsureHomeChannels(t.Context()); err != nil {
		t.Fatal(err)
	}
	sm := state.New(zap.NewNop())
	m := New(s, sm, zap.NewNop())
	t.Cleanup(m.Close)
	var failNext atomic.Bool
	failed := make(chan struct{}, 1)
	a, err := authorization.NewAuthority(t.Context(), s, func(ctx context.Context, _, next *authorization.RoleEvaluator) error {
		if err := m.ReconcileRoleChannels(ctx, next.Policy(), func(result DeleteResult) error {
			if len(result.Members) != 0 {
				return errors.New("attempted to delete occupied room")
			}
			return nil
		}); err != nil {
			return err
		}
		if failNext.Swap(false) {
			failed <- struct{}{}
			return errors.New("injected failure after state reconciliation")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	m.EnableRoleMode(a)
	if err := a.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	var room int64
	for _, ch := range sm.ListChannels() {
		if ch.SystemKind == "public" {
			room = ch.ChannelID
			break
		}
	}
	sm.AddClient(&state.Client{ClientID: "member"})
	failNext.Store(true)
	if _, err := m.MoveClient("member", room); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); m.MaintainPublicChannels(ctx, a) }()
	defer func() { cancel(); <-done }()
	select {
	case <-failed:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not resize")
	}
	// State already has two spare rooms: recovery must still run before the
	// idle fast path, otherwise an acknowledged commit leaves access closed.
	m.requestPublicPool()
	wait := func(check func() bool) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if check() {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("pool did not converge")
	}
	wait(func() bool {
		return a.WithPolicy(t.Context(), func(*authorization.RoleEvaluator) error { return nil }) == nil
	})
	count := func() int {
		n := 0
		for _, ch := range sm.ListChannels() {
			if ch.SystemKind == "public" {
				n++
			}
		}
		return n
	}
	if count() != 3 {
		t.Fatalf("occupied room plus two empty rooms: %d", count())
	}
	if c, _ := sm.GetClient("member"); c.ChannelID != room {
		t.Fatal("occupied membership changed")
	}
	if _, err := m.LeaveClient("member"); err != nil {
		t.Fatal(err)
	}
	wait(func() bool { return count() == 2 })
	if err := a.WithPolicy(t.Context(), func(*authorization.RoleEvaluator) error { return nil }); err != nil {
		t.Fatal(err)
	}
}
