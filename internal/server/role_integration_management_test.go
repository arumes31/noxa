//go:build integration

package server

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"
	"noxa/internal/auth"
	"noxa/internal/authorization"
	"noxa/internal/netproto"
	"noxa/internal/store"
)

func integrationManagementStore(t *testing.T) *store.Store {
	t.Helper()
	dsn := os.Getenv("NOXA_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set NOXA_TEST_DATABASE_URL to a disposable PostgreSQL database")
	}
	admin, err := store.New(dsn, zap.NewNop(), 2, 1, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("noxa_integration_roles_%d", time.Now().UnixNano())
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

func TestIntegrationRoleManagementUsesTransactionalAuthority(t *testing.T) {
	db := integrationManagementStore(t)
	a := auth.New(db, zap.NewNop())
	create := func(nickname string) auth.IntegrationPrincipal {
		t.Helper()
		uid, err := a.RegisterUser(t.Context(), nickname, "integration-password")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.DB().ExecContext(t.Context(), "UPDATE users SET integration_enabled=TRUE WHERE unique_id=$1", uid); err != nil {
			t.Fatal(err)
		}
		p, err := a.AuthenticateIntegration(t.Context(), uid, "integration-password", "192.0.2.79")
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	owner, member := create("integration-owner"), create("integration-member")
	policy, err := db.PrepareRolePolicy(t.Context(), owner.UserID())
	if err != nil {
		t.Fatal(err)
	}
	var pending atomic.Bool
	var srv *TCPServer
	authority, err := authorization.NewAuthority(t.Context(), db, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error {
		if !srv.roleMetadataMu.TryLock() {
			return errors.New("admission retained metadata lock into reconciliation")
		}
		srv.roleMetadataMu.Unlock()
		if pending.Load() {
			return context.DeadlineExceeded
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	env := startTestEnvDeps(t, nil, nil, func(d *Deps) { d.Authority, d.Auth, d.Roles = authority, a, db })
	defer env.stop()
	srv = env.srv
	read := func(principal auth.IntegrationPrincipal) (netproto.RoleState, error) {
		var state netproto.RoleState
		err := srv.WithIntegrationRoleState(t.Context(), principal, 0, func(_ context.Context, response netproto.RoleState) error { state = response; return nil })
		return state, err
	}
	if _, err := read(member); !errors.Is(err, authorization.ErrRoleForbidden) {
		t.Fatalf("ordinary member could read roles: %v", err)
	}
	state, err := read(owner)
	if err != nil || state.ActorID != owner.UserID() || state.Policy.Revision != policy.Revision {
		t.Fatalf("owner projection: %+v %v", state, err)
	}
	members := func(principal auth.IntegrationPrincipal, revision int64) (authorization.MemberPage, error) {
		var page authorization.MemberPage
		err := srv.WithIntegrationRoleMembers(t.Context(), principal, authorization.MemberQuery{ExpectedRevision: revision, Search: "integration-"}, func(_ context.Context, response authorization.MemberPage) error {
			if srv.roleMetadataMu.TryLock() {
				srv.roleMetadataMu.Unlock()
				t.Fatal("member delivery released metadata barrier")
			}
			page = response
			return nil
		})
		return page, err
	}
	check := func(principal auth.IntegrationPrincipal, target, revision int64) (netproto.AccessCheckResult, error) {
		var result netproto.AccessCheckResult
		err := srv.WithIntegrationAccessCheck(t.Context(), principal, netproto.AccessCheck{UserID: target, ExpectedRevision: revision, Capability: authorization.ManageRoles}, func(_ context.Context, response netproto.AccessCheckResult) error {
			if srv.roleMetadataMu.TryLock() {
				srv.roleMetadataMu.Unlock()
				t.Fatal("access explanation released metadata barrier")
			}
			result = response
			return nil
		})
		return result, err
	}
	page, err := members(owner, 1)
	if err != nil || page.Revision != 1 || len(page.Entries) != 2 {
		t.Fatalf("offline member search: %+v %v", page, err)
	}
	for _, entry := range page.Entries {
		if entry.Manageable != (entry.UserID != owner.UserID()) {
			t.Fatalf("protected owner not represented: %+v", entry)
		}
	}
	preview, err := check(owner, member.UserID(), 1)
	if err != nil || preview.Decision.Allowed || !preview.CanManageMember || preview.Decision.Revision != 1 {
		t.Fatalf("ordinary member target preview: %+v %v", preview, err)
	}
	if _, err := members(member, 1); !errors.Is(err, authorization.ErrRoleForbidden) {
		t.Fatalf("ordinary member could search: %v", err)
	}
	if _, err := check(member, owner.UserID(), 1); !errors.Is(err, authorization.ErrRoleForbidden) {
		t.Fatalf("ordinary member could inspect access: %v", err)
	}
	if _, err := members(owner, 2); !errors.Is(err, authorization.ErrRoleConflict) {
		t.Fatalf("stale member search: %v", err)
	}
	if _, err := check(owner, member.UserID(), 2); !errors.Is(err, authorization.ErrRoleConflict) {
		t.Fatalf("stale access explanation: %v", err)
	}
	change := authorization.RoleChange{Kind: authorization.RoleCreate, ExpectedRevision: 1, Role: authorization.Role{Name: "Integration managed"}}
	if _, err := srv.ChangeIntegrationRoles(t.Context(), member, change); !errors.Is(err, authorization.ErrRoleForbidden) {
		t.Fatalf("ordinary member mutated roles: %v", err)
	}
	created, err := srv.ChangeIntegrationRoles(t.Context(), owner, change)
	if err != nil || created.Revision != 2 || created.CreatedRoleID <= 0 || created.EnforcementPending {
		t.Fatalf("create result: %+v %v", created, err)
	}
	if _, err := srv.ChangeIntegrationRoles(t.Context(), owner, change); !errors.Is(err, authorization.ErrRoleConflict) {
		t.Fatalf("stale mutation: %v", err)
	}
	var actor string
	var auditCount int
	if err := db.DB().QueryRowContext(t.Context(), "SELECT COUNT(*),COALESCE(MAX(actor_unique_id),'') FROM audit_log WHERE action='roles.role_create'").Scan(&auditCount, &actor); err != nil {
		t.Fatal(err)
	}
	if auditCount != 1 {
		t.Fatalf("expected one atomic create audit, got %d", auditCount)
	}
	if actor != owner.UniqueID() {
		t.Fatalf("audit used a supplied target rather than authenticated actor: %q", actor)
	}
	var administrator int64
	for _, role := range state.Policy.Roles {
		if role.Name == "Administrator" {
			administrator = role.ID
		}
	}
	assigned, err := srv.ChangeIntegrationRoles(t.Context(), owner, authorization.RoleChange{Kind: authorization.MemberRolesSet, ExpectedRevision: 2, UserID: member.UserID(), RoleIDs: []int64{administrator}})
	if err != nil || assigned.Revision != 3 {
		t.Fatalf("assignment: %+v %v", assigned, err)
	}
	if _, err := read(member); err != nil {
		t.Fatalf("newly assigned Administrator denied: %v", err)
	}
	if _, err := members(member, 3); err != nil {
		t.Fatalf("same principal's new search authority denied: %v", err)
	}
	preview, err = check(member, owner.UserID(), 3)
	if err != nil || !preview.Decision.Allowed || preview.CanManageMember {
		t.Fatalf("owner access conflated with management hierarchy: %+v %v", preview, err)
	}
	if _, err := srv.ChangeIntegrationRoles(t.Context(), member, authorization.RoleChange{Kind: authorization.MemberRolesSet, ExpectedRevision: 3, UserID: owner.UserID(), RoleIDs: []int64{administrator}}); !errors.Is(err, authorization.ErrRoleForbidden) {
		t.Fatalf("owner hierarchy bypass: %v", err)
	}
	if _, err := db.DB().ExecContext(t.Context(), "UPDATE users SET integration_enabled=FALSE WHERE id=$1", owner.UserID()); err != nil {
		t.Fatal(err)
	}
	change.ExpectedRevision = 3
	if _, err := srv.ChangeIntegrationRoles(t.Context(), owner, change); !errors.Is(err, auth.ErrIntegrationDenied) {
		t.Fatalf("disabled owner mutated roles: %v", err)
	}
	if _, err := members(owner, 3); !errors.Is(err, auth.ErrIntegrationDenied) {
		t.Fatalf("disabled owner searched members: %v", err)
	}
	if _, err := check(owner, member.UserID(), 3); !errors.Is(err, auth.ErrIntegrationDenied) {
		t.Fatalf("disabled owner inspected access: %v", err)
	}
	if _, err := read(member); err != nil {
		t.Fatalf("admission failure poisoned authority: %v", err)
	}
	if _, err := db.DB().ExecContext(t.Context(), "UPDATE users SET integration_enabled=TRUE WHERE id=$1", owner.UserID()); err != nil {
		t.Fatal(err)
	}
	pending.Store(true)
	result, err := srv.ChangeIntegrationRoles(t.Context(), owner, authorization.RoleChange{Kind: authorization.RoleDelete, ExpectedRevision: 3, RoleID: created.CreatedRoleID})
	if err != nil || result.Revision != 4 || !result.EnforcementPending {
		t.Fatalf("known commit lost: %+v %v", result, err)
	}
	if _, err := read(owner); !errors.Is(err, authorization.ErrAuthorizationUnavailable) {
		t.Fatalf("pending enforcement failed open: %v", err)
	}
	if _, err := members(owner, 4); !errors.Is(err, authorization.ErrAuthorizationUnavailable) {
		t.Fatalf("pending enforcement allowed member search: %v", err)
	}
	if _, err := check(owner, member.UserID(), 4); !errors.Is(err, authorization.ErrAuthorizationUnavailable) {
		t.Fatalf("pending enforcement allowed access inspection: %v", err)
	}
	persisted, err := db.RolePolicy(t.Context())
	if err != nil || persisted.Revision != 4 {
		t.Fatalf("committed state missing: %+v %v", persisted, err)
	}
}
