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
	noxawebrtc "noxa/internal/webrtc"
)

func TestIntegrationMediaLimitsUseCurrentAuthorityAndPersist(t *testing.T) {
	db := integrationManagementStore(t)
	a := auth.New(db, zap.NewNop())
	create := func(name string) auth.IntegrationPrincipal {
		t.Helper()
		uid, err := a.RegisterUser(t.Context(), name, "media-password")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.DB().ExecContext(t.Context(), "UPDATE users SET integration_enabled=TRUE WHERE unique_id=$1", uid); err != nil {
			t.Fatal(err)
		}
		principal, err := a.AuthenticateIntegration(t.Context(), uid, "media-password", "127.0.0.1")
		if err != nil {
			t.Fatal(err)
		}
		return principal
	}
	member, owner := create("media-member"), create("media-owner")
	backend := serverRoleFixture()
	backend.policy.OwnerID = owner.UserID()
	backend.policy.Roles[0].Permissions = nil
	authority, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	engine, err := noxawebrtc.New(zap.NewNop(), nil, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = engine.Close() }()
	voice := noxawebrtc.NewVoice(engine, noxawebrtc.NewRouter(zap.NewNop()), zap.NewNop())
	srv := New(&config.Config{}, zap.NewNop(), &Deps{Auth: a, Authority: authority, Chat: db, Groups: db, Voice: voice})
	want := netproto.MediaLimits{VideoMaxBitrate: 800000, VideoMaxWidth: 640, VideoMaxHeight: 360}
	if _, err := srv.SetIntegrationMediaLimits(t.Context(), member, want); !errors.Is(err, authorization.ErrRoleForbidden) {
		t.Fatalf("ordinary member changed media limits: %v", err)
	}
	if _, err := srv.SetIntegrationMediaLimits(t.Context(), owner, netproto.MediaLimits{VideoMaxWidth: 640}); !errors.Is(err, authorization.ErrRoleInvalid) {
		t.Fatalf("invalid limits: %v", err)
	}
	saved, err := srv.SetIntegrationMediaLimits(t.Context(), owner, want)
	if err != nil || saved.MediaLimits != want || saved.Revision != 1 {
		t.Fatalf("owner save: %+v %v", saved, err)
	}
	var read netproto.MediaLimitsChanged
	if err := srv.WithIntegrationMediaLimits(t.Context(), owner, func(_ context.Context, limits netproto.MediaLimitsChanged) error {
		read = limits
		return nil
	}); err != nil || read != netproto.MediaLimitsChanged(saved) {
		t.Fatalf("owner read: %+v %v", read, err)
	}
	var restarted config.Config
	if err := LoadPersistedServerConfig(t.Context(), &restarted, db); err != nil {
		t.Fatal(err)
	}
	if restarted.VideoMaxBitrate != want.VideoMaxBitrate || restarted.VideoMaxWidth != want.VideoMaxWidth || restarted.VideoMaxHeight != want.VideoMaxHeight {
		t.Fatalf("restart limits: %+v", restarted)
	}
	backend.mu.Lock()
	backend.policy.Roles[0].Permissions = []authorization.Capability{authorization.ManageServer}
	backend.policy.Revision++
	backend.mu.Unlock()
	if err := authority.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	saved, err = srv.SetIntegrationMediaLimits(t.Context(), member, netproto.MediaLimits{})
	if err != nil || saved.MediaLimits != (netproto.MediaLimits{}) || saved.Revision != 2 {
		t.Fatalf("delegated clear: %+v %v", saved, err)
	}
	backend.mu.Lock()
	backend.policy.Roles[0].Permissions = nil
	backend.policy.Revision++
	backend.mu.Unlock()
	if err := authority.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := srv.WithIntegrationMediaLimits(t.Context(), member, func(context.Context, netproto.MediaLimitsChanged) error { return nil }); !errors.Is(err, authorization.ErrRoleForbidden) {
		t.Fatalf("revoked read: %v", err)
	}
	if _, err := srv.SetIntegrationMediaLimits(t.Context(), member, want); !errors.Is(err, authorization.ErrRoleForbidden) {
		t.Fatalf("revoked write: %v", err)
	}
	if _, err := db.DB().ExecContext(t.Context(), "UPDATE users SET integration_enabled=FALSE WHERE id=$1", owner.UserID()); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.SetIntegrationMediaLimits(t.Context(), owner, want); !errors.Is(err, auth.ErrIntegrationDenied) {
		t.Fatalf("disabled owner: %v", err)
	}
	entries, err := db.AuditList(t.Context(), 0, 10)
	if err != nil || len(entries) != 2 || entries[0].Actor != member.UniqueID() || entries[0].Action != "media_limits_set" || entries[1].Actor != owner.UniqueID() {
		t.Fatalf("media audits: %+v %v", entries, err)
	}
}
