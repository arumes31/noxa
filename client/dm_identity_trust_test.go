package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"noxa/internal/auth"
	"noxa/internal/e2ee"
	"os"
	"testing"
)

func signedTestRecord(t *testing.T, uid string, key [32]byte, id *identity) string {
	t.Helper()
	signing, err := auth.LoadPrivateKey(id.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	record, err := e2ee.SignKeyRecord(uid, key, signing)
	if err != nil {
		t.Fatal(err)
	}
	return record
}
func TestPeerIdentityRequiresExplicitConfirmationAndPersists(t *testing.T) {
	cm := newTestConnManager()
	cm.id = mustTempIdentity(t)
	cm.addr = "trust-test.invalid:9987"
	pub, _ := genPair(t)
	first := mustTempIdentity(t)
	second := mustTempIdentity(t)
	initial := signedTestRecord(t, "peer", pub, first)
	replacement := signedTestRecord(t, "peer", pub, second)
	if _, err := cm.verifyPeerRecord("peer", initial, false); err != nil {
		t.Fatal(err)
	}
	cm.pubKeys.putAt("peer", pub, cm.pubKeys.epoch, initial)
	if _, err := cm.verifyPeerRecord("peer", replacement, false); !errors.Is(err, errDMIdentityChanged) {
		t.Fatal("identity replacement accepted", err)
	}
	restarted := newTestConnManager()
	restarted.id = cm.id
	restarted.addr = cm.addr
	if _, err := restarted.verifyPeerRecord("peer", initial, false); !errors.Is(err, errDMIdentityChanged) {
		t.Fatal("old identity not blocked while change pending", err)
	}
	prompted := false
	restarted.confirmPeerChange = func(server, uid, oldFP, newFP string) bool {
		prompted = true
		return server == cm.addr && uid == "peer" && oldFP != newFP && len(newFP) == 64
	}
	if _, err := restarted.verifyPeerRecord("peer", replacement, true); err != nil || !prompted {
		t.Fatal("explicit confirmation failed", err)
	}
	if cm.verifiedCachedPeer("peer", pub) {
		t.Fatal("cached old identity survived confirmation")
	}
	if _, err := cm.verifyPeerRecord("peer", initial, false); !errors.Is(err, errDMIdentityChanged) {
		t.Fatal("retired incoming identity accepted")
	}
	if _, err := cm.verifyPeerRecord("peer", replacement, false); err != nil {
		t.Fatal("old replay blocked confirmed identity", err)
	}
	rotated, _ := genPair(t)
	if _, err := cm.verifyPeerRecord("peer", signedTestRecord(t, "peer", rotated, second), false); err != nil {
		t.Fatal("same signer key rotation rejected", err)
	}
	path, _, _ := cm.peerPinPath("peer")
	if err := os.WriteFile(path, []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := cm.verifyPeerRecord("peer", replacement, false); err == nil {
		t.Fatal("corrupt trust store silently reset")
	}
}
func TestPeerTrustRejectsUnwritableStoreAndUnsignedRecords(t *testing.T) {
	cm := newTestConnManager()
	cm.id = mustTempIdentity(t)
	cm.addr = "trust-test.invalid:9987"
	key, _ := genPair(t)
	peer := mustTempIdentity(t)
	record := signedTestRecord(t, "peer", key, peer)
	path, _, _ := cm.peerPinPath("peer")
	if err := os.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := cm.verifyPeerRecord("peer", record, false); err == nil {
		t.Fatal("unreadable pin accepted")
	}
	if _, err := cm.verifyPeerRecord("other", record, false); err == nil {
		t.Fatal("UID substitution accepted")
	}
	_, signer, _ := ed25519.GenerateKey(rand.Reader)
	wrong, _ := e2ee.SignKeyRecord("self", key, signer)
	if _, err := cm.verifyMessageKey("self", wrong, "self", key); err == nil {
		t.Fatal("own identity signing substitution accepted")
	}
}

// Fixture records use a stable signer per account so encryption-key rotation
// exercises the existing trust binding rather than manufacturing an identity change.
func testPeerRecord(t *testing.T, uid string, key [32]byte) string {
	t.Helper()
	seed := sha256.Sum256([]byte("test identity:" + uid))
	signer := ed25519.NewKeyFromSeed(seed[:])
	record, err := e2ee.SignKeyRecord(uid, key, signer)
	if err != nil {
		t.Fatal(err)
	}
	return record
}
func cacheTestPeer(t *testing.T, cm *connManager, uid string, key [32]byte, signer ...*identity) string {
	t.Helper()
	if cm.id == nil {
		cm.id = mustTempIdentity(t)
	}
	if cm.addr == "" {
		cm.addr = "test.invalid:9987"
	}
	record := testPeerRecord(t, uid, key)
	if len(signer) > 0 {
		record = signedTestRecord(t, uid, key, signer[0])
	}
	if _, err := cm.verifyPeerRecord(uid, record, false); err != nil {
		t.Fatal(err)
	}
	cm.pubKeys.putAt(uid, key, cm.pubKeys.epoch, record)
	return record
}

func TestDelayedDirectoryRecordKeepsOriginalTrustNamespace(t *testing.T) {
	cm := newTestConnManager()
	cm.id = mustTempIdentity(t)
	cm.addr = "first.invalid:9987"
	path, server, err := cm.peerPinPath("peer")
	if err != nil {
		t.Fatal(err)
	}
	cm.addr = "second.invalid:9987"
	key, _ := genPair(t)
	record := signedTestRecord(t, "peer", key, mustTempIdentity(t))
	if _, err := cm.verifyPeerRecordAt("peer", record, false, path, server); err != nil {
		t.Fatal(err)
	}
	other, _, err := cm.peerPinPath("peer")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(other); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("delayed response changed the new server trust store", err)
	}
	pin, err := readDMPin(path)
	if err != nil || pin.Version != 1 {
		t.Fatal("original trust namespace lost", err)
	}
}
