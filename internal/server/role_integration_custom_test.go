//go:build integration

package server

import (
	"context"
	"errors"
	"strings"
	"testing"

	"go.uber.org/zap"
	"noxa/internal/auth"
	"noxa/internal/authorization"
	"noxa/internal/config"
	"noxa/internal/netproto"
)

func TestIntegrationCustomMetadataAuthorityAndPersistence(t *testing.T) {
	db := integrationManagementStore(t)
	a := auth.New(db, zap.NewNop())
	create := func(name string) auth.IntegrationPrincipal {
		t.Helper()
		uid, err := a.RegisterUser(t.Context(), name, "custom-password")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.DB().ExecContext(t.Context(), "UPDATE users SET integration_enabled=TRUE WHERE unique_id=$1", uid); err != nil {
			t.Fatal(err)
		}
		p, err := a.AuthenticateIntegration(t.Context(), uid, "custom-password", "127.0.0.1")
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	owner, member := create("custom-owner"), create("custom-member")
	backend := serverRoleFixture()
	backend.policy.OwnerID, backend.policy.Roles[0].Permissions = owner.UserID(), nil
	authority, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	s := New(&config.Config{}, zap.NewNop(), &Deps{Auth: a, Authority: authority, CustomMetadata: db, Groups: db})
	const subject = "retained-orphan-uid"
	value, empty := "private annotation | with\nlines", ""
	change := netproto.CustomMetadataChange{UniqueID: subject, Key: "a-private-key", Value: &value}
	read := func(p auth.IntegrationPrincipal, after string, limit int) (netproto.CustomMetadataPage, error) {
		var page netproto.CustomMetadataPage
		err := s.WithIntegrationCustomMetadata(t.Context(), p, netproto.CustomMetadataQuery{UniqueID: subject, AfterKey: after, Limit: limit}, func(_ context.Context, result netproto.CustomMetadataPage) error { page = result; return nil })
		return page, err
	}
	if _, err := s.ChangeIntegrationCustomMetadata(t.Context(), member, change); !errors.Is(err, authorization.ErrRoleForbidden) {
		t.Fatalf("ordinary member write: %v", err)
	}
	if _, err := read(member, "", 1); !errors.Is(err, authorization.ErrRoleForbidden) {
		t.Fatalf("ordinary member read: %v", err)
	}
	if _, err := s.ChangeIntegrationCustomMetadata(t.Context(), auth.IntegrationPrincipal{}, change); !errors.Is(err, auth.ErrIntegrationDenied) {
		t.Fatalf("forged actor: %v", err)
	}
	for _, key := range []string{"a-private-key", "b", "c"} {
		change.Key = key
		result, err := s.ChangeIntegrationCustomMetadata(t.Context(), owner, change)
		if err != nil || result.UniqueID != subject || result.Key != key || result.Deleted {
			t.Fatalf("set: %+v %v", result, err)
		}
	}
	page, err := read(owner, "", 1)
	if err != nil || len(page.Entries) != 1 || page.Entries[0].Value != value || page.NextAfterKey != "a-private-key" {
		t.Fatalf("first page: %+v %v", page, err)
	}
	page, err = read(owner, page.NextAfterKey, 1)
	if err != nil || len(page.Entries) != 1 || page.Entries[0].Key != "b" || page.NextAfterKey != "b" {
		t.Fatalf("second page: %+v %v", page, err)
	}
	grant := func(allowed bool) {
		t.Helper()
		backend.mu.Lock()
		backend.policy.Roles[0].Permissions = nil
		if allowed {
			backend.policy.Roles[0].Permissions = []authorization.Capability{authorization.ManageServer}
		}
		backend.policy.Revision++
		backend.mu.Unlock()
		if err := authority.Reload(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	grant(true)
	change.Key, change.Value = "b", &empty
	if _, err := s.ChangeIntegrationCustomMetadata(t.Context(), member, change); err != nil {
		t.Fatal(err)
	}
	page, err = read(member, "a-private-key", 1)
	if err != nil || page.Entries[0].Value != "" {
		t.Fatalf("empty value was deleted: %+v %v", page, err)
	}
	change.Value, change.Delete = nil, true
	for range 2 {
		if result, err := s.ChangeIntegrationCustomMetadata(t.Context(), member, change); err != nil || !result.Deleted {
			t.Fatalf("idempotent exact delete: %+v %v", result, err)
		}
	}
	page, err = read(member, "", 0)
	if err != nil || len(page.Entries) != 2 || page.Entries[0].Key != "a-private-key" || page.Entries[1].Key != "c" || page.NextAfterKey != "" {
		t.Fatalf("wrong delete scope: %+v %v", page, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	change.Key = "c"
	if _, err := s.ChangeIntegrationCustomMetadata(ctx, owner, change); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled change: %v", err)
	}
	entries, err := db.AuditList(t.Context(), 0, 20)
	if err != nil || len(entries) != 6 {
		t.Fatalf("audit count: %d %v", len(entries), err)
	}
	for _, entry := range entries {
		if (entry.Actor != owner.UniqueID() && entry.Actor != member.UniqueID()) || entry.Target != subject || strings.Contains(entry.Detail, value) || strings.Contains(entry.Detail, "a-private-key") || entry.ChannelIDs == nil {
			t.Fatalf("noncanonical/value-bearing audit: %+v", entry)
		}
	}
	grant(false)
	if _, err := read(member, "", 0); !errors.Is(err, authorization.ErrRoleForbidden) {
		t.Fatalf("revoked read: %v", err)
	}
	if _, err := s.ChangeIntegrationCustomMetadata(t.Context(), member, change); !errors.Is(err, authorization.ErrRoleForbidden) {
		t.Fatalf("revoked write: %v", err)
	}
	if _, err := db.DB().ExecContext(t.Context(), "UPDATE users SET integration_enabled=FALSE WHERE id=$1", owner.UserID()); err != nil {
		t.Fatal(err)
	}
	if _, err := read(owner, "", 0); !errors.Is(err, auth.ErrIntegrationDenied) {
		t.Fatalf("disabled read: %v", err)
	}
	if _, err := s.ChangeIntegrationCustomMetadata(t.Context(), owner, change); !errors.Is(err, auth.ErrIntegrationDenied) {
		t.Fatalf("disabled write: %v", err)
	}
	rows, err := db.CustomInfo(t.Context(), subject)
	if err != nil || len(rows) != 2 {
		t.Fatalf("denied/canceled delete changed data: %v %v", rows, err)
	}
}
