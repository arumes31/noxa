//go:build integration

package server

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"go.uber.org/zap"
	"noxa/internal/auth"
	"noxa/internal/authorization"
	"noxa/internal/config"
	"noxa/internal/netproto"
	"noxa/internal/store"
)

func TestIntegrationConfigPersistsAtomicallyWithCurrentAuthority(t *testing.T) {
	db := integrationManagementStore(t)
	a := auth.New(db, zap.NewNop())
	create := func(name string) auth.IntegrationPrincipal {
		t.Helper()
		uid, err := a.RegisterUser(t.Context(), name, "config-password")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.DB().ExecContext(t.Context(), "UPDATE users SET integration_enabled=TRUE WHERE unique_id=$1", uid); err != nil {
			t.Fatal(err)
		}
		p, err := a.AuthenticateIntegration(t.Context(), uid, "config-password", "127.0.0.1")
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	member, owner := create("config-member"), create("config-owner")
	backend := serverRoleFixture()
	backend.policy.OwnerID = owner.UserID()
	backend.policy.Roles[0].Permissions = nil
	authority, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	srv := New(&config.Config{}, zap.NewNop(), &Deps{Auth: a, Authority: authority, Chat: db, Groups: db})
	request := netproto.ServerConfig{MaxClients: 100, ClientTimeoutSeconds: 120, OpusBitrate: 64000, OpusFEC: true, OpusDTX: true}
	if _, err := srv.SetIntegrationServerConfig(t.Context(), member, request); !errors.Is(err, authorization.ErrRoleForbidden) {
		t.Fatalf("ordinary member changed settings: %v", err)
	}
	if _, err := srv.SetIntegrationServerConfig(t.Context(), owner, netproto.ServerConfig{}); !errors.Is(err, authorization.ErrRoleInvalid) {
		t.Fatal(err)
	}
	got, err := srv.SetIntegrationServerConfig(t.Context(), owner, request)
	if err != nil || got != request || srv.serverConfig() != request {
		t.Fatalf("owner save: %+v %v", got, err)
	}
	assertPersisted := func(want netproto.ServerConfig) {
		t.Helper()
		var cfg config.Config
		if err := LoadPersistedServerConfig(t.Context(), &cfg, db); err != nil {
			t.Fatal(err)
		}
		loaded := New(&cfg, zap.NewNop(), nil).serverConfig()
		if loaded != want || srv.serverConfig() != want {
			t.Fatalf("restart/runtime diverged: persisted=%+v runtime=%+v want=%+v", loaded, srv.serverConfig(), want)
		}
	}
	assertPersisted(request)
	backend.mu.Lock()
	backend.policy.Roles[0].Permissions = []authorization.Capability{authorization.ManageServer}
	backend.policy.Revision++
	backend.mu.Unlock()
	if err := authority.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	request.MaxClients, request.OpusFEC, request.OpusDTX = 0, false, false
	got, err = srv.SetIntegrationServerConfig(t.Context(), member, request)
	if err != nil || got != request {
		t.Fatalf("delegated full replacement: %+v %v", got, err)
	}
	assertPersisted(request)
	entries, err := db.AuditList(t.Context(), 0, 10)
	if err != nil || len(entries) != 2 {
		t.Fatalf("configuration audits: %+v %v", entries, err)
	}
	var detail store.AuditDetail
	if err := json.Unmarshal([]byte(entries[0].Detail), &detail); err != nil {
		t.Fatal(err)
	}
	if entries[0].Actor != member.UniqueID() || entries[0].Action != "server_config_set" || entries[0].Target != "server" || entries[0].ChannelIDs == nil || detail.Text != "max_clients=0 timeout=120 opus=64000" {
		t.Fatalf("canonical audit: %+v", entries[0])
	}
	// Fail in the middle of the sorted six-row transaction, after earlier keys
	// were updated. Both durable and runtime values must remain the last save.
	if _, err := db.DB().ExecContext(t.Context(), `CREATE FUNCTION reject_stereo() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.key='default_opus_stereo' AND NEW.value='true' THEN RAISE EXCEPTION 'forced setting failure'; END IF; RETURN NEW; END $$`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB().ExecContext(t.Context(), `CREATE TRIGGER reject_stereo BEFORE INSERT OR UPDATE ON server_settings FOR EACH ROW EXECUTE FUNCTION reject_stereo()`); err != nil {
		t.Fatal(err)
	}
	failed := request
	failed.MaxClients, failed.OpusBitrate, failed.OpusStereo = 500, 128000, true
	if _, err := srv.SetIntegrationServerConfig(t.Context(), member, failed); err == nil {
		t.Fatal("failed transaction reported saved")
	}
	assertPersisted(request)
	entries, err = db.AuditList(t.Context(), 0, 10)
	if err != nil || len(entries) != 2 {
		t.Fatal("failed transaction claimed a successful audit")
	}
	backend.mu.Lock()
	backend.policy.Roles[0].Permissions = nil
	backend.policy.Revision++
	backend.mu.Unlock()
	if err := authority.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.SetIntegrationServerConfig(t.Context(), member, request); !errors.Is(err, authorization.ErrRoleForbidden) {
		t.Fatalf("revoked grant: %v", err)
	}
	if _, err := db.DB().ExecContext(t.Context(), "UPDATE users SET integration_enabled=FALSE WHERE id=$1", owner.UserID()); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.SetIntegrationServerConfig(t.Context(), owner, request); !errors.Is(err, auth.ErrIntegrationDenied) {
		t.Fatalf("disabled owner: %v", err)
	}
}
