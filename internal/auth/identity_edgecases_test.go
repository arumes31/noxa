package auth

import (
	"crypto/ed25519"
	"encoding/pem"
	"strings"
	"testing"
)

func TestLoadKeyRejectsMalformedPEM(t *testing.T) {
	t.Parallel()

	for _, key := range []struct {
		kind, block, otherBlock string
		size                    int
		load                    func(string) error
	}{
		{"public", "PUBLIC KEY", "PRIVATE KEY", ed25519.PublicKeySize, func(s string) error { _, err := LoadPublicKey(s); return err }},
		{"private", "PRIVATE KEY", "PUBLIC KEY", ed25519.PrivateKeySize, func(s string) error { _, err := LoadPrivateKey(s); return err }},
	} {
		tests := []struct {
			name    string
			encoded string
			wantErr string
		}{
			{name: "empty", wantErr: "decode " + key.kind + " key PEM"},
			{name: "not PEM", encoded: "not a pem block", wantErr: "decode " + key.kind + " key PEM"},
			{name: "wrong block type", encoded: encodeTestPEM(key.otherBlock, make([]byte, key.size)), wantErr: "unexpected PEM block type"},
			{name: "short key", encoded: encodeTestPEM(key.block, make([]byte, key.size-1)), wantErr: "invalid ed25519 " + key.kind + " key length"},
			{name: "long key", encoded: encodeTestPEM(key.block, make([]byte, key.size+1)), wantErr: "invalid ed25519 " + key.kind + " key length"},
		}
		for _, test := range tests {
			t.Run(key.kind+"/"+test.name, func(t *testing.T) {
				t.Parallel()

				if err := key.load(test.encoded); err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("Load %s key error = %v, want text %q", key.kind, err, test.wantErr)
				}
			})
		}
	}
}

func TestChallengeOperationsRejectMissingInputs(t *testing.T) {
	t.Parallel()

	publicKey, privateKey, err := GenerateIdentityKeyPair()
	if err != nil {
		t.Fatalf("GenerateIdentityKeyPair: %v", err)
	}

	if _, err := SignChallenge(privateKey, nil); err == nil || !strings.Contains(err.Error(), "challenge must not be empty") {
		t.Fatalf("SignChallenge(nil) error = %v, want empty-challenge error", err)
	}
	if err := VerifyChallenge(publicKey, nil, make([]byte, ed25519.SignatureSize)); err == nil || !strings.Contains(err.Error(), "challenge must not be empty") {
		t.Fatalf("VerifyChallenge(empty challenge) error = %v, want empty-challenge error", err)
	}
	if err := VerifyChallenge(publicKey, []byte("challenge"), nil); err == nil || !strings.Contains(err.Error(), "signature must not be empty") {
		t.Fatalf("VerifyChallenge(empty signature) error = %v, want empty-signature error", err)
	}
}

func encodeTestPEM(blockType string, data []byte) string {
	return string(pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: data}))
}
