//go:build integration

package auth

import (
	"context"
	"errors"
	"testing"
)

func TestIntegrationIdentityEligibilityAndRevocation(t *testing.T) {
	svc, db := testAuthServiceWithStore(t)
	verify := svc.passwordVerifier()
	verifications := 0
	svc.verifyPassword = func(password, hash string) error {
		verifications++
		return verify(password, hash)
	}
	ctx := t.Context()
	nick := uniqueNickname("integration")
	const password = "integration-password"
	const ip = "192.0.2.78"
	uid, err := svc.RegisterUser(ctx, nick, password)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.DB().ExecContext(context.Background(), "DELETE FROM bans WHERE value IN ($1, $2)", uid, ip)
		_, _ = db.DB().ExecContext(context.Background(), "DELETE FROM users WHERE unique_id = $1", uid)
	})
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.DB().ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	denyLogin := func(identifier, pw, address string) {
		t.Helper()
		before := verifications
		p, err := svc.AuthenticateIntegration(ctx, identifier, pw, address)
		if !errors.Is(err, ErrIntegrationDenied) || p.UserID() != 0 {
			t.Fatalf("login unexpectedly issued identity: %+v, %v", p, err)
		}
		if verifications-before != 1 {
			t.Fatalf("login performed %d password verifications, want one", verifications-before)
		}
	}
	denyLogin(uid, password, ip) // new accounts are disabled
	exec("UPDATE users SET is_bot = TRUE WHERE unique_id = $1", uid)
	denyLogin(uid, password, ip) // bot identity alone does not opt in
	exec("UPDATE users SET integration_enabled = TRUE WHERE unique_id = $1", uid)
	denyLogin(uid, "wrong-password", ip)
	denyLogin("missing-"+uid, password, ip)
	denyLogin(uid, password, "")
	denyLogin(uid, password, "192.0.2.78:1234")
	principal, err := svc.AuthenticateIntegration(ctx, nick, password, ip)
	if err != nil || principal.UserID() <= 0 || principal.UniqueID() != uid {
		t.Fatalf("nickname login did not resolve canonical identity: %+v, %v", principal, err)
	}
	byID, err := svc.AuthenticateIntegration(ctx, uid, password, "::ffff:"+ip)
	if err != nil || byID.UserID() != principal.UserID() {
		t.Fatalf("unique-ID login: %+v, %v", byID, err)
	}
	if err := svc.ValidateIntegration(ctx, principal); err != nil {
		t.Fatal(err)
	}
	if err := svc.ValidateIntegration(ctx, IntegrationPrincipal{}); !errors.Is(err, ErrIntegrationDenied) {
		t.Fatalf("zero identity accepted: %v", err)
	}
	if err := New(db, nil).ValidateIntegration(ctx, principal); !errors.Is(err, ErrIntegrationDenied) {
		t.Fatalf("identity from another issuer accepted: %v", err)
	}
	exec("UPDATE users SET integration_enabled = FALSE WHERE unique_id = $1", uid)
	if err := svc.ValidateIntegration(ctx, principal); !errors.Is(err, ErrIntegrationDenied) {
		t.Fatalf("disabled account retained access: %v", err)
	}
	exec("UPDATE users SET integration_enabled = TRUE WHERE unique_id = $1", uid)
	for _, tc := range []struct {
		name  string
		kind  int
		value string
	}{
		{"identity ban", 1, uid},
		{"address ban", 0, ip},
	} {
		t.Run(tc.name, func(t *testing.T) {
			exec("INSERT INTO bans (ban_type, value, reason) VALUES ($1, $2, 'private reason')", tc.kind, tc.value)
			for _, p := range []IntegrationPrincipal{principal, byID} {
				if err := svc.ValidateIntegration(ctx, p); !errors.Is(err, ErrIntegrationDenied) {
					t.Fatalf("banned identity retained access: %v", err)
				}
			}
			denyLogin(uid, password, ip)
			exec("UPDATE bans SET expires_at = NOW() - INTERVAL '1 second' WHERE value = $1", tc.value)
			if err := svc.ValidateIntegration(ctx, principal); err != nil {
				t.Fatalf("expired ban still applied: %v", err)
			}
			exec("DELETE FROM bans WHERE value = $1", tc.value)
		})
	}
	for _, tc := range []struct{ peer, ban string }{
		{ip, "::ffff:" + ip},
		{"2001:db8::78", "2001:0db8:0:0:0:0:0:78"},
	} {
		t.Run("equivalent-address-"+tc.peer, func(t *testing.T) {
			p, err := svc.AuthenticateIntegration(ctx, uid, password, tc.peer)
			if err != nil {
				t.Fatal(err)
			}
			exec("INSERT INTO bans (ban_type, value) VALUES (0, $1)", tc.ban)
			t.Cleanup(func() { _, _ = db.DB().ExecContext(context.Background(), "DELETE FROM bans WHERE value = $1", tc.ban) })
			if err := svc.ValidateIntegration(ctx, p); !errors.Is(err, ErrIntegrationDenied) {
				t.Fatalf("equivalent IP ban missed: %v", err)
			}
			denyLogin(uid, password, tc.peer)
		})
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := svc.ValidateIntegration(canceled, principal); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled lookup failed open: %v", err)
	}
	exec("DELETE FROM users WHERE unique_id = $1", uid)
	if err := svc.ValidateIntegration(ctx, principal); !errors.Is(err, ErrIntegrationDenied) {
		t.Fatalf("deleted identity retained access: %v", err)
	}
	exec("INSERT INTO users (unique_id, nickname, integration_enabled) VALUES ($1, $2, TRUE)", uid, nick)
	if err := svc.ValidateIntegration(ctx, principal); !errors.Is(err, ErrIntegrationDenied) {
		t.Fatalf("replacement account inherited old session: %v", err)
	}
}
