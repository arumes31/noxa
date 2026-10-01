//go:build integration

package server

import (
	"context"
	"errors"
	"testing"

	"go.uber.org/zap"
	"noxa/internal/auth"
	"noxa/internal/authorization"
	"noxa/internal/config"
	"noxa/internal/netproto"
)

func TestIntegrationFiltersUseSeparateCapabilityAndCurrentAdmission(t *testing.T) {
	db := integrationManagementStore(t)
	a := auth.New(db, zap.NewNop())
	create := func(name string) auth.IntegrationPrincipal {
		t.Helper()
		uid, err := a.RegisterUser(t.Context(), name, "filter-password")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.DB().ExecContext(t.Context(), "UPDATE users SET integration_enabled=TRUE WHERE unique_id=$1", uid); err != nil {
			t.Fatal(err)
		}
		p, err := a.AuthenticateIntegration(t.Context(), uid, "filter-password", "127.0.0.1")
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	member, owner := create("filter-member"), create("filter-owner")
	backend := serverRoleFixture()
	backend.policy.OwnerID = owner.UserID()
	backend.policy.Roles[0].Permissions = []authorization.Capability{authorization.ManageServer}
	authority, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	srv := New(&config.Config{ChatWordFilter: "default", ChatLinkBlacklist: "blocked.example", ChatLinkWhitelist: "allowed.example"}, zap.NewNop(), &Deps{Auth: a, Authority: authority, Chat: db, Groups: db})
	read := func(p auth.IntegrationPrincipal) (netproto.ChatFilterResponse, error) {
		var result netproto.ChatFilterResponse
		err := srv.WithIntegrationChatFilters(t.Context(), p, func(_ context.Context, got netproto.ChatFilterResponse) error {
			if srv.roleMetadataMu.TryLock() {
				srv.roleMetadataMu.Unlock()
				return errors.New("metadata released before delivery")
			}
			result = got
			return nil
		})
		return result, err
	}
	words := "  First, , Second  "
	patch := netproto.ChatFilterSet{WordFilter: &words}
	if _, err := read(member); !errors.Is(err, authorization.ErrRoleForbidden) {
		t.Fatalf("ManageServer/ordinary member read filters: %v", err)
	}
	if _, err := srv.SetIntegrationChatFilters(t.Context(), member, patch); !errors.Is(err, authorization.ErrRoleForbidden) {
		t.Fatalf("ManageServer/ordinary member saved filters: %v", err)
	}
	got, err := read(owner)
	if err != nil || !got.FromConfig || got.WordFilter != "default" {
		t.Fatalf("config defaults: %+v %v", got, err)
	}
	if _, err := srv.SetIntegrationChatFilters(t.Context(), owner, netproto.ChatFilterSet{}); !errors.Is(err, authorization.ErrRoleInvalid) {
		t.Fatal(err)
	}
	backend.mu.Lock()
	backend.policy.Roles[0].Permissions = []authorization.Capability{authorization.ManageChatFilters}
	backend.policy.Revision++
	backend.mu.Unlock()
	if err := authority.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	got, err = srv.SetIntegrationChatFilters(t.Context(), member, patch)
	if err != nil || got.WordFilter != "First,Second" || got.LinkBlacklist != "blocked.example" || got.LinkWhitelist != "allowed.example" || got.FromConfig {
		t.Fatalf("partial edit: %+v %v", got, err)
	}
	if err := srv.moderateBody(t.Context(), "SECOND"); err == nil {
		t.Fatal("saved filter not enforced")
	}
	empty := ""
	got, err = srv.SetIntegrationChatFilters(t.Context(), member, netproto.ChatFilterSet{WordFilter: &empty})
	if err != nil || got.WordFilter != "" || got.LinkBlacklist != "blocked.example" || got.LinkWhitelist != "allowed.example" {
		t.Fatalf("clear changed omitted fields: %+v %v", got, err)
	}
	if err := srv.moderateBody(t.Context(), "SECOND"); err != nil {
		t.Fatal("clear left stale filter active")
	}
	fresh := New(&config.Config{}, zap.NewNop(), &Deps{Chat: db})
	persisted, err := fresh.readManagedChatFilters(t.Context())
	if err != nil || persisted != got {
		t.Fatalf("restart differed: %+v %v", persisted, err)
	}
	entries, err := db.AuditList(t.Context(), 0, 10)
	if err != nil || len(entries) != 2 || entries[0].Actor != member.UniqueID() || entries[0].Action != "chat_filter_set" || entries[0].ChannelIDs == nil {
		t.Fatalf("canonical audit: %+v %v", entries, err)
	}
	backend.mu.Lock()
	backend.policy.Roles[0].Permissions = nil
	backend.policy.Revision++
	backend.mu.Unlock()
	if err := authority.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := read(member); !errors.Is(err, authorization.ErrRoleForbidden) {
		t.Fatalf("revoked read: %v", err)
	}
	if _, err := srv.SetIntegrationChatFilters(t.Context(), member, patch); !errors.Is(err, authorization.ErrRoleForbidden) {
		t.Fatalf("revoked save: %v", err)
	}
	if _, err := db.DB().ExecContext(t.Context(), "UPDATE users SET integration_enabled=FALSE WHERE id=$1", owner.UserID()); err != nil {
		t.Fatal(err)
	}
	if _, err := read(owner); !errors.Is(err, auth.ErrIntegrationDenied) {
		t.Fatalf("disabled owner read: %v", err)
	}
	if _, err := srv.SetIntegrationChatFilters(t.Context(), owner, patch); !errors.Is(err, auth.ErrIntegrationDenied) {
		t.Fatalf("disabled owner save: %v", err)
	}
}
