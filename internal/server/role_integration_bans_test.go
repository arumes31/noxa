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

func TestIntegrationBanPagesUseCurrentAccessThroughDelivery(t *testing.T) {
	db := integrationManagementStore(t)
	a := auth.New(db, zap.NewNop())
	uid, err := a.RegisterUser(t.Context(), "ban-reader", "integration-password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB().ExecContext(t.Context(), "UPDATE users SET integration_enabled=TRUE WHERE unique_id=$1", uid); err != nil {
		t.Fatal(err)
	}
	principal, err := a.AuthenticateIntegration(t.Context(), uid, "integration-password", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	backend := serverRoleFixture()
	backend.policy.OwnerID = principal.UserID() + 100
	authority, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	srv := New(&config.Config{}, zap.NewNop(), &Deps{Authority: authority, Auth: a, BanAdmin: db})
	read := func(p auth.IntegrationPrincipal, query netproto.BanQuery) (netproto.BanPage, error) {
		var page netproto.BanPage
		err := srv.WithIntegrationBans(t.Context(), p, query, func(_ context.Context, result netproto.BanPage) error { page = result; return nil })
		return page, err
	}
	if _, err := read(principal, netproto.BanQuery{}); !errors.Is(err, authorization.ErrRoleForbidden) {
		t.Fatalf("ordinary member bypassed BanMembers: %v", err)
	}
	setGrant := func(granted bool) error {
		backend.mu.Lock()
		backend.policy.Roles[0].Permissions = nil
		if granted {
			backend.policy.Roles[0].Permissions = []authorization.Capability{authorization.BanMembers}
		}
		backend.policy.Revision++
		backend.mu.Unlock()
		return authority.Reload(t.Context())
	}
	if err := setGrant(true); err != nil {
		t.Fatal(err)
	}
	if _, err := read(auth.IntegrationPrincipal{}, netproto.BanQuery{}); !errors.Is(err, auth.ErrIntegrationDenied) {
		t.Fatalf("forged principal: %v", err)
	}
	for _, query := range []netproto.BanQuery{{BeforeID: -1}, {Limit: -1}, {Limit: 101}} {
		if _, err := read(principal, query); !errors.Is(err, authorization.ErrRoleInvalid) {
			t.Fatalf("invalid query %+v: %v", query, err)
		}
	}
	if _, err := db.ListBanPage(t.Context(), 0, 101); err == nil {
		t.Fatal("store accepted unbounded page")
	}
	empty, err := read(principal, netproto.BanQuery{})
	if err != nil || empty.Bans == nil || len(empty.Bans) != 0 || empty.NextBeforeID != 0 {
		t.Fatalf("empty page: %+v %v", empty, err)
	}
	expires := time.Now().Add(time.Hour).Truncate(time.Second)
	var ids []int64
	for i, value := range []string{"older-uid", "middle-uid", "newest-uid"} {
		var id int64
		var expiry *time.Time
		if i == 2 {
			expiry = &expires
		}
		if err := db.DB().QueryRowContext(t.Context(), "INSERT INTO bans (ban_type,value,reason,banned_by,expires_at) VALUES (1,$1,$2,$3,$4) RETURNING id", value, "Reason | with \\ separators", principal.UserID(), expiry).Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	page, err := read(principal, netproto.BanQuery{Limit: 2})
	if err != nil || len(page.Bans) != 2 || page.Bans[0].ID != ids[2] || page.Bans[1].ID != ids[1] || page.NextBeforeID != ids[1] {
		t.Fatalf("first page: %+v %v", page, err)
	}
	if got := page.Bans[0]; got.BannedBy != uid || got.Value != "newest-uid" || got.ExpiresAt != expires.Unix() || got.CreatedAt <= 0 || got.Reason != "Reason | with \\ separators" {
		t.Fatalf("ban fields: %+v", got)
	}
	if page.Bans[1].ExpiresAt != 0 {
		t.Fatal("permanent ban got an expiry")
	}
	// A new ban between pages cannot duplicate or displace older entries.
	if _, err := db.DB().ExecContext(t.Context(), "INSERT INTO bans (ban_type,value) VALUES (1,'concurrent-uid')"); err != nil {
		t.Fatal(err)
	}
	page, err = read(principal, netproto.BanQuery{BeforeID: page.NextBeforeID, Limit: 2})
	if err != nil || len(page.Bans) != 1 || page.Bans[0].ID != ids[0] || page.NextBeforeID != 0 {
		t.Fatalf("last page: %+v %v", page, err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	done := make(chan error, 1)
	go func() {
		done <- srv.WithIntegrationBans(t.Context(), principal, netproto.BanQuery{}, func(ctx context.Context, _ netproto.BanPage) error {
			if srv.roleMetadataMu.TryLock() {
				srv.roleMetadataMu.Unlock()
				return errors.New("delivery released metadata lock")
			}
			close(entered)
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	select {
	case <-entered:
	case err := <-done:
		t.Fatalf("delivery failed: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("delivery did not start")
	}
	revoked := make(chan error, 1)
	go func() { revoked <- setGrant(false) }()
	select {
	case err := <-revoked:
		t.Fatalf("revocation crossed active delivery: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	once.Do(func() { close(release) })
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := <-revoked; err != nil {
		t.Fatal(err)
	}
	if _, err := read(principal, netproto.BanQuery{}); !errors.Is(err, authorization.ErrRoleForbidden) {
		t.Fatalf("same principal retained revoked grant: %v", err)
	}
	if err := setGrant(true); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB().ExecContext(t.Context(), "UPDATE users SET integration_enabled=FALSE WHERE id=$1", principal.UserID()); err != nil {
		t.Fatal(err)
	}
	if _, err := read(principal, netproto.BanQuery{}); !errors.Is(err, auth.ErrIntegrationDenied) {
		t.Fatalf("disabled principal retained read access: %v", err)
	}
}
