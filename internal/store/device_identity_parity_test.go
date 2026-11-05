package store_test

import (
	"testing"

	"noxa/internal/auth"
	"noxa/internal/store"
)

// The store re-derives key-based account IDs because it cannot import auth;
// both derivations must stay identical.
func TestDeviceKeyUniqueIDMatchesAuth(t *testing.T) {
	pub, _, err := auth.GenerateIdentityKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	want, err := auth.UniqueIDFromPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := store.DeviceKeyUniqueIDForTest(pub)
	if !ok || got != want {
		t.Fatalf("deviceKeyUniqueID = %q, %t; want %q", got, ok, want)
	}
	if _, ok := store.DeviceKeyUniqueIDForTest("ed25519-opaque-test-key"); ok {
		t.Fatal("opaque key derived an account ID")
	}
}
