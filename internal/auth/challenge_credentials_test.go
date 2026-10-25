package auth

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"strings"
	"testing"
)

// Exercise the real SQL lookup, including NULL keys left by identity migrations.
// Count verification work rather than asserting noisy wall-clock thresholds.
func TestIdentityChallengeCredentialPaths(t *testing.T) {
	svc, db := testAuthServiceWithStore(t)
	public, private, err := GenerateIdentityKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	challenge := []byte("server-issued-challenge")
	signature, err := SignChallenge(private, challenge)
	if err != nil {
		t.Fatal(err)
	}
	seed, err := hex.DecodeString("9d61b19deffd5a60ba844af492ec2cc44449c5697b326919703bac031cae7f60")
	if err != nil {
		t.Fatal(err)
	}
	dummyPrivate := ed25519.NewKeyFromSeed(seed)
	dummyPublic, err := LoadPublicKey(dummyChallengePublicKey)
	if err != nil || !bytes.Equal(dummyPublic, dummyPrivate.Public().(ed25519.PublicKey)) {
		t.Fatalf("invalid dummy verification key: %v", err)
	}
	dummySignature := ed25519.Sign(dummyPrivate, challenge)
	offCurve, err := hex.DecodeString("efffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff7f")
	if err != nil {
		t.Fatal(err)
	}
	offCurvePEM := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: offCurve}))
	tampered := bytes.Clone(signature)
	tampered[0] ^= 1
	tests := []struct {
		name      string
		key       any
		missing   bool
		signature []byte
		challenge []byte
		wantOK    bool
		wantDummy bool
	}{
		{name: "unknown", missing: true, challenge: challenge, signature: signature, wantDummy: true},
		{name: "unknown with valid dummy signature", missing: true, challenge: challenge, signature: dummySignature, wantDummy: true},
		{name: "NULL", key: nil, challenge: challenge, signature: dummySignature, wantDummy: true},
		{name: "empty", key: "", challenge: challenge, signature: dummySignature, wantDummy: true},
		{name: "malformed PEM", key: "not a public key", challenge: challenge, signature: dummySignature, wantDummy: true},
		{name: "wrong PEM type", key: strings.ReplaceAll(public, "PUBLIC KEY", "PRIVATE KEY"), challenge: challenge, signature: dummySignature, wantDummy: true},
		{name: "wrong key length", key: "-----BEGIN PUBLIC KEY-----\nYQ==\n-----END PUBLIC KEY-----\n", challenge: challenge, signature: dummySignature, wantDummy: true},
		{name: "off-curve key", key: offCurvePEM, challenge: challenge, signature: dummySignature, wantDummy: true},
		{name: "correct", key: public, challenge: challenge, signature: signature, wantOK: true},
		{name: "wrong signature", key: public, challenge: challenge, signature: tampered},
		{name: "dummy signature for real key", key: public, challenge: challenge, signature: dummySignature},
		{name: "short signature", key: public, challenge: challenge, signature: []byte{1}},
		{name: "empty challenge", key: public, signature: signature},
		{name: "empty signature", key: public, challenge: challenge},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			uid := uniqueNickname("challenge-path")
			if !tc.missing {
				if _, err := db.DB().ExecContext(t.Context(), "INSERT INTO users (unique_id, public_key) VALUES ($1, $2)", uid, tc.key); err != nil {
					t.Fatal(err)
				}
				defer func() {
					if _, err := db.DB().ExecContext(context.Background(), "DELETE FROM users WHERE unique_id=$1", uid); err != nil {
						t.Error(err)
					}
				}()
			}
			calls := 0
			svc.verifyChallenge = func(key string, gotChallenge, gotSignature []byte) error {
				calls++
				expectedKey := public
				if tc.wantDummy {
					expectedKey = dummyChallengePublicKey
				}
				if key != expectedKey || !bytes.Equal(gotChallenge, tc.challenge) || !bytes.Equal(gotSignature, tc.signature) {
					t.Fatal("verification used different key or input")
				}
				return VerifyChallenge(key, gotChallenge, gotSignature)
			}
			for _, wrapper := range []bool{false, true} {
				calls = 0
				var key string
				var ok bool
				var err error
				if wrapper {
					ok, err = svc.AuthenticateChallenge(t.Context(), uid, tc.challenge, tc.signature)
				} else {
					key, ok, err = svc.AuthenticateIdentityChallenge(t.Context(), uid, tc.challenge, tc.signature)
				}
				if (tc.missing && !errors.Is(err, ErrUserNotFound)) || (!tc.missing && err != nil) {
					t.Fatalf("error=%v missing=%v", err, tc.missing)
				}
				if ok != tc.wantOK || calls != 1 {
					t.Fatalf("ok=%v want=%v verification calls=%d", ok, tc.wantOK, calls)
				}
				if !wrapper && ((ok && key != public) || (!ok && key != "")) {
					t.Fatalf("incorrect returned identity %q", key)
				}
			}
		})
	}
	t.Run("database failure", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		svc.verifyChallenge = func(string, []byte, []byte) error { t.Fatal("verifier called after database failure"); return nil }
		key, ok, err := svc.AuthenticateIdentityChallenge(ctx, "anything", challenge, signature)
		if key != "" || ok || !errors.Is(err, context.Canceled) {
			t.Fatalf("key=%q ok=%v err=%v", key, ok, err)
		}
	})
}
