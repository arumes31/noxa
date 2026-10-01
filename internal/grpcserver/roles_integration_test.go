//go:build integration

package grpcserver

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"noxa/internal/auth"
	"noxa/internal/authorization"
	"noxa/internal/channels"
	"noxa/internal/config"
	"noxa/internal/query"
	"noxa/internal/server"
	"noxa/internal/state"
	"noxa/internal/store"
	noxav1 "noxa/v1"
)

// The concrete role adapter is the native server. Any legacy backend call in
// this test would panic instead of gaining implicit administrator authority.
type nativeRoleGRPCBackend struct {
	query.RoleIntegrationBackend
	query.RoleManagementBackend
	query.RoleChannelBackend
}

func grpcRoleTestStore(t *testing.T) *store.Store {
	t.Helper()
	dsn := os.Getenv("NOXA_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set NOXA_TEST_DATABASE_URL to a disposable PostgreSQL database")
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := store.New(dsn, zap.NewNop(), 2, 1, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	name := fmt.Sprintf("noxa_grpc_roles_%d", time.Now().UnixNano())
	if _, err := admin.DB().ExecContext(t.Context(), "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := admin.DB().ExecContext(ctx, "DROP DATABASE "+name); err != nil {
			t.Errorf("drop scratch database: %v", err)
		}
	})
	u.Path, u.RawPath = "/"+name, ""
	db, err := store.New(u.String(), zap.NewNop(), 5, 1, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestRoleGRPCTransactionalAuthority(t *testing.T) {
	db := grpcRoleTestStore(t)
	a := auth.New(db, zap.NewNop())
	const password = "grpc-integration-password"
	create := func(name string) auth.IntegrationPrincipal {
		t.Helper()
		uid, err := a.RegisterUser(t.Context(), name, password)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.DB().ExecContext(t.Context(), "UPDATE users SET integration_enabled=TRUE WHERE unique_id=$1", uid); err != nil {
			t.Fatal(err)
		}
		p, err := a.AuthenticateIntegration(t.Context(), uid, password, "127.0.0.1")
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	owner, member := create("grpc-owner"), create("grpc-member")
	if _, err := db.PrepareRolePolicy(t.Context(), owner.UserID()); err != nil {
		t.Fatal(err)
	}
	var pending atomic.Bool
	authority, err := authorization.NewAuthority(t.Context(), db, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error {
		if pending.Load() {
			return context.DeadlineExceeded
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	native := server.New(&config.Config{}, zap.NewNop(), &server.Deps{Auth: a, Authority: authority, Roles: db})
	b := &nativeRoleGRPCBackend{RoleIntegrationBackend: native, RoleManagementBackend: native, RoleChannelBackend: native}
	c := noxav1.NewControlClient(dialGRPC(t, startGRPC(t, b, nil)))
	login, err := c.Authenticate(roleModelCtx(t.Context()), &noxav1.AuthenticateRequest{Username: "grpc-owner", Password: password})
	if err != nil || login.GetUserId() != owner.UniqueID() {
		t.Fatalf("canonical login: %v %v", login, err)
	}
	request := &noxav1.ChangeRolesRequest{Kind: "role_create", ExpectedRevision: 1, Role: &noxav1.RoleDefinition{Name: "From gRPC"}, UserId: member.UserID()}
	if _, err := c.ChangeRoles(roleAuthCtx(t, "grpc-member", password), request); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("ordinary member bypass: %v", err)
	}
	created, err := c.ChangeRoles(roleAuthCtx(t, "grpc-owner", password), request)
	if err != nil || created.GetRevision() != 2 || created.GetCreatedRoleId() <= 0 || created.GetEnforcementPending() {
		t.Fatalf("create: %v %v", created, err)
	}
	if _, err := c.ChangeRoles(roleAuthCtx(t, owner.UniqueID(), password), request); status.Code(err) != codes.Aborted {
		t.Fatalf("stale revision: %v", err)
	}
	var actor string
	var count int
	if err := db.DB().QueryRowContext(t.Context(), "SELECT COUNT(*),COALESCE(MAX(actor_unique_id),'') FROM audit_log WHERE action='roles.role_create'").Scan(&count, &actor); err != nil {
		t.Fatal(err)
	}
	if count != 1 || actor != owner.UniqueID() {
		t.Fatalf("audit: count=%d actor=%q", count, actor)
	}
	if _, err := db.DB().ExecContext(t.Context(), "UPDATE users SET integration_enabled=FALSE WHERE id=$1", owner.UserID()); err != nil {
		t.Fatal(err)
	}
	request.ExpectedRevision = 2
	if _, err := c.ChangeRoles(roleAuthCtx(t, "grpc-owner", password), request); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("disabled identity: %v", err)
	}
	if _, err := db.DB().ExecContext(t.Context(), "UPDATE users SET integration_enabled=TRUE WHERE id=$1", owner.UserID()); err != nil {
		t.Fatal(err)
	}
	pending.Store(true)
	deleted, err := c.ChangeRoles(roleAuthCtx(t, "grpc-owner", password), &noxav1.ChangeRolesRequest{Kind: "role_delete", ExpectedRevision: 2, RoleId: created.GetCreatedRoleId()})
	if err != nil || deleted.GetRevision() != 3 || !deleted.GetEnforcementPending() {
		t.Fatalf("known pending commit: %v %v", deleted, err)
	}
	saved, err := db.RolePolicy(t.Context())
	if err != nil || saved.Revision != 3 {
		t.Fatalf("committed revision: %d %v", saved.Revision, err)
	}
	request.ExpectedRevision = 3
	if _, err := c.ChangeRoles(roleAuthCtx(t, "grpc-owner", password), request); status.Code(err) != codes.Unavailable {
		t.Fatalf("pending enforcement remained open: %v", err)
	}
}

func TestRoleGRPCChannelCommitAndAdmission(t *testing.T) {
	db := grpcRoleTestStore(t)
	a := auth.New(db, zap.NewNop())
	const password = "grpc-channel-password"
	uid, err := a.RegisterUser(t.Context(), "grpc-channel-owner", password)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB().ExecContext(t.Context(), "UPDATE users SET integration_enabled=TRUE WHERE unique_id=$1", uid); err != nil {
		t.Fatal(err)
	}
	owner, err := a.AuthenticateIntegration(t.Context(), uid, password, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.PrepareRolePolicy(t.Context(), owner.UserID()); err != nil {
		t.Fatal(err)
	}
	sm := state.New(zap.NewNop())
	manager := channels.New(db, sm, zap.NewNop())
	t.Cleanup(manager.Close)
	var pending atomic.Bool
	authority, err := authorization.NewAuthority(t.Context(), db, func(ctx context.Context, _, after *authorization.RoleEvaluator) error {
		if err := manager.ReconcileRoleChannels(ctx, after.Policy(), func(channels.DeleteResult) error { return nil }); err != nil {
			return err
		}
		if pending.Load() {
			return context.DeadlineExceeded
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	manager.EnableRoleMode(authority)
	native := server.New(&config.Config{FileRoot: t.TempDir()}, zap.NewNop(), &server.Deps{Auth: a, Authority: authority, Roles: db, Channels: manager, State: sm})
	b := &nativeRoleGRPCBackend{RoleIntegrationBackend: native, RoleManagementBackend: native, RoleChannelBackend: native}
	c := noxav1.NewControlClient(dialGRPC(t, startGRPC(t, b, nil)))
	request := &noxav1.ChangeChannelRequest{Kind: "channel_create", ExpectedRevision: 1, ChannelType: 2, Settings: &noxav1.RoleChannelSettings{Name: "gRPC room"}}
	created, err := c.ChangeChannel(roleAuthCtx(t, uid, password), request)
	if err != nil || created.GetRevision() != 2 || created.GetChannelId() <= 0 || created.GetEnforcementPending() {
		t.Fatalf("channel commit: %v %v", created, err)
	}
	if ch, ok := sm.GetChannel(created.GetChannelId()); !ok || ch.Name != "gRPC room" {
		t.Fatalf("channel not mirrored before acknowledgement: %+v", ch)
	}
	if _, err := c.ChangeChannel(roleAuthCtx(t, uid, password), request); status.Code(err) != codes.Aborted {
		t.Fatalf("stale channel change: %v", err)
	}
	if _, err := db.DB().ExecContext(t.Context(), "UPDATE users SET integration_enabled=FALSE WHERE unique_id=$1", uid); err != nil {
		t.Fatal(err)
	}
	deletion := &noxav1.ChangeChannelRequest{Kind: "channel_delete", ExpectedRevision: 2, ChannelId: created.GetChannelId()}
	if _, err := c.ChangeChannel(roleAuthCtx(t, uid, password), deletion); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("disabled channel owner: %v", err)
	}
	if _, err := db.DB().ExecContext(t.Context(), "UPDATE users SET integration_enabled=TRUE WHERE unique_id=$1", uid); err != nil {
		t.Fatal(err)
	}
	pending.Store(true)
	deleted, err := c.ChangeChannel(roleAuthCtx(t, uid, password), deletion)
	if err != nil || deleted.GetRevision() != 3 || !deleted.GetEnforcementPending() {
		t.Fatalf("saved pending deletion: %v %v", deleted, err)
	}
	if sm.ChannelCount() != 0 {
		t.Fatal("deleted channel remains live")
	}
	var count int
	var actor string
	if err := db.DB().QueryRowContext(t.Context(), "SELECT COUNT(*),COALESCE(MAX(actor_unique_id),'') FROM audit_log WHERE action IN ('roles.channel_create','roles.channel_delete')").Scan(&count, &actor); err != nil {
		t.Fatal(err)
	}
	if count != 2 || actor != uid {
		t.Fatalf("authenticated channel audit: %d %q", count, actor)
	}
}
