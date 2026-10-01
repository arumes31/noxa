//go:build integration

package server

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
	"noxa/internal/auth"
	"noxa/internal/authorization"
	"noxa/internal/config"
	"noxa/internal/netproto"
)

func TestIntegrationAuditPreservesScopesAndDeliveryLease(t *testing.T) {
	db := integrationManagementStore(t)
	a := auth.New(db, zap.NewNop())
	create := func(name string) auth.IntegrationPrincipal {
		t.Helper()
		uid, err := a.RegisterUser(t.Context(), name, "audit-password")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.DB().ExecContext(t.Context(), "UPDATE users SET integration_enabled=TRUE WHERE unique_id=$1", uid); err != nil {
			t.Fatal(err)
		}
		p, err := a.AuthenticateIntegration(t.Context(), uid, "audit-password", "127.0.0.1")
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	reader, owner := create("audit-reader"), create("audit-owner")
	backend := serverRoleFixture()
	backend.policy.OwnerID = owner.UserID()
	backend.policy.Roles[0].Permissions = []authorization.Capability{authorization.ViewAuditLog, authorization.ViewChannel}
	backend.policy.Channels = []authorization.ChannelPolicy{{ChannelID: 1}, {ChannelID: 2, Overrides: []authorization.RoleOverride{{RoleID: 10, Capability: authorization.ViewChannel, Effect: authorization.Deny}}}}
	authority, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	srv := New(&config.Config{}, zap.NewNop(), &Deps{Auth: a, Authority: authority, Groups: db})
	for _, row := range []struct {
		target   string
		channels []int64
	}{
		{"server", []int64{}}, {"visible", []int64{1}}, {"hidden", []int64{2}}, {"mixed", []int64{1, 2}}, {"deleted", []int64{99}}, {"negative", []int64{-1}}, {"legacy", nil},
	} {
		db.AuditScoped(t.Context(), "private-actor", "private-action", row.target, `{"version":1,"channel_ids":[],"text":"private detail"}`, row.channels)
	}
	read := func(p auth.IntegrationPrincipal, request netproto.AuditLog) (netproto.AuditLogResponse, error) {
		var result netproto.AuditLogResponse
		err := srv.WithIntegrationAudit(t.Context(), p, request, func(_ context.Context, page netproto.AuditLogResponse) error { result = page; return nil })
		return result, err
	}
	page, err := read(reader, netproto.AuditLog{})
	if err != nil || len(page.Entries) != 7 || len(page.Capabilities) == 0 {
		t.Fatalf("audit page: %+v %v", page, err)
	}
	for _, entry := range page.Entries {
		if entry.ID <= 0 || entry.CreatedAt <= 0 {
			t.Fatalf("pagination metadata missing: %+v", entry)
		}
		if entry.Target == "server" || entry.Target == "visible" {
			if !entry.Structured || entry.Restricted || entry.Detail == "" {
				t.Fatalf("visible row lost detail: %+v", entry)
			}
		} else if !entry.Restricted || entry.Actor != "" || entry.Action != "" || entry.Target != "" || entry.Detail != "" || entry.Structured {
			t.Fatalf("ordinary member or forged JSON bypassed scope: %+v", entry)
		}
	}
	ownerPage, err := read(owner, netproto.AuditLog{})
	if err != nil || len(ownerPage.Entries) != 7 {
		t.Fatalf("owner page: %+v %v", ownerPage, err)
	}
	for _, entry := range ownerPage.Entries {
		if entry.Restricted || entry.Actor == "" || entry.Structured != (entry.Target != "legacy") {
			t.Fatalf("owner provenance: %+v", entry)
		}
	}
	var before int64
	var seen []int64
	for {
		page, err := read(reader, netproto.AuditLog{BeforeID: before, Limit: 2})
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range page.Entries {
			if before != 0 && entry.ID >= before {
				t.Fatal("cursor repeated or reordered an entry")
			}
			before = entry.ID
			seen = append(seen, entry.ID)
		}
		if len(page.Entries) < 2 {
			break
		}
	}
	if len(seen) != 7 {
		t.Fatalf("redacted pagination lost rows: %v", seen)
	}
	for _, request := range []netproto.AuditLog{{BeforeID: -1}, {Limit: -1}, {Limit: 201}} {
		if _, err := read(reader, request); !errors.Is(err, authorization.ErrRoleInvalid) {
			t.Fatalf("invalid request: %v", err)
		}
	}
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	var once sync.Once
	defer once.Do(func() { close(release) })
	go func() {
		done <- srv.WithIntegrationAudit(t.Context(), reader, netproto.AuditLog{}, func(context.Context, netproto.AuditLogResponse) error {
			if srv.roleMetadataMu.TryLock() {
				srv.roleMetadataMu.Unlock()
				return errors.New("metadata lease ended before delivery")
			}
			close(entered)
			<-release
			return nil
		})
	}()
	select {
	case <-entered:
	case err := <-done:
		t.Fatal(err)
	case <-time.After(3 * time.Second):
		t.Fatal("audit callback missing")
	}
	backend.mu.Lock()
	backend.policy.Roles[0].Permissions = []authorization.Capability{authorization.ViewAuditLog}
	backend.policy.Revision++
	backend.mu.Unlock()
	reloaded := make(chan error, 1)
	go func() { reloaded <- authority.Reload(t.Context()) }()
	select {
	case err := <-reloaded:
		t.Fatalf("revocation passed delivery lease: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	once.Do(func() { close(release) })
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := <-reloaded; err != nil {
		t.Fatal(err)
	}
	page, err = read(reader, netproto.AuditLog{})
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range page.Entries {
		if entry.Target != "server" && !entry.Restricted {
			t.Fatalf("revoked scope remained visible: %+v", entry)
		}
	}
	backend.mu.Lock()
	backend.policy.Roles[0].Permissions = nil
	backend.policy.Revision++
	backend.mu.Unlock()
	if err := authority.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := read(reader, netproto.AuditLog{}); !errors.Is(err, authorization.ErrRoleForbidden) {
		t.Fatalf("ordinary member retained audit grant: %v", err)
	}
	if _, err := db.DB().ExecContext(t.Context(), "UPDATE users SET integration_enabled=FALSE WHERE id=$1", reader.UserID()); err != nil {
		t.Fatal(err)
	}
	if _, err := read(reader, netproto.AuditLog{}); !errors.Is(err, auth.ErrIntegrationDenied) {
		t.Fatalf("disabled integration read audit: %v", err)
	}
}
