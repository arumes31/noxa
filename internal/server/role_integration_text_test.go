//go:build integration

package server

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"go.uber.org/zap"
	"noxa/internal/auth"
	"noxa/internal/authorization"
	"noxa/internal/chatcrypto"
	"noxa/internal/config"
	"noxa/internal/netproto"
	"noxa/internal/rules"
	"noxa/internal/state"
)

func TestIntegrationServerTextAuthorityPersistenceAndEncryption(t *testing.T) {
	db := integrationManagementStore(t)
	a := auth.New(db, zap.NewNop())
	create := func(name string) auth.IntegrationPrincipal {
		t.Helper()
		uid, err := a.RegisterUser(t.Context(), name, "text-password")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.DB().ExecContext(t.Context(), "UPDATE users SET integration_enabled=TRUE WHERE unique_id=$1", uid); err != nil {
			t.Fatal(err)
		}
		p, err := a.AuthenticateIntegration(t.Context(), uid, "text-password", "127.0.0.1")
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	member, owner := create("text-member"), create("text-owner")
	backend := serverRoleFixture()
	backend.policy.OwnerID = owner.UserID()
	backend.policy.Roles[0].Permissions = nil
	authority, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	kek, err := chatcrypto.LoadKEKRing(filepath.Join(t.TempDir(), "kek.ring"), "", true)
	if err != nil {
		t.Fatal(err)
	}
	r := rules.New(db, db.DB())
	deps := &Deps{Auth: a, Authority: authority, Chat: db, Groups: db, ScopeKeys: db, ChatKEK: kek, Rules: r, State: state.New(zap.NewNop())}
	srv := New(&config.Config{ServerName: "Configured fallback"}, zap.NewNop(), deps)
	write := func(p auth.IntegrationPrincipal, key, value string) error {
		got, err := srv.SetIntegrationServerText(t.Context(), p, netproto.ServerTextSet{Key: key, Value: &value})
		if err == nil && (got.Key != key || got.ContentHash != fmt.Sprintf("%x", sha256.Sum256([]byte(value)))) {
			t.Fatalf("wrong committed acknowledgement: %+v", got)
		}
		return err
	}
	if err := write(member, "server_name", "Denied"); !errors.Is(err, authorization.ErrRoleForbidden) {
		t.Fatalf("ordinary member write: %v", err)
	}
	if err := write(auth.IntegrationPrincipal{}, "server_name", "Forged"); !errors.Is(err, auth.ErrIntegrationDenied) {
		t.Fatalf("forged identity: %v", err)
	}
	for _, key := range []string{"server_name", "motd", "announcement", "server_rules"} {
		value := "Private wording " + key
		if err := write(owner, key, value); err != nil {
			t.Fatal(err)
		}
		stored, generation, err := db.GetServerSetting(t.Context(), key)
		if err != nil {
			t.Fatal(err)
		}
		if sealedSetting(key) {
			if generation == 0 || stored == value || strings.Contains(stored, "Private wording") {
				t.Fatalf("plaintext persisted: %s", key)
			}
			fresh := New(&config.Config{}, zap.NewNop(), deps)
			if plain := fresh.serverSettingPlain(t.Context(), key); plain != value {
				t.Fatalf("restart decryption: %s %q", key, plain)
			}
		} else if stored != value || generation != 0 {
			t.Fatalf("wrong stored setting: %q %d", stored, generation)
		}
	}
	var info netproto.ServerInfoResponse
	readName := func() string {
		t.Helper()
		if err := srv.WithIntegrationServerInfo(t.Context(), owner, func(_ context.Context, got netproto.ServerInfoResponse) error { info = got; return nil }); err != nil {
			t.Fatal(err)
		}
		return info.Name
	}
	if got := readName(); got != "Private wording server_name" {
		t.Fatal(got)
	}
	if err := r.Accept(t.Context(), member.UserID(), rules.Hash("Private wording server_rules")); err != nil {
		t.Fatal(err)
	}
	backend.mu.Lock()
	backend.policy.Roles[0].Permissions = []authorization.Capability{authorization.ManageServer}
	backend.policy.Revision++
	backend.mu.Unlock()
	if err := authority.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := write(member, "server_rules", "Changed rules"); err != nil {
		t.Fatal(err)
	}
	if err := srv.WithIntegrationRules(t.Context(), member, func(_ context.Context, got netproto.RulesInspection) error {
		if got.Text != "Changed rules" || got.Hash != rules.Hash("Changed rules") || got.AcceptedClients != 0 {
			t.Fatalf("old acceptance counted: %+v", got)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"server_name", "motd", "announcement", "server_rules"} {
		if err := write(member, key, ""); err != nil {
			t.Fatal(err)
		}
		stored, generation, err := db.GetServerSetting(t.Context(), key)
		if err != nil || stored != "" || generation != 0 {
			t.Fatalf("clear %s: %q %d %v", key, stored, generation, err)
		}
	}
	if got := readName(); got != "Configured fallback" {
		t.Fatal(got)
	}
	entries, err := db.AuditList(t.Context(), 0, 20)
	if err != nil || len(entries) != 9 {
		t.Fatalf("audit count: %d %v", len(entries), err)
	}
	for _, row := range entries {
		if row.Action != "server_text_set" || (row.Actor != owner.UniqueID() && row.Actor != member.UniqueID()) || strings.Contains(row.Detail, "Private wording") || strings.Contains(row.Detail, "Changed rules") || row.ChannelIDs == nil {
			t.Fatalf("noncanonical/plaintext audit: %+v", row)
		}
	}
	backend.mu.Lock()
	backend.policy.Roles[0].Permissions = nil
	backend.policy.Revision++
	backend.mu.Unlock()
	if err := authority.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := write(member, "server_name", "Revoked"); !errors.Is(err, authorization.ErrRoleForbidden) {
		t.Fatalf("revoked write: %v", err)
	}
	if _, err := db.DB().ExecContext(t.Context(), "UPDATE users SET integration_enabled=FALSE WHERE id=$1", owner.UserID()); err != nil {
		t.Fatal(err)
	}
	if err := write(owner, "server_name", "Disabled"); !errors.Is(err, auth.ErrIntegrationDenied) {
		t.Fatalf("disabled write: %v", err)
	}
}
