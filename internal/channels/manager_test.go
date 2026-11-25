package channels

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"go.uber.org/zap"

	"noxa/internal/authorization"
	"noxa/internal/state"
	"noxa/internal/store"
)

// testEnv constructs a ChannelManager backed by a real Postgres store if one
// is reachable. It skips the calling test when no database is available. It
// also runs migrations and returns a fresh state.Manager.
func testEnv(t *testing.T) (*ChannelManager, *store.Store, *state.Manager) {
	t.Helper()

	dbURL := os.Getenv("NOXA_TEST_DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://noxa:noxa@localhost:5432/noxa?sslmode=disable"
	}

	logger, err := zap.NewDevelopment()
	if err != nil {
		t.Fatalf("zap.NewDevelopment: %v", err)
	}

	s, err := store.New(dbURL, logger, 5, 1, time.Minute)
	if err != nil {
		if os.Getenv("NOXA_TEST_DATABASE_URL") != "" {
			t.Fatalf("configured database unavailable: %v", err)
		}
		t.Skipf("database unavailable, skipping: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if err := s.Migrate(); err != nil {
		t.Fatalf("database migration failed: %v", err)
	}

	sm := state.New(logger)
	mgr := New(s, sm, logger)
	t.Cleanup(func() { mgr.Close() })
	return mgr, s, sm
}

// timerEnv returns a manager whose cleanup timers are armed but never fire
// during the test, so it needs no database.
func timerEnv(t *testing.T) (*ChannelManager, *state.Manager) {
	t.Helper()
	a, err := authorization.NewAuthority(t.Context(), cleanupPolicyBackend{}, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	sm := state.New(zap.NewNop())
	mgr := New(nil, sm, zap.NewNop())
	mgr.SetCleanupDelay(time.Hour)
	mgr.EnableRoleMode(a)
	t.Cleanup(mgr.Close)
	return mgr, sm
}

func TestClientLeaveAndRemovalSerializeWithMove(t *testing.T) {
	setup := func(t *testing.T) (*ChannelManager, *state.Manager, int64, int64, string) {
		t.Helper()
		mgr, sm := timerEnv(t)
		const sourceID, targetID = int64(1), int64(2)
		sm.AddChannel(&state.Channel{ChannelID: sourceID, ChannelType: int(ChannelTypePermanent)})
		sm.AddChannel(&state.Channel{ChannelID: targetID, ChannelType: int(ChannelTypeTemporary)})
		mgr.StartCleanupWatcher(targetID)
		clientID := "serialized-client"
		sm.AddClient(&state.Client{ClientID: clientID})
		if _, err := mgr.MoveClient(clientID, sourceID); err != nil {
			t.Fatal(err)
		}
		return mgr, sm, sourceID, targetID, clientID
	}

	t.Run("leave then move", func(t *testing.T) {
		mgr, sm, sourceID, targetID, clientID := setup(t)
		leaveReady := make(chan struct{})
		releaseLeave := make(chan struct{})
		mgr.testHooks.beforeLeaveClient = func(string) {
			close(leaveReady)
			<-releaseLeave
		}
		leaveResult := make(chan error, 1)
		go func() {
			oldChannelID, err := mgr.LeaveClient(clientID)
			if err == nil && oldChannelID != sourceID {
				err = fmt.Errorf("old channel = %d, want %d", oldChannelID, sourceID)
			}
			leaveResult <- err
		}()
		<-leaveReady
		moveResult := make(chan error, 1)
		go func() {
			_, err := mgr.MoveClient(clientID, targetID)
			moveResult <- err
		}()
		select {
		case err := <-moveResult:
			t.Fatalf("move crossed leave lifecycle lock: %v", err)
		default:
		}
		close(releaseLeave)
		if err := <-leaveResult; err != nil {
			t.Fatal(err)
		}
		if err := <-moveResult; err != nil {
			t.Fatal(err)
		}
		channelID, _, ok := sm.ClientChannelState(clientID)
		if !ok || channelID != targetID {
			t.Fatalf("final client state = (%d, %v), want target %d", channelID, ok, targetID)
		}
		if got := mgr.CleanupTimersCount(); got != 0 {
			t.Fatalf("target cleanup timer survived successful move: %d", got)
		}
	})

	t.Run("remove then move", func(t *testing.T) {
		mgr, _, sourceID, targetID, clientID := setup(t)
		removeReady := make(chan struct{})
		releaseRemove := make(chan struct{})
		mgr.testHooks.beforeRemoveClient = func(string) {
			close(removeReady)
			<-releaseRemove
		}
		removeResult := make(chan error, 1)
		go func() {
			removed, err := mgr.RemoveClient(clientID)
			if err == nil && (removed == nil || removed.ChannelID != sourceID) {
				err = fmt.Errorf("removed client = %+v, want source %d", removed, sourceID)
			}
			removeResult <- err
		}()
		<-removeReady
		moveResult := make(chan error, 1)
		go func() {
			_, err := mgr.MoveClient(clientID, targetID)
			moveResult <- err
		}()
		select {
		case err := <-moveResult:
			t.Fatalf("move crossed remove lifecycle lock: %v", err)
		default:
		}
		close(releaseRemove)
		if err := <-removeResult; err != nil {
			t.Fatal(err)
		}
		if err := <-moveResult; !errors.Is(err, state.ErrClientNotFound) {
			t.Fatalf("move after removal error = %v, want client not found", err)
		}
		if got := mgr.CleanupTimersCount(); got != 1 {
			t.Fatalf("empty target cleanup timer count = %d, want 1", got)
		}
	})
}

// TestMoveClient_CancelsCleanup verifies that leaving a temporary channel arms
// its cleanup timer and that joining it again cancels the timer.
func TestMoveClient_CancelsCleanup(t *testing.T) {
	mgr, sm := timerEnv(t)
	const id = int64(1)
	sm.AddChannel(&state.Channel{ChannelID: id, ChannelType: int(ChannelTypeTemporary)})

	clientID := "test-client-join-cancel"
	sm.AddClient(&state.Client{ClientID: clientID})
	if _, err := mgr.MoveClient(clientID, id); err != nil {
		t.Fatalf("MoveClient: %v", err)
	}
	if _, err := mgr.LeaveClient(clientID); err != nil {
		t.Fatalf("LeaveClient: %v", err)
	}
	if mgr.CleanupTimersCount() != 1 {
		t.Fatalf("expected 1 timer after leave, got %d", mgr.CleanupTimersCount())
	}

	if _, err := mgr.MoveClient(clientID, id); err != nil {
		t.Fatalf("MoveClient rejoin: %v", err)
	}
	if mgr.CleanupTimersCount() != 0 {
		t.Fatalf("expected 0 timers after rejoin, got %d", mgr.CleanupTimersCount())
	}
}

// TestClose_CancelsAllTimers verifies that Close cancels all pending timers.
func TestClose_CancelsAllTimers(t *testing.T) {
	mgr, sm := timerEnv(t)
	for id := int64(1); id <= 3; id++ {
		sm.AddChannel(&state.Channel{ChannelID: id, ChannelType: int(ChannelTypeTemporary)})
		mgr.StartCleanupWatcher(id)
	}
	if got := mgr.CleanupTimersCount(); got != 3 {
		t.Fatalf("cleanup timers = %d, want 3", got)
	}

	mgr.Close()
	if mgr.CleanupTimersCount() != 0 {
		t.Fatalf("expected 0 timers after Close, got %d", mgr.CleanupTimersCount())
	}
}

func insertChannelRow(t *testing.T, s *store.Store, name string, parentID int64, channelType ChannelType, maxClients int) int64 {
	t.Helper()
	var parent any
	if parentID != 0 {
		parent = parentID
	}
	var maxc any
	if maxClients > 0 {
		maxc = maxClients
	}
	var id int64
	const q = `INSERT INTO channels (parent_id, name, topic, order_index, channel_type, max_clients)
	          VALUES ($1, $2, $3, 0, $4, $5) RETURNING id`
	err := s.DB().QueryRowContext(context.Background(), q,
		parent, name, name+" topic", int16(channelType), maxc,
	).Scan(&id)
	if err != nil {
		t.Fatalf("insertChannelRow: %v", err)
	}
	t.Cleanup(func() {
		_, _ = s.DB().ExecContext(context.Background(), `DELETE FROM channels WHERE id = $1`, id)
	})
	return id
}

// TestLoadIntoState verifies that persisted channels are loaded into the
// state manager with their type, parent, topic, and max_clients, and that no
// cleanup timer is armed before the authority is installed.
func TestLoadIntoState(t *testing.T) {
	mgr, s, sm := testEnv(t)

	if _, err := s.DB().ExecContext(t.Context(), "DELETE FROM channels"); err != nil {
		t.Fatalf("failed to clean channels table: %v", err)
	}

	rootID := insertChannelRow(t, s, "LoadRoot", 0, ChannelTypePermanent, 10)
	childID := insertChannelRow(t, s, "LoadChild", rootID, ChannelTypeSemiPermanent, 0)
	tempID := insertChannelRow(t, s, "LoadTemp", 0, ChannelTypeTemporary, 0)

	count, err := mgr.LoadIntoState(context.Background())
	if err != nil {
		t.Fatalf("LoadIntoState: %v", err)
	}
	if count != 3 {
		t.Fatalf("LoadIntoState count = %d, want 3", count)
	}

	root, ok := sm.GetChannel(rootID)
	if !ok {
		t.Fatalf("root channel %d not in state", rootID)
	}
	if root.Name != "LoadRoot" || root.ChannelType != int(ChannelTypePermanent) ||
		root.MaxClients != 10 || root.Topic != "LoadRoot topic" {
		t.Fatalf("root channel = %+v", root)
	}

	child, ok := sm.GetChannel(childID)
	if !ok {
		t.Fatalf("child channel %d not in state", childID)
	}
	if child.ParentID != rootID || child.ChannelType != int(ChannelTypeSemiPermanent) {
		t.Fatalf("child channel = %+v, want parent %d semi-permanent", child, rootID)
	}

	if _, ok := sm.GetChannel(tempID); !ok {
		t.Fatalf("temp channel %d not in state", tempID)
	}

	if got := mgr.CleanupTimersCount(); got != 0 {
		t.Fatalf("cleanup timers before the authority = %d, want 0", got)
	}
}
