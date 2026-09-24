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
	"noxa/internal/rules"
	"noxa/internal/store"
)

// Change the stored wording immediately after it was read. A second Text call
// would return the replacement and count a different set of acceptances.
type changingRulesSettings struct {
	*store.Store
	next string
}

func (s *changingRulesSettings) GetServerSetting(ctx context.Context, key string) (string, uint32, error) {
	value, id, err := s.Store.GetServerSetting(ctx, key)
	if err == nil && s.next != "" {
		err = s.SetServerSetting(ctx, rules.SettingKey, s.next, 0)
		s.next = ""
	}
	return value, id, err
}

func TestIntegrationRulesCountsReturnedWordingAndCurrentAuthority(t *testing.T) {
	db := integrationManagementStore(t)
	a := auth.New(db, zap.NewNop())
	create := func(name string) auth.IntegrationPrincipal {
		t.Helper()
		uid, err := a.RegisterUser(t.Context(), name, "rules-password")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.DB().ExecContext(t.Context(), "UPDATE users SET integration_enabled=TRUE WHERE unique_id=$1", uid); err != nil {
			t.Fatal(err)
		}
		p, err := a.AuthenticateIntegration(t.Context(), uid, "rules-password", "127.0.0.1")
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	member, owner := create("rules-reader"), create("rules-owner")
	backend := serverRoleFixture()
	backend.policy.OwnerID = owner.UserID()
	backend.policy.Roles[0].Permissions = nil
	authority, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	settings := &changingRulesSettings{Store: db}
	r := rules.New(settings, db.DB())
	srv := New(&config.Config{}, zap.NewNop(), &Deps{Auth: a, Authority: authority, Rules: r})
	read := func(p auth.IntegrationPrincipal) (netproto.RulesInspection, error) {
		var result netproto.RulesInspection
		err := srv.WithIntegrationRules(t.Context(), p, func(ctx context.Context, got netproto.RulesInspection) error {
			if srv.roleMetadataMu.TryLock() {
				srv.roleMetadataMu.Unlock()
				return errors.New("metadata released before delivery")
			}
			if _, ok := ctx.Value(roleLeaseKey{}).(roleLease); !ok {
				return errors.New("missing policy lease")
			}
			result = got
			return nil
		})
		return result, err
	}
	if _, err := read(member); !errors.Is(err, authorization.ErrRoleForbidden) {
		t.Fatalf("ordinary member read: %v", err)
	}
	got, err := read(owner)
	if err != nil || got != (netproto.RulesInspection{}) {
		t.Fatalf("empty rules: %+v %v", got, err)
	}
	const first, second = "First | rules\nwording", "Changed wording"
	if err := r.Set(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	if err := r.Accept(t.Context(), member.UserID(), rules.Hash(first)); err != nil {
		t.Fatal(err)
	}
	settings.next = second
	got, err = read(owner)
	if err != nil || got.Text != first || got.Hash != rules.Hash(first) || got.AcceptedClients != 1 {
		t.Fatalf("mixed wording/count: %+v %v", got, err)
	}
	got, err = read(owner)
	if err != nil || got.Text != second || got.Hash != rules.Hash(second) || got.AcceptedClients != 0 {
		t.Fatalf("old acceptance counted for new wording: %+v %v", got, err)
	}
	if err := r.Accept(t.Context(), owner.UserID(), rules.Hash(second)); err != nil {
		t.Fatal(err)
	}
	backend.mu.Lock()
	backend.policy.Roles[0].Permissions = []authorization.Capability{authorization.ManageServer}
	backend.policy.Revision++
	backend.mu.Unlock()
	if err := authority.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	got, err = read(member)
	if err != nil || got.AcceptedClients != 1 || got.Hash != rules.Hash(second) {
		t.Fatalf("delegated read: %+v %v", got, err)
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
	if _, err := db.DB().ExecContext(t.Context(), "UPDATE users SET integration_enabled=FALSE WHERE id=$1", owner.UserID()); err != nil {
		t.Fatal(err)
	}
	if _, err := read(owner); !errors.Is(err, auth.ErrIntegrationDenied) {
		t.Fatalf("disabled owner: %v", err)
	}
	if err := srv.WithIntegrationRules(t.Context(), owner, nil); !errors.Is(err, authorization.ErrRoleInvalid) {
		t.Fatal(err)
	}
}
