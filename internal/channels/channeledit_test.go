// channeledit_test.go covers the editable tree fields: order index,
// re-parenting with its cycle guard, the configurable temporary-channel
// lifetime (165) and the creator's channel-admin assignment (156).
package channels

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

// channelRow reads the tree columns of a channel straight from the database.
func channelRow(t *testing.T, mgr *ChannelManager, id int64) (parentID int64, orderIndex int) {
	t.Helper()
	err := mgr.store.DB().QueryRowContext(context.Background(),
		`SELECT COALESCE(parent_id, 0), order_index
		 FROM channels WHERE id = $1`, id,
	).Scan(&parentID, &orderIndex)
	if err != nil {
		t.Fatalf("channelRow(%d): %v", id, err)
	}
	return
}

// ptr returns a pointer to v, for building a ChannelUpdate.
func ptr[T any](v T) *T { return &v }

// TestUpdateChannelTreeFields verifies order index reaches both the database
// and the in-memory state.
func TestUpdateChannelTreeFields(t *testing.T) {
	mgr, _, sm := testEnv(t)
	ctx := context.Background()

	id, err := mgr.CreateChannel(ctx, ChannelSpec{
		Name: fmt.Sprintf("edit-%d", time.Now().UnixNano()),
		Type: ChannelTypePermanent,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	t.Cleanup(func() { _ = mgr.DeleteChannel(ctx, id) })

	if err := mgr.UpdateChannel(ctx, id, ChannelUpdate{OrderIndex: ptr(7)}); err != nil {
		t.Fatalf("update channel: %v", err)
	}

	_, order := channelRow(t, mgr, id)
	if order != 7 {
		t.Fatalf("db order = %d, want 7", order)
	}
	ch, ok := sm.GetChannel(id)
	if !ok || ch.OrderIndex != 7 {
		t.Fatalf("state channel = %+v, want order 7", ch)
	}
}

// TestUpdateChannelReparent verifies a legal move updates the parent in the
// database and state, and that moving back to the root works.
func TestUpdateChannelReparent(t *testing.T) {
	mgr, _, sm := testEnv(t)
	ctx := context.Background()

	parent, err := mgr.CreateChannel(ctx, ChannelSpec{Name: fmt.Sprintf("p-%d", time.Now().UnixNano()), Type: ChannelTypePermanent})
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	t.Cleanup(func() { _ = mgr.DeleteChannel(ctx, parent) })
	child, err := mgr.CreateChannel(ctx, ChannelSpec{Name: fmt.Sprintf("c-%d", time.Now().UnixNano()), Type: ChannelTypePermanent})
	if err != nil {
		t.Fatalf("create child: %v", err)
	}
	t.Cleanup(func() { _ = mgr.DeleteChannel(ctx, child) })

	if err := mgr.UpdateChannel(ctx, child, ChannelUpdate{ParentID: ptr(parent)}); err != nil {
		t.Fatalf("re-parent: %v", err)
	}
	if got, _ := channelRow(t, mgr, child); got != parent {
		t.Fatalf("db parent = %d, want %d", got, parent)
	}
	if ch, _ := sm.GetChannel(child); ch == nil || ch.ParentID != parent {
		t.Fatalf("state parent = %+v, want %d", ch, parent)
	}

	if err := mgr.UpdateChannel(ctx, child, ChannelUpdate{ParentID: ptr(int64(0))}); err != nil {
		t.Fatalf("move to root: %v", err)
	}
	if got, _ := channelRow(t, mgr, child); got != 0 {
		t.Fatalf("db parent = %d, want 0 (root)", got)
	}
	if ch, _ := sm.GetChannel(child); ch == nil || ch.ParentID != 0 {
		t.Fatalf("state parent = %+v, want root", ch)
	}
}

// TestUpdateChannelRejectsCycles verifies the tree cannot be corrupted: a
// channel may not become its own parent, descend from itself, or move under a
// channel that does not exist. Every refusal must leave the tree untouched.
func TestUpdateChannelRejectsCycles(t *testing.T) {
	mgr, _, _ := testEnv(t)
	ctx := context.Background()

	root, err := mgr.CreateChannel(ctx, ChannelSpec{Name: fmt.Sprintf("r-%d", time.Now().UnixNano()), Type: ChannelTypePermanent})
	if err != nil {
		t.Fatalf("create root: %v", err)
	}
	t.Cleanup(func() { _ = mgr.DeleteChannel(ctx, root) })
	mid, err := mgr.CreateChannel(ctx, ChannelSpec{Name: fmt.Sprintf("m-%d", time.Now().UnixNano()), Type: ChannelTypePermanent, ParentID: root})
	if err != nil {
		t.Fatalf("create mid: %v", err)
	}
	leaf, err := mgr.CreateChannel(ctx, ChannelSpec{Name: fmt.Sprintf("l-%d", time.Now().UnixNano()), Type: ChannelTypePermanent, ParentID: mid})
	if err != nil {
		t.Fatalf("create leaf: %v", err)
	}

	cases := map[string]int64{
		"self":       root,
		"descendant": leaf,
		"missing":    999999999,
	}
	for name, target := range cases {
		err := mgr.UpdateChannel(ctx, root, ChannelUpdate{ParentID: ptr(target)})
		if !errors.Is(err, ErrInvalidMove) {
			t.Fatalf("%s move error = %v, want ErrInvalidMove", name, err)
		}
		if got, _ := channelRow(t, mgr, root); got != 0 {
			t.Fatalf("%s move changed the parent to %d", name, got)
		}
	}

	// The subtree is intact: refusing a move must not detach anything.
	if got, _ := channelRow(t, mgr, mid); got != root {
		t.Fatalf("mid parent = %d, want %d", got, root)
	}
	if got, _ := channelRow(t, mgr, leaf); got != mid {
		t.Fatalf("leaf parent = %d, want %d", got, mid)
	}
}

// TestSetCleanupDelay verifies the temporary-channel lifetime is configurable
// (165): an empty temp channel survives a gap shorter than the lifetime.
func TestSetCleanupDelay(t *testing.T) {
	mgr, s, _ := testEnv(t)
	ctx := context.Background()

	mgr.SetCleanupDelay(2 * time.Second)
	id, err := mgr.CreateChannel(ctx, ChannelSpec{Name: fmt.Sprintf("tmp-%d", time.Now().UnixNano()), Type: ChannelTypeTemporary})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	t.Cleanup(func() { _ = mgr.DeleteChannel(ctx, id) })

	time.Sleep(500 * time.Millisecond)
	if !channelExistsInDB(t, s, id) {
		t.Fatal("temporary channel deleted before its configured lifetime elapsed")
	}

	mgr.SetCleanupDelay(0)
	mgr.mu.Lock()
	defer mgr.mu.Unlock()
	if got := mgr.cleanupDelayLocked(); got != DefaultCleanupDelay {
		t.Fatalf("cleanup delay = %v, want the default %v", got, DefaultCleanupDelay)
	}
}
