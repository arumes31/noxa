package auth

import (
	"context"
	"errors"
	"testing"
)

func TestBindPublicKeyRefusesAnotherAccountsKey(t *testing.T) {
	svc, db := testAuthServiceWithStore(t)
	ctx := t.Context()
	register := func(prefix string) *User {
		t.Helper()
		nick := uniqueNickname(prefix)
		uid, err := svc.RegisterUser(ctx, nick, "binding-password")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_, _ = db.DB().ExecContext(context.Background(), "DELETE FROM users WHERE unique_id = $1", uid)
		})
		u, err := svc.LookupUser(ctx, uid)
		if err != nil {
			t.Fatal(err)
		}
		return u
	}
	victim := register("bind-victim")
	attacker := register("bind-attacker")

	key, _, err := GenerateIdentityKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.BindPublicKey(ctx, victim.ID, key); err != nil {
		t.Fatalf("victim bind: %v", err)
	}
	// Re-binding the same key to its owner stays idempotent.
	if err := svc.BindPublicKey(ctx, victim.ID, key); err != nil {
		t.Fatalf("victim re-bind: %v", err)
	}
	if err := svc.BindPublicKey(ctx, attacker.ID, key); !errors.Is(err, ErrPublicKeyInUse) {
		t.Fatalf("attacker bind = %v, want ErrPublicKeyInUse", err)
	}
	got, err := svc.LookupUserByPublicKey(ctx, key)
	if err != nil || got.ID != victim.ID {
		t.Fatalf("lookup = %+v, %v; want victim", got, err)
	}

	// A key whose derived unique ID is another account's is refused as well.
	derivedKey, _, err := GenerateIdentityKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	derivedUID, err := UniqueIDFromPublicKey(derivedKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB().ExecContext(ctx, "INSERT INTO users (unique_id, nickname) VALUES ($1, $2)", derivedUID, uniqueNickname("bind-derived")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.DB().ExecContext(context.Background(), "DELETE FROM users WHERE unique_id = $1", derivedUID)
	})
	if err := svc.BindPublicKey(ctx, attacker.ID, derivedKey); !errors.Is(err, ErrPublicKeyInUse) {
		t.Fatalf("derived-ID bind = %v, want ErrPublicKeyInUse", err)
	}

	if err := svc.BindPublicKey(ctx, attacker.ID, "not a key"); err == nil {
		t.Fatal("malformed key bound")
	}
}
