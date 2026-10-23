package e2ee

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"golang.org/x/crypto/nacl/box"
	"strings"
	"testing"
)

func TestSignedDMIdentityBinding(t *testing.T) {
	_, signing, _ := ed25519.GenerateKey(rand.Reader)
	pub, _, _ := box.GenerateKey(rand.Reader)
	encoded, err := SignKeyRecord("account-alias", *pub, signing)
	if err != nil {
		t.Fatal(err)
	}
	record, err := ParseKeyRecord(encoded, "account-alias")
	if err != nil || record.PublicKey() != *pub {
		t.Fatal("valid record rejected", err)
	}
	if _, err := ParseKeyRecord(encoded, "other-account"); err == nil {
		t.Fatal("cross-account substitution")
	}
	if _, err := ParseKeyRecord(base64.StdEncoding.EncodeToString(pub[:]), "account-alias"); err == nil {
		t.Fatal("unsigned fallback")
	}
	for _, mutate := range []func(*KeyRecord){
		func(r *KeyRecord) { r.UniqueID = "other-account" },
		func(r *KeyRecord) { r.SigningKey[0] ^= 1 },
		func(r *KeyRecord) { r.EncryptionKey[0] ^= 1 },
		func(r *KeyRecord) { r.Signature[0] ^= 1 },
	} {
		r, _ := DecodeKeyRecord(encoded)
		mutate(&r)
		raw, _ := json.Marshal(r)
		if _, err := DecodeKeyRecord(keyRecordPrefix + base64.StdEncoding.EncodeToString(raw)); err == nil {
			t.Fatal("tampered record accepted")
		}
	}
	if _, err := DecodeKeyRecord(strings.Replace(encoded, ":", ":\n", 1)); err == nil {
		t.Fatal("noncanonical record accepted")
	}
	if _, err := SignKeyRecord("account-alias", [32]byte{}, signing); err == nil {
		t.Fatal("low-order key accepted")
	}
}
