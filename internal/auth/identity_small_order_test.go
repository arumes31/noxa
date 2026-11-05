package auth

import (
	"crypto/ed25519"
	"encoding/pem"
	"strings"
	"testing"

	"filippo.io/edwards25519"
)

func TestLoadPublicKeyRejectsSmallOrderPoints(t *testing.T) {
	identity := make([]byte, ed25519.PublicKeySize)
	identity[0] = 1 // the neutral element: [S]B == R verifies for any message
	identityPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: identity}))
	if _, err := LoadPublicKey(identityPEM); err == nil {
		t.Fatal("LoadPublicKey accepted the identity point")
	}

	// A forged signature for the identity key must not verify.
	s, err := edwards25519.NewScalar().SetCanonicalBytes(append([]byte{7}, make([]byte, 31)...))
	if err != nil {
		t.Fatal(err)
	}
	r := new(edwards25519.Point).ScalarBaseMult(s)
	forged := append(r.Bytes(), s.Bytes()...)
	if !ed25519.Verify(ed25519.PublicKey(identity), []byte("any"), forged) {
		t.Fatal("test premise: cofactorless Verify accepts the forgery for the identity key")
	}
	if err := VerifyChallenge(identityPEM, []byte("any"), forged); err == nil {
		t.Fatal("VerifyChallenge accepted a forged signature for a small-order key")
	}
	if _, err := UniqueIDFromPublicKey(identityPEM); err == nil {
		t.Fatal("UniqueIDFromPublicKey accepted a small-order key")
	}

	pub, _, err := GenerateIdentityKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPublicKey(pub); err != nil {
		t.Fatalf("ordinary key rejected: %v", err)
	}
}

func TestCanonicalPublicKeyNormalizesSpellings(t *testing.T) {
	pub, _, err := GenerateIdentityKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	for _, spelling := range []string{
		pub,
		strings.ReplaceAll(pub, "\n", "\r\n"),
		strings.TrimSuffix(pub, "\n"),
		"leading text\n" + pub + "trailing text",
	} {
		got, err := CanonicalPublicKey(spelling)
		if err != nil || got != pub {
			t.Fatalf("CanonicalPublicKey(%q) = %q, %v; want %q", spelling, got, err, pub)
		}
	}
}
