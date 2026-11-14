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

// A linked guest's root key (its principal is derived from the key) and
// another principal's device key are identities too: a password account may
// not claim them.
func TestBindPublicKeyRefusesGuestAndDeviceKeys(t *testing.T) {
	svc, db := testAuthServiceWithStore(t)
	ctx := t.Context()
	nick := uniqueNickname("bind-device-attacker")
	uid, err := svc.RegisterUser(ctx, nick, "binding-password")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.DB().ExecContext(context.Background(), "DELETE FROM users WHERE unique_id = $1", uid)
	})
	attacker, err := svc.LookupUser(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	guestKey, _, err := GenerateIdentityKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	guestUID, err := UniqueIDFromPublicKey(guestKey)
	if err != nil {
		t.Fatal(err)
	}
	deviceKey, _, err := GenerateIdentityKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB().ExecContext(ctx, `INSERT INTO user_devices (device_id, principal, kind, name, ed25519_public)
		VALUES ($1, $2, 'web', 'Phone', $3)`, "dev-"+nick, guestUID, deviceKey); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.DB().ExecContext(context.Background(), "DELETE FROM user_devices WHERE device_id = $1", "dev-"+nick)
	})
	if err := svc.BindPublicKey(ctx, attacker.ID, guestKey); !errors.Is(err, ErrPublicKeyInUse) {
		t.Fatalf("guest root key bind = %v, want ErrPublicKeyInUse", err)
	}
	if err := svc.BindPublicKey(ctx, attacker.ID, deviceKey); !errors.Is(err, ErrPublicKeyInUse) {
		t.Fatalf("device key bind = %v, want ErrPublicKeyInUse", err)
	}
	own, _, err := GenerateIdentityKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.BindPublicKey(ctx, attacker.ID, own); err != nil {
		t.Fatalf("own fresh key bind: %v", err)
	}
}
