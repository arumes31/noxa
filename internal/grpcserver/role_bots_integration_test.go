//go:build integration

package grpcserver

import (
	"context"
	"slices"
	"strings"
	"testing"

	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"noxa/internal/auth"
	"noxa/internal/authorization"
	"noxa/internal/config"
	"noxa/internal/query"
	"noxa/internal/server"
	noxav1 "noxa/v1"
)

type nativeBotGRPCBackend struct {
	query.RoleIntegrationBackend
	query.RoleBotBackend
}

// Scopes narrow an integration below its roles, and bulk delete follows the
// single-delete rules: own messages always, others' only with ManageMessages.
func TestRoleGRPCIntegrationScopesAndBulkDelete(t *testing.T) {
	db := grpcRoleTestStore(t)
	a := auth.New(db, zap.NewNop())
	const password = "grpc-bot-password"
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
	owner, bot := create("bot-owner"), create("bot-member")
	if _, err := db.PrepareRolePolicy(t.Context(), owner.UserID()); err != nil {
		t.Fatal(err)
	}
	authority, err := authorization.NewAuthority(t.Context(), db, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	native := server.New(&config.Config{}, zap.NewNop(), &server.Deps{Auth: a, Authority: authority, Roles: db, Chat: db, Groups: db})
	c := noxav1.NewControlClient(dialGRPC(t, startGRPC(t, &nativeBotGRPCBackend{RoleIntegrationBackend: native, RoleBotBackend: native}, nil)))
	ownerCtx := func() context.Context { return roleAuthCtx(t, owner.UniqueID(), password) }
	botCtx := func() context.Context { return roleAuthCtx(t, bot.UniqueID(), password) }

	store := func(author string) int64 {
		t.Helper()
		id, _, err := db.StoreChatMessage(t.Context(), 0, author, author, "c2VhbGVk", 1, 0, "")
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	bulk := func(ctx context.Context, ids ...int64) (*noxav1.BulkDeleteMessagesResponse, error) {
		return c.BulkDeleteMessages(ctx, &noxav1.BulkDeleteMessagesRequest{ChannelId: 0, MessageIds: ids})
	}
	outsideScopes := func(err error) bool {
		return status.Code(err) == codes.PermissionDenied && strings.Contains(status.Convert(err).Message(), "scopes")
	}
	roleDenied := func(err error) bool {
		return status.Code(err) == codes.PermissionDenied && !strings.Contains(status.Convert(err).Message(), "scopes")
	}
	// Without a ceiling an integration keeps its legacy access.
	own := store(owner.UniqueID())
	if res, err := bulk(ownerCtx(), own); err != nil || !slices.Equal(res.GetDeletedIds(), []int64{own}) {
		t.Fatalf("legacy integration lost access: %v %v", res, err)
	}
	if _, err := c.SetIntegrationScopes(botCtx(), &noxav1.SetIntegrationScopesRequest{UniqueId: bot.UniqueID(), Full: true}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("member changed scopes without ManageServer: %v", err)
	}
	other, other2 := store("someone-else"), store("someone-else")
	if _, err := c.SetIntegrationScopes(ownerCtx(), &noxav1.SetIntegrationScopesRequest{UniqueId: bot.UniqueID(), Scopes: []string{auth.ScopeServerRead}}); err != nil {
		t.Fatal(err)
	}
	if _, err := bulk(botCtx(), other); !outsideScopes(err) {
		t.Fatalf("scope ceiling not applied: %v", err)
	}
	set, err := c.SetIntegrationScopes(ownerCtx(), &noxav1.SetIntegrationScopesRequest{UniqueId: bot.UniqueID(), Scopes: []string{auth.ScopeMessagesManage}})
	if err != nil || set.GetFull() || !slices.Equal(set.GetScopes(), []string{auth.ScopeMessagesManage}) {
		t.Fatalf("set scopes: %v %v", set, err)
	}
	got, err := c.GetIntegrationScopes(ownerCtx(), &noxav1.GetIntegrationScopesRequest{UniqueId: bot.UniqueID()})
	if err != nil || got.GetFull() || len(got.GetScopes()) != 1 {
		t.Fatalf("get scopes: %v %v", got, err)
	}
	if _, err := c.SetIntegrationScopes(ownerCtx(), &noxav1.SetIntegrationScopesRequest{UniqueId: bot.UniqueID(), Scopes: []string{"bogus"}}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("unknown scope: %v", err)
	}
	// The scope admits the call, but the roles still decide: no grant.
	if _, err := bulk(botCtx(), other); !roleDenied(err) {
		t.Fatalf("scope granted a permission the roles do not: %v", err)
	}
	if msg, _ := db.GetChatMessage(t.Context(), other); msg == nil || msg.DeletedAt != nil {
		t.Fatal("a refused bulk delete changed the message")
	}
	result, err := bulk(ownerCtx(), other2, own, 987654321, other)
	if err != nil || !slices.Equal(result.GetDeletedIds(), []int64{other2, other}) || !slices.Equal(result.GetNotFoundIds(), []int64{own, 987654321}) {
		t.Fatalf("owner bulk delete: %v %v", result, err)
	}
	if _, err := bulk(ownerCtx(), 1, 1); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("duplicate ids accepted: %v", err)
	}
	for _, action := range []string{"integration_scopes", "message_bulk_delete"} {
		var audits int
		if err := db.DB().QueryRowContext(t.Context(), "SELECT COUNT(*) FROM audit_log WHERE action = $1", action).Scan(&audits); err != nil {
			t.Fatal(err)
		}
		if audits == 0 {
			t.Fatalf("no %s audit row", action)
		}
	}

	// An empty ceiling allows nothing, even what the roles allow.
	if _, err := c.SetIntegrationScopes(ownerCtx(), &noxav1.SetIntegrationScopesRequest{UniqueId: bot.UniqueID()}); err != nil {
		t.Fatal(err)
	}
	if _, err := bulk(botCtx(), store(bot.UniqueID())); !outsideScopes(err) {
		t.Fatalf("empty ceiling allowed bulk delete: %v", err)
	}
	// A scoped caller cannot widen scopes, even with ManageServer.
	if _, err := c.SetIntegrationScopes(ownerCtx(), &noxav1.SetIntegrationScopesRequest{UniqueId: owner.UniqueID(), Scopes: []string{auth.ScopeServerManage}}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.SetIntegrationScopes(ownerCtx(), &noxav1.SetIntegrationScopesRequest{UniqueId: owner.UniqueID(), Full: true}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("scoped owner cleared its own ceiling: %v", err)
	}
	if _, err := c.SetIntegrationScopes(ownerCtx(), &noxav1.SetIntegrationScopesRequest{UniqueId: bot.UniqueID(), Scopes: []string{auth.ScopeMembersBan}}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("scoped owner handed out a scope it lacks: %v", err)
	}
	if _, err := c.SetIntegrationScopes(ownerCtx(), &noxav1.SetIntegrationScopesRequest{UniqueId: bot.UniqueID(), Scopes: []string{auth.ScopeServerManage}}); err != nil {
		t.Fatalf("scoped owner could not hand out its own scope: %v", err)
	}
	if _, err := bulk(ownerCtx(), store(owner.UniqueID())); !outsideScopes(err) {
		t.Fatalf("owner ceiling not applied: %v", err)
	}
}
