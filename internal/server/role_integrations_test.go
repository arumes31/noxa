//go:build integration

package server

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"go.uber.org/zap"
	"noxa/internal/auth"
	"noxa/internal/authorization"
	"noxa/internal/broadcast"
	"noxa/internal/state"
	"noxa/internal/store"
)

func TestIntegrationSnapshotUsesCurrentIdentityAndRoles(t *testing.T) {
	dsn := os.Getenv("NOXA_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set NOXA_TEST_DATABASE_URL to a disposable PostgreSQL database")
	}
	db, err := store.New(dsn, zap.NewNop(), 5, 1, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	a := auth.New(db, zap.NewNop())
	uid, err := a.RegisterUser(t.Context(), fmt.Sprintf("integration-tree-%d", time.Now().UnixNano()), "integration-password")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.DB().ExecContext(context.Background(), "DELETE FROM users WHERE unique_id = $1", uid)
	})
	if _, err := db.DB().ExecContext(t.Context(), "UPDATE users SET integration_enabled = TRUE WHERE unique_id = $1", uid); err != nil {
		t.Fatal(err)
	}
	u, err := a.LookupUser(t.Context(), uid)
	if err != nil {
		t.Fatal(err)
	}
	backend := serverRoleFixture()
	backend.policy.OwnerID = u.ID + 1
	backend.policy.Channels = []authorization.ChannelPolicy{
		{ChannelID: 1},
		{ChannelID: 2, ParentID: 1, Overrides: []authorization.RoleOverride{{RoleID: 10, Capability: authorization.ViewChannel, Effect: authorization.Allow}}},
	}
	authority, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	env := startTestEnvDeps(t, nil, nil, func(d *Deps) { d.Authority, d.Auth = authority, a })
	defer env.stop()
	env.srv.deps.State.AddChannel(&state.Channel{ChannelID: 2, ParentID: 1, Name: "visible child"})
	p, err := env.srv.AuthenticateIntegration(t.Context(), uid, "integration-password", "192.0.2.89")
	if err != nil {
		t.Fatal(err)
	}
	read := func(wantChannels int) {
		t.Helper()
		called := false
		err := env.srv.WithIntegrationSnapshot(t.Context(), p, func(_ context.Context, snapshot *broadcast.TreeSnapshot) error {
			called = true
			if snapshot.TotalChannels != wantChannels {
				t.Fatalf("visible count = %d, want %d", snapshot.TotalChannels, wantChannels)
			}
			if wantChannels == 1 && (len(snapshot.RootChannels) != 1 || snapshot.RootChannels[0].ChannelID != 2 || snapshot.RootChannels[0].ParentID != 0) {
				t.Fatalf("hidden parent leaked: %+v", snapshot.RootChannels)
			}
			if env.srv.roleMetadataMu.TryLock() {
				env.srv.roleMetadataMu.Unlock()
				t.Fatal("metadata barrier released before delivery")
			}
			return nil
		})
		if err != nil || !called {
			t.Fatalf("snapshot callback=%v error=%v", called, err)
		}
	}
	read(1)
	backend.mu.Lock()
	backend.policy.Revision++
	backend.policy.Channels[1].Overrides = nil
	backend.mu.Unlock()
	if err := authority.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	read(0) // the same principal cannot retain its old visible tree
	if _, err := db.DB().ExecContext(t.Context(), "UPDATE users SET integration_enabled = FALSE WHERE unique_id = $1", uid); err != nil {
		t.Fatal(err)
	}
	if err := env.srv.WithIntegrationSnapshot(t.Context(), p, func(context.Context, *broadcast.TreeSnapshot) error {
		t.Fatal("disabled account reached delivery")
		return nil
	}); !errors.Is(err, auth.ErrIntegrationDenied) {
		t.Fatalf("disabled account error: %v", err)
	}
}
