package auth

import (
	"context"
	"testing"
)

func TestBotIdentitySurvivesEveryAccountLookup(t *testing.T) {
	svc, db := testAuthServiceWithStore(t)
	nick := uniqueNickname("bot-identity")
	uid, err := svc.RegisterUser(t.Context(), nick, "bot-password")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.DB().ExecContext(context.Background(), "DELETE FROM users WHERE unique_id = $1", uid)
	})
	u, err := svc.LookupUser(t.Context(), uid)
	if err != nil || u.IsBot {
		t.Fatalf("new account bot flag: %+v, %v", u, err)
	}
	key := "test-public-key-" + uid
	if _, err := db.DB().ExecContext(t.Context(), "UPDATE users SET is_bot = TRUE, public_key = $2 WHERE unique_id = $1", uid, key); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		lookup func() (*User, error)
	}{
		{"unique_id", func() (*User, error) { return svc.LookupUser(t.Context(), uid) }},
		{"nickname", func() (*User, error) { return svc.AuthenticateNickname(t.Context(), nick, "bot-password") }},
		{"identifier", func() (*User, error) { return svc.AuthenticateIdentifier(t.Context(), uid, "bot-password") }},
		{"public_key", func() (*User, error) { return svc.LookupUserByPublicKey(t.Context(), key) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.lookup()
			if err != nil || got == nil || !got.IsBot || got.UniqueID != uid {
				t.Fatalf("bot identity: %+v, %v", got, err)
			}
		})
	}
}

func TestEnrolledIdentityDisplayNameLookup(t *testing.T) {
	svc, db := testAuthServiceWithStore(t)
	uid := uniqueNickname("display-label")
	key := "test-public-key-" + uid
	_, err := db.DB().ExecContext(t.Context(), "INSERT INTO users(unique_id, public_key, identity_display_name) VALUES($1,$2,$3)", uid, key, "Daniel")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.DB().ExecContext(context.Background(), "DELETE FROM users WHERE unique_id=$1", uid) })
	for _, lookup := range []func() (*User, error){
		func() (*User, error) { return svc.LookupUser(t.Context(), uid) },
		func() (*User, error) { return svc.LookupUserByPublicKey(t.Context(), key) },
	} {
		user, err := lookup()
		if err != nil || user.Nickname != "" || user.DisplayName != "Daniel" || user.UniqueID != uid {
			t.Fatalf("display label lookup: %+v, %v", user, err)
		}
	}
}
