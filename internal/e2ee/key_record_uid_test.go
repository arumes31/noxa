package e2ee_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"testing"

	"noxa/internal/auth"
	"noxa/internal/e2ee"
)

// The client-side derivation must match the server's key-login unique ID.
func TestFingerprintCertifiesUIDMatchesKeyLogin(t *testing.T) {
	pub, private, _ := ed25519.GenerateKey(rand.Reader)
	uid, err := auth.UniqueIDFromPublicKey(string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pub})))
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := e2ee.SignKeyRecord(uid, [32]byte{9}, private)
	if err != nil {
		t.Fatal(err)
	}
	record, _ := e2ee.ParseKeyRecord(encoded, uid)
	if !e2ee.FingerprintCertifiesUID(record.Fingerprint(), uid) {
		t.Fatal("key-derived uid not recognised")
	}
	_, otherPrivate, _ := ed25519.GenerateKey(rand.Reader)
	forged, _ := e2ee.SignKeyRecord(uid, [32]byte{9}, otherPrivate)
	forgedRecord, _ := e2ee.ParseKeyRecord(forged, uid)
	if e2ee.FingerprintCertifiesUID(forgedRecord.Fingerprint(), uid) {
		t.Fatal("another key certifies the uid")
	}
	if e2ee.FingerprintCertifiesUID("zz", uid) || e2ee.FingerprintCertifiesUID(record.Fingerprint(), "password-account") {
		t.Fatal("malformed input certified")
	}
}
