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
	if err := svc.BindPublicKey(ctx, victim.ID, key, true); err != nil {
		t.Fatalf("victim bind: %v", err)
	}
	// Re-binding the same key to its owner stays idempotent.
	if err := svc.BindPublicKey(ctx, victim.ID, key, true); err != nil {
		t.Fatalf("victim re-bind: %v", err)
	}
	if err := svc.BindPublicKey(ctx, attacker.ID, key, true); !errors.Is(err, ErrPublicKeyInUse) {
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
	if err := svc.BindPublicKey(ctx, attacker.ID, derivedKey, true); !errors.Is(err, ErrPublicKeyInUse) {
		t.Fatalf("derived-ID bind = %v, want ErrPublicKeyInUse", err)
	}

	if err := svc.BindPublicKey(ctx, attacker.ID, "not a key", true); err == nil {
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
	if err := svc.BindPublicKey(ctx, attacker.ID, guestKey, true); !errors.Is(err, ErrPublicKeyInUse) {
		t.Fatalf("guest root key bind = %v, want ErrPublicKeyInUse", err)
	}
	if err := svc.BindPublicKey(ctx, attacker.ID, deviceKey, true); !errors.Is(err, ErrPublicKeyInUse) {
		t.Fatalf("device key bind = %v, want ErrPublicKeyInUse", err)
	}
	own, _, err := GenerateIdentityKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.BindPublicKey(ctx, attacker.ID, own, true); err != nil {
		t.Fatalf("own fresh key bind: %v", err)
	}
}

// An unproven binding never routes a key login; a proven binding of the same
// key by another account replaces it, and its own proven login upgrades it.
func TestUnprovenBindingNeverRoutesAndProvenWins(t *testing.T) {
	svc, db := testAuthServiceWithStore(t)
	ctx := t.Context()
	register := func(prefix string) *User {
		t.Helper()
		uid, err := svc.RegisterUser(ctx, uniqueNickname(prefix), "binding-password")
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
	attacker := register("unproven-attacker")
	owner := register("unproven-owner")
	key, private, err := GenerateIdentityKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.BindPublicKey(ctx, attacker.ID, key, false); err != nil {
		t.Fatalf("unproven bind: %v", err)
	}
	if _, err := svc.LookupUserByPublicKey(ctx, key); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("unproven binding routed a key lookup: %v", err)
	}
	challenge, err := GenerateChallenge()
	if err != nil {
		t.Fatal(err)
	}
	signature, err := SignLoginChallenge(private, challenge)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := svc.AuthenticateIdentityChallenge(ctx, attacker.UniqueID, challenge, signature); ok {
		t.Fatal("an unproven binding authenticated a challenge login")
	}
	// An unproven claim cannot take a key from another account either.
	if err := svc.BindPublicKey(ctx, owner.ID, key, false); !errors.Is(err, ErrPublicKeyInUse) {
		t.Fatalf("second unproven bind = %v, want ErrPublicKeyInUse", err)
	}
	if err := svc.BindPublicKey(ctx, owner.ID, key, true); err != nil {
		t.Fatalf("proven bind over an unproven claim: %v", err)
	}
	if got, err := svc.LookupUserByPublicKey(ctx, key); err != nil || got.ID != owner.ID {
		t.Fatalf("lookup = %+v, %v; want the proven owner", got, err)
	}
	if err := svc.BindPublicKey(ctx, attacker.ID, key, false); !errors.Is(err, ErrPublicKeyInUse) {
		t.Fatalf("unproven claim of a proven key = %v, want ErrPublicKeyInUse", err)
	}
	// An unproven rebind by the owner (older client again) keeps it proven.
	if err := svc.BindPublicKey(ctx, owner.ID, key, false); err != nil {
		t.Fatalf("owner unproven rebind: %v", err)
	}
	if got, err := svc.LookupUserByPublicKey(ctx, key); err != nil || got.ID != owner.ID {
		t.Fatalf("owner rebind downgraded the key: %+v, %v", got, err)
	}

	// Upgrade: an unproven binding becomes routable once proven.
	upgraded, upgradedPrivate, err := GenerateIdentityKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.BindPublicKey(ctx, attacker.ID, upgraded, false); err != nil {
		t.Fatal(err)
	}
	if err := svc.BindPublicKey(ctx, attacker.ID, upgraded, true); err != nil {
		t.Fatal(err)
	}
	signature, err = SignLoginChallenge(upgradedPrivate, challenge)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := svc.AuthenticateIdentityChallenge(ctx, attacker.UniqueID, challenge, signature); !ok || err != nil {
		t.Fatalf("upgraded binding challenge login = %v, %v", ok, err)
	}
}

// An older client's password login (unproven, different key) must not
// replace the key a newer client proved, or key login stops working.
func TestUnprovenBindingKeepsProvenKey(t *testing.T) {
	svc, db := testAuthServiceWithStore(t)
	ctx := t.Context()
	uid, err := svc.RegisterUser(ctx, uniqueNickname("proven-keeper"), "binding-password")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.DB().ExecContext(context.Background(), "DELETE FROM users WHERE unique_id = $1", uid)
	})
	owner, err := svc.LookupUser(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	proven, _, err := GenerateIdentityKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	legacy, _, err := GenerateIdentityKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.BindPublicKey(ctx, owner.ID, proven, true); err != nil {
		t.Fatal(err)
	}
	if err := svc.BindPublicKey(ctx, owner.ID, legacy, false); err != nil {
		t.Fatalf("older client's password login: %v", err)
	}
	if got, err := svc.LookupUserByPublicKey(ctx, proven); err != nil || got.ID != owner.ID {
		t.Fatalf("proven key lost after an unproven login: %+v, %v", got, err)
	}
	if _, err := svc.LookupUserByPublicKey(ctx, legacy); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("unproven key routes a key login: %v", err)
	}
}
