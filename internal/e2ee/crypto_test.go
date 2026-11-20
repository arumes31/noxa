package e2ee

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"testing"
)

func TestHKDFPreservesExistingVectors(t *testing.T) {
	secret := []byte("voicx hkdf secret")
	info := []byte{0, 0xff, 1, 0x80, 'A'}
	for _, test := range []struct {
		name string
		salt []byte
		want string
	}{
		{name: "nil salt", want: "513aea0af16e6632e061361077bafe38b2dc159fecf81a39463e86b1a9939be2"},
		{name: "explicit salt", salt: []byte{1, 2, 3, 4, 5, 6, 7, 8}, want: "d8be36b07f0478379dec7930f1295b889aa7faa7f68021f8019efa6f17b8c27a"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := hkdf(secret, test.salt, info, 32)
			if actual := fmt.Sprintf("%x", got); actual != test.want {
				t.Fatalf("hkdf = %s, want %s", actual, test.want)
			}
		})
	}
}

func TestHKDFRoundTripLabelsRemainDistinct(t *testing.T) {
	secret := []byte("shared secret")
	first := hkdf(secret, nil, []byte{0, 1, 2, 0xff}, 32)
	second := hkdf(secret, nil, []byte{0, 1, 2, 0xfe}, 32)
	if len(first) != 32 || bytes.Equal(first, second) {
		t.Fatalf("derived keys are not distinct 32-byte values")
	}
}

func TestFileChunkBinding(t *testing.T) {
	secret := make([]byte, 32)
	blob, err := EncryptFileChunk(secret, "file-a", 3, []byte("payload"))
	if err != nil {
		t.Fatal(err)
	}
	if plain, err := DecryptFileChunk(secret, "file-a", 3, blob); err != nil || string(plain) != "payload" {
		t.Fatalf("chunk = %q, %v", plain, err)
	}
	if _, err := DecryptFileChunk(secret, "file-a", 4, blob); err == nil {
		t.Fatal("chunk opened under a different index")
	}
}

func TestSignedPreKeyTamperRejected(t *testing.T) {
	identity, _ := GenerateX25519()
	signed, _ := GenerateX25519()
	oneTime, _ := GenerateX25519()
	_, signingPrivate, _ := ed25519.GenerateKey(rand.Reader)
	bundle := NewPreKeyBundle(identity, signingPrivate, 1, signed, 2, oneTime)
	if !VerifyPreKeyBundle(bundle) {
		t.Fatal("valid bundle rejected")
	}
	bundle.SignedPreKey[0] ^= 1
	if VerifyPreKeyBundle(bundle) {
		t.Fatal("tampered signed prekey accepted")
	}
}
