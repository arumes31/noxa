//go:build integration

package server

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"go.uber.org/zap"
	"noxa/internal/auth"
	"noxa/internal/authorization"
	"noxa/internal/channels"
	"noxa/internal/config"
	"noxa/internal/netproto"
	"noxa/internal/state"
)

func TestIntegrationChannelsUseTransactionalLifecycle(t *testing.T) {
	db := integrationManagementStore(t)
	a := auth.New(db, zap.NewNop())
	createIdentity := func(name string) auth.IntegrationPrincipal {
		t.Helper()
		uid, err := a.RegisterUser(t.Context(), name, "integration-password")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.DB().ExecContext(t.Context(), "UPDATE users SET integration_enabled=TRUE WHERE unique_id=$1", uid); err != nil {
			t.Fatal(err)
		}
		p, err := a.AuthenticateIntegration(t.Context(), uid, "integration-password", "127.0.0.1")
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	owner, member := createIdentity("channel-owner"), createIdentity("channel-member")
	policy, err := db.PrepareRolePolicy(t.Context(), owner.UserID())
	if err != nil {
		t.Fatal(err)
	}
	sm := state.New(zap.NewNop())
	manager := channels.New(db, sm, zap.NewNop())
	t.Cleanup(manager.Close)
	var srv *TCPServer
	var pending atomic.Bool
	authority, err := authorization.NewAuthority(t.Context(), db, func(ctx context.Context, before, after *authorization.RoleEvaluator) error {
		if !srv.roleMetadataMu.TryLock() {
			return errors.New("admission retained metadata lock into reconciliation")
		}
		srv.roleMetadataMu.Unlock()
		if err := srv.reconcileRoleChannels(ctx, before, after); err != nil {
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
	srv = New(&config.Config{FileRoot: t.TempDir()}, zap.NewNop(), &Deps{Auth: a, Authority: authority, Roles: db, State: sm, Channels: manager})
	query := func(principal auth.IntegrationPrincipal, request netproto.RoleChannelQuery) (netproto.RoleChannelState, error) {
		var result netproto.RoleChannelState
		err := srv.WithIntegrationChannelState(t.Context(), principal, request, func(_ context.Context, response netproto.RoleChannelState) error {
			if srv.roleMetadataMu.TryLock() {
				srv.roleMetadataMu.Unlock()
				t.Fatal("channel metadata released before delivery")
			}
			result = response
			return nil
		})
		return result, err
	}
	options, err := query(owner, netproto.RoleChannelQuery{Kind: authorization.ChannelCreate})
	if err != nil || options.Revision != 1 || !options.CanCreatePermanent || !options.CanManageAccess {
		t.Fatalf("creation options: %+v %v", options, err)
	}
	request := netproto.RoleChannelChange{Kind: authorization.ChannelCreate, ExpectedRevision: 1, ChannelType: 2, Settings: &netproto.RoleChannelSettings{Name: "Parent"}, Password: "channel-password-canary"}
	if _, err := srv.ChangeIntegrationChannel(t.Context(), member, request); !errors.Is(err, authorization.ErrRoleForbidden) {
		t.Fatalf("ordinary member created a channel: %v", err)
	}
	parent, err := srv.ChangeIntegrationChannel(t.Context(), owner, request)
	if err != nil || parent.Revision != 2 || parent.ChannelID <= 0 || parent.EnforcementPending {
		t.Fatalf("parent: %+v %v", parent, err)
	}
	if _, err := srv.ChangeIntegrationChannel(t.Context(), owner, request); !errors.Is(err, authorization.ErrRoleConflict) {
		t.Fatalf("stale creation: %v", err)
	}
	var passwordHash string
	if err := db.DB().QueryRowContext(t.Context(), "SELECT password_hash FROM channels WHERE id=$1", parent.ChannelID).Scan(&passwordHash); err != nil {
		t.Fatal(err)
	}
	if passwordHash == request.Password || auth.VerifyPassword(request.Password, passwordHash) != nil {
		t.Fatal("password not hashed")
	}
	if _, err := query(member, netproto.RoleChannelQuery{Kind: authorization.ChannelEdit, ChannelID: parent.ChannelID}); !errors.Is(err, authorization.ErrRoleForbidden) {
		t.Fatalf("hidden channel options disclosed: %v", err)
	}
	childAccess := &netproto.RoleChannelAccess{Overrides: []authorization.RoleOverride{{RoleID: policy.EveryoneID, Capability: authorization.ViewChannel, Effect: authorization.Allow}}}
	child, err := srv.ChangeIntegrationChannel(t.Context(), owner, netproto.RoleChannelChange{Kind: authorization.ChannelCreate, ExpectedRevision: 2, ParentID: parent.ChannelID, ChannelType: 2, Settings: &netproto.RoleChannelSettings{Name: "Child"}, Access: childAccess})
	if err != nil || child.Revision != 3 {
		t.Fatalf("child: %+v %v", child, err)
	}
	edit := netproto.RoleChannelChange{Kind: authorization.ChannelEdit, ExpectedRevision: 3, ChannelID: child.ChannelID, Settings: &netproto.RoleChannelSettings{Name: "Edited child", Topic: "Topic", MaxClients: 7, OpusBitrate: 64000}}
	if _, err := db.DB().ExecContext(t.Context(), "UPDATE users SET integration_enabled=FALSE WHERE id=$1", owner.UserID()); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.ChangeIntegrationChannel(t.Context(), owner, edit); !errors.Is(err, auth.ErrIntegrationDenied) {
		t.Fatalf("disabled owner edited: %v", err)
	}
	if _, err := query(owner, netproto.RoleChannelQuery{Kind: authorization.ChannelEdit, ChannelID: child.ChannelID}); !errors.Is(err, auth.ErrIntegrationDenied) {
		t.Fatalf("disabled owner read options: %v", err)
	}
	if p, err := authority.RolePolicy(t.Context()); err != nil || p.Revision != 3 {
		t.Fatalf("admission denial changed authority: %d %v", p.Revision, err)
	}
	if _, err := db.DB().ExecContext(t.Context(), "UPDATE users SET integration_enabled=TRUE WHERE id=$1", owner.UserID()); err != nil {
		t.Fatal(err)
	}
	if result, err := srv.ChangeIntegrationChannel(t.Context(), owner, edit); err != nil || result.Revision != 4 {
		t.Fatalf("edit: %+v %v", result, err)
	}
	if ch, ok := sm.GetChannel(child.ChannelID); !ok || ch.Name != "Edited child" || ch.MaxClients != 7 {
		t.Fatalf("edit was not mirrored: %+v", ch)
	}
	options, err = query(owner, netproto.RoleChannelQuery{Kind: authorization.ChannelMove, ChannelID: child.ChannelID})
	if err != nil || options.Revision != 4 || options.AffectedChannels != 1 {
		t.Fatalf("move options: %+v %v", options, err)
	}
	if result, err := srv.ChangeIntegrationChannel(t.Context(), owner, netproto.RoleChannelChange{Kind: authorization.ChannelMove, ExpectedRevision: 4, ChannelID: child.ChannelID}); err != nil || result.Revision != 5 {
		t.Fatalf("move: %+v %v", result, err)
	}
	saved, err := db.RolePolicy(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, ch := range saved.Channels {
		if ch.ChannelID == child.ChannelID && (ch.ParentID != 0 || ch.Synced || !reflect.DeepEqual(ch.Overrides, childAccess.Overrides)) {
			t.Fatalf("keep-access move changed policy: %+v", ch)
		}
	}
	if result, err := srv.ChangeIntegrationChannel(t.Context(), owner, netproto.RoleChannelChange{Kind: authorization.ChannelDelete, ExpectedRevision: 5, ChannelID: parent.ChannelID}); err != nil || result.Revision != 6 {
		t.Fatalf("delete parent: %+v %v", result, err)
	}
	if _, ok := sm.GetChannel(parent.ChannelID); ok {
		t.Fatal("deleted parent remains live")
	}
	if _, ok := sm.GetChannel(child.ChannelID); !ok {
		t.Fatal("moving child did not preserve it after parent deletion")
	}
	pending.Store(true)
	result, err := srv.ChangeIntegrationChannel(t.Context(), owner, netproto.RoleChannelChange{Kind: authorization.ChannelDelete, ExpectedRevision: 6, ChannelID: child.ChannelID})
	if err != nil || result.Revision != 7 || !result.EnforcementPending {
		t.Fatalf("known pending delete: %+v %v", result, err)
	}
	saved, err = db.RolePolicy(t.Context())
	if err != nil || saved.Revision != 7 || len(saved.Channels) != 0 || sm.ChannelCount() != 0 {
		t.Fatalf("committed deletion not persisted/mirrored: %+v %v", saved, err)
	}
	if _, err := query(owner, netproto.RoleChannelQuery{Kind: authorization.ChannelCreate}); !errors.Is(err, authorization.ErrAuthorizationUnavailable) {
		t.Fatalf("pending enforcement opened queries: %v", err)
	}
	var auditCount int
	var actors, details string
	if err := db.DB().QueryRowContext(t.Context(), "SELECT COUNT(*),COALESCE(string_agg(actor_unique_id,','),''),COALESCE(string_agg(detail::text,','),'') FROM audit_log WHERE action LIKE 'roles.channel_%'").Scan(&auditCount, &actors, &details); err != nil {
		t.Fatal(err)
	}
	if auditCount != 6 || actors != strings.TrimSuffix(strings.Repeat(owner.UniqueID()+",", 6), ",") {
		t.Fatalf("channel audit count/actor: %d %q", auditCount, actors)
	}
	if strings.Contains(details, "channel-password-canary") || strings.Contains(details, passwordHash) {
		t.Fatal("audit exposed channel credential")
	}
}
