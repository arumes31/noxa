//go:build integration

package grpcserver

import (
	"context"
	"testing"

	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"noxa/internal/auth"
	"noxa/internal/authorization"
	"noxa/internal/config"
	"noxa/internal/query"
	"noxa/internal/server"
	"noxa/internal/state"
	noxav1 "noxa/v1"
)

type nativeInspectionGRPCBackend struct {
	*nativeRoleGRPCBackend
	query.RoleInspectionBackend
}

func TestRoleGRPCInspectionUsesNativeScopeAndRevision(t *testing.T) {
	db := grpcRoleTestStore(t)
	a := auth.New(db, zap.NewNop())
	const password = "grpc-inspection-password"
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
	owner, member := create("inspection-owner"), create("inspection-member")
	if _, err := db.DB().ExecContext(t.Context(), "INSERT INTO channels (id,name,channel_type,parent_id) VALUES (1,'Hidden parent',2,NULL),(2,'Managed child',2,1),(3,'Hidden other',2,NULL)"); err != nil {
		t.Fatal(err)
	}
	policy, err := db.PrepareRolePolicy(t.Context(), owner.UserID())
	if err != nil {
		t.Fatal(err)
	}
	authority, err := authorization.NewAuthority(t.Context(), db, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	change := authorization.RoleChange{Kind: authorization.ChannelAccessSet, ExpectedRevision: policy.Revision, Channel: authorization.ChannelPolicy{ChannelID: 2, ParentID: 1}}
	for _, cap := range []authorization.Capability{authorization.ViewChannel, authorization.ManageChannelAccess, authorization.ManageChannels} {
		change.Channel.Overrides = append(change.Channel.Overrides, authorization.RoleOverride{RoleID: policy.EveryoneID, Capability: cap, Effect: authorization.Allow})
	}
	policy, err = authority.ChangeRolePolicy(t.Context(), owner.UserID(), change)
	if err != nil {
		t.Fatal(err)
	}
	sm := state.New(zap.NewNop())
	sm.AddChannel(&state.Channel{ChannelID: 1, Name: "Hidden parent", ChannelType: 2})
	sm.AddChannel(&state.Channel{ChannelID: 2, ParentID: 1, Name: "Managed child", Topic: "Visible topic", ChannelType: 2, MaxClients: 25, OpusBitrate: 64000})
	sm.AddChannel(&state.Channel{ChannelID: 3, Name: "Hidden other", ChannelType: 2})
	native := server.New(&config.Config{}, zap.NewNop(), &server.Deps{Auth: a, Authority: authority, Roles: db, State: sm})
	b := &nativeInspectionGRPCBackend{nativeRoleGRPCBackend: &nativeRoleGRPCBackend{RoleIntegrationBackend: native, RoleManagementBackend: native, RoleChannelBackend: native}, RoleInspectionBackend: native}
	client := noxav1.NewControlClient(dialGRPC(t, startGRPC(t, b, nil)))
	ctx := func() context.Context { return roleAuthCtx(t, member.UniqueID(), password) }
	ownerCtx := func() context.Context { return roleAuthCtx(t, owner.UniqueID(), password) }
	if _, err := client.GetRoleState(ctx(), &noxav1.GetRoleStateRequest{}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("ordinary member inspected global roles: %v", err)
	}
	roles, err := client.GetRoleState(ctx(), &noxav1.GetRoleStateRequest{ChannelId: 2})
	if err != nil || roles.GetActorId() != member.UserID() || roles.GetPolicy().GetRevision() != policy.Revision || len(roles.GetPolicy().GetChannels()) != 1 {
		t.Fatalf("scoped roles: %v %v", roles, err)
	}
	if roles.Policy.Channels[0].ChannelId != 2 || roles.Policy.Channels[0].ParentId != 0 || roles.ParentAccessAvailable || len(roles.ParentOverrides) != 0 || len(roles.EffectiveOverrides) != 3 {
		t.Fatalf("hidden parent projection: %v", roles)
	}
	for _, id := range []int64{1, 3, 9999} {
		if _, err := client.GetRoleState(ctx(), &noxav1.GetRoleStateRequest{ChannelId: id}); status.Code(err) != codes.PermissionDenied {
			t.Fatalf("hidden/missing roles %d: %v", id, err)
		}
		if _, err := client.GetChannelOptions(ctx(), &noxav1.GetChannelOptionsRequest{Kind: "channel_edit", ChannelId: id}); status.Code(err) != codes.PermissionDenied {
			t.Fatalf("hidden/missing options %d: %v", id, err)
		}
	}
	options, err := client.GetChannelOptions(ctx(), &noxav1.GetChannelOptionsRequest{Kind: "channel_edit", ChannelId: 2})
	if err != nil || options.GetRevision() != policy.Revision || options.GetSettings().GetTopic() != "Visible topic" || options.GetSettings().GetMaxClients() != 25 || !options.GetCanManageAccess() {
		t.Fatalf("channel options: %v %v", options, err)
	}
	page, err := client.ListRoleMembers(ctx(), &noxav1.ListRoleMembersRequest{ChannelId: 2, ExpectedRevision: policy.Revision, Search: "inspection-"})
	if err != nil || page.GetRevision() != policy.Revision || len(page.GetEntries()) != 2 || page.GetMore() {
		t.Fatalf("offline roster: %v %v", page, err)
	}
	for _, entry := range page.Entries {
		if entry.Manageable {
			t.Fatalf("owner or equal-ranked actor manageable: %v", entry)
		}
	}
	page, err = client.ListRoleMembers(ownerCtx(), &noxav1.ListRoleMembersRequest{ExpectedRevision: policy.Revision, AfterId: owner.UserID()})
	if err != nil || len(page.GetEntries()) != 1 || page.Entries[0].UserId != member.UserID() || !page.Entries[0].Manageable {
		t.Fatalf("owner ascending roster cursor: %v %v", page, err)
	}
	check := &noxav1.CheckAccessRequest{UserId: owner.UserID(), ChannelId: 2, ExpectedRevision: policy.Revision, Capability: string(authorization.ManageChannels)}
	decision, err := client.CheckAccess(ctx(), check)
	if err != nil || !decision.GetDecision().GetAllowed() || decision.GetCanManageMember() {
		t.Fatalf("subject access confused with actor hierarchy: %v %v", decision, err)
	}
	check.UserId, check.Capability = 0, string(authorization.ViewChannel)
	decision, err = client.CheckAccess(ctx(), check)
	if err != nil || !decision.GetDecision().GetAllowed() {
		t.Fatalf("guest explanation: %v %v", decision, err)
	}
	check.ExpectedRevision--
	if _, err := client.CheckAccess(ctx(), check); status.Code(err) != codes.Aborted {
		t.Fatalf("stale explanation: %v", err)
	}
	if _, err := client.ListRoleMembers(ctx(), &noxav1.ListRoleMembersRequest{ChannelId: 2, ExpectedRevision: policy.Revision - 1}); status.Code(err) != codes.Aborted {
		t.Fatalf("stale roster: %v", err)
	}
	change.ExpectedRevision = policy.Revision
	for i := range change.Channel.Overrides {
		change.Channel.Overrides[i].Effect = authorization.Deny
	}
	if _, err := authority.ChangeRolePolicy(t.Context(), owner.UserID(), change); err != nil {
		t.Fatal(err)
	}
	check.ExpectedRevision = policy.Revision + 1
	reads := map[string]func() error{
		"roles": func() error {
			_, err := client.GetRoleState(ctx(), &noxav1.GetRoleStateRequest{ChannelId: 2})
			return err
		},
		"members": func() error {
			_, err := client.ListRoleMembers(ctx(), &noxav1.ListRoleMembersRequest{ChannelId: 2, ExpectedRevision: policy.Revision + 1})
			return err
		},
		"access": func() error { _, err := client.CheckAccess(ctx(), check); return err },
		"options": func() error {
			_, err := client.GetChannelOptions(ctx(), &noxav1.GetChannelOptionsRequest{Kind: "channel_edit", ChannelId: 2})
			return err
		},
	}
	for name, read := range reads {
		if err := read(); status.Code(err) != codes.PermissionDenied {
			t.Fatalf("same connection retained %s access: %v", name, err)
		}
	}
	if _, err := db.DB().ExecContext(t.Context(), "UPDATE users SET integration_enabled=FALSE WHERE id=$1", member.UserID()); err != nil {
		t.Fatal(err)
	}
	for name, read := range reads {
		if err := read(); status.Code(err) != codes.Unauthenticated {
			t.Fatalf("disabled identity inspected %s: %v", name, err)
		}
	}
}
