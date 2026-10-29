package main

import (
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"noxa/internal/auth"
)

const localTestServer = "noxa.example:7700"
const localTestFingerprint = "aa11"

// localDataFixture is one identity's local data in every per-identity store.
type localDataFixture struct {
	dmLogs []string
	files  []string // outbox, saved messages, tab layout, contact pin
}

func (f localDataFixture) all() []string { return append(append([]string{}, f.dmLogs...), f.files...) }

// writeIdentityLocalData writes a DM log (with a noxa-olm-v1 projection and a
// legacy entry) and the per-server files of id, named exactly as their stores
// name them.
func writeIdentityLocalData(t *testing.T, a *App, id *identity, peer string) localDataFixture {
	t.Helper()
	root := filepath.Dir(a.settingsFile())
	store, err := dmHistoryStoreForIdentity(filepath.Join(root, "dmhistory"), id)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range []DMEntry{
		{FromUniqueID: peer, Body: "legacy history"},
		{FromUniqueID: peer, Body: "olm plaintext", Protocol: "noxa-olm-v1", ClientMsgID: "m1", Status: "received"},
	} {
		if msg := a.dmHistoryAppendWith(store, peer, "Peer", e); msg != "" {
			t.Fatal(msg)
		}
	}
	pub, _, err := id.x25519()
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := canonicalServerDestination(localTestServer)
	if err != nil {
		t.Fatal(err)
	}
	owner := localServerOwner(pub, canonical, localTestFingerprint)
	layout := chatTabLayoutName(pub, canonical, localTestFingerprint)
	local, err := auth.LoadPublicKey(id.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	pin, err := dmPinPathFor(local, canonical, peer)
	if err != nil {
		t.Fatal(err)
	}
	files := []string{
		filepath.Join(root, "outbox", hex.EncodeToString(owner[:])+".sealed"),
		filepath.Join(root, "saved-messages", hex.EncodeToString(owner[:])+".json"),
		filepath.Join(root, "chat-tab-layout", hex.EncodeToString(layout[:])+".sealed"),
		pin,
	}
	for _, f := range files {
		writeTomb(t, filepath.Dir(f), filepath.Base(f), "data of "+id.Name)
	}
	return localDataFixture{dmLogs: []string{store.path(peer)}, files: files}
}

func localDataSetup(t *testing.T) (*App, *identity, string, *identity, localDataFixture, localDataFixture) {
	t.Helper()
	a, id, idName, _ := tombSetup(t)
	a.knownServers = &knownServers{Servers: map[string]string{localTestServer: localTestFingerprint}}
	ids := a.ListIdentities()
	otherPath, _ := identityPathFor(ids[1].ID)
	other, err := loadIdentityAtStrict(otherPath)
	if err != nil {
		t.Fatal(err)
	}
	mine := writeIdentityLocalData(t, a, id, "peer-a")
	theirs := writeIdentityLocalData(t, a, other, "peer-a")
	return a, id, idName, other, mine, theirs
}

func requireGone(t *testing.T, paths []string, what string) {
	t.Helper()
	for _, p := range paths {
		if exists(p) {
			t.Fatalf("%s must be erased: %s", what, p)
		}
	}
}

func requireKept(t *testing.T, paths []string, what string) {
	t.Helper()
	for _, p := range paths {
		if !exists(p) {
			t.Fatalf("%s must be kept: %s", what, p)
		}
	}
}

func dmHistoryOf(t *testing.T, a *App, id *identity) []DMPeer {
	t.Helper()
	store, err := dmHistoryStoreForIdentity(filepath.Join(filepath.Dir(a.settingsFile()), "dmhistory"), id)
	if err != nil {
		t.Fatal(err)
	}
	return store.peers()
}

// P2-3: identity deletion erases the identity's whole DM history (its
// noxa-olm-v1 projection and legacy history), its outbox, saved messages,
// pinned conversations and contact pins, and nothing of another identity;
// importing the exported key again brings none of it back.
func TestDeleteIdentityErasesLocalDataAndReimportStaysEmpty(t *testing.T) {
	a, id, idName, other, mine, theirs := localDataSetup(t)
	if len(dmHistoryOf(t, a, id)) != 1 {
		t.Fatal("precondition: the identity has DM history")
	}
	if msg := a.DeleteIdentity(idName, true); msg != "" {
		t.Fatal(msg)
	}
	requireGone(t, mine.all(), "deleted identity's local data")
	requireKept(t, theirs.all(), "another identity's local data")
	if tombs := localTombs(a.identityLocalBase(), ""); len(tombs) != 0 {
		t.Fatalf("no tombstone may be left: %v", tombs)
	}
	// Writers of the deleted identity are fenced (a live tab, a late projection).
	store, _ := dmHistoryStoreForIdentity(filepath.Join(filepath.Dir(a.settingsFile()), "dmhistory"), id)
	if msg := a.dmHistoryAppendWith(store, "peer-a", "", DMEntry{Body: "late"}); msg != errIdentityDeleted.Error() {
		t.Fatalf("a deleted identity's DM history must refuse writes, got %q", msg)
	}
	// Re-import the exported key.
	if msg := a.adoptImportedIdentity(id, "one.json"); msg != "" {
		t.Fatal(msg)
	}
	if peers := dmHistoryOf(t, a, id); len(peers) != 0 {
		t.Fatalf("re-import must not resurrect DM history: %+v", peers)
	}
	requireGone(t, mine.all(), "re-imported identity's old local data")
	if len(dmHistoryOf(t, a, other)) != 1 {
		t.Fatal("another identity's DM history must survive")
	}
	if msg := a.dmHistoryAppendWith(store, "peer-a", "", DMEntry{Body: "fresh"}); msg != "" {
		t.Fatalf("the re-imported identity starts a fresh history: %q", msg)
	}
}

// Switching identity or disconnecting (the desktop sign-out) keeps every
// identity's local data.
func TestSignOutKeepsIdentityLocalData(t *testing.T) {
	a, _, _, _, mine, theirs := localDataSetup(t)
	ids := a.ListIdentities()
	if msg := a.SwitchIdentity(ids[1].ID); msg != "" {
		t.Fatal(msg)
	}
	a.Disconnect()
	a.sweepRatchetStores()
	requireKept(t, mine.all(), "a signed-out identity's local data")
	requireKept(t, theirs.all(), "the active identity's local data")
}

// A deletion refused before the commit point moves the local data back.
func TestDeleteIdentityRollbackRestoresLocalData(t *testing.T) {
	a, _, idName, _, mine, _ := localDataSetup(t)
	old := ratchetDeletionHook
	t.Cleanup(func() { ratchetDeletionHook = old })
	ratchetDeletionHook = func(stage string, moved [][2]string) {
		if stage == "before-commit" {
			// Another process restores the local-data tombstone.
			for _, tomb := range localTombs(a.identityLocalBase(), "") {
				_ = a.restoreLocalTomb(tomb.path)
			}
		}
	}
	if msg := a.DeleteIdentity(idName, true); !strings.Contains(msg, "restored by another") {
		t.Fatalf("deletion must be refused: %q", msg)
	}
	requireKept(t, mine.all(), "local data of an identity whose deletion was refused")
}

// Crash resume: an uncommitted tombstone of a live identity is restored; one
// of a gone identity (crash after the commit point) is erased by the sweep or
// by a re-import; a committed (.erasing-) one is never restored.
func TestIdentityLocalDataDeletionResumesAfterCrash(t *testing.T) {
	t.Run("crash before the commit: restored", func(t *testing.T) {
		a, id, _, _, mine, _ := localDataSetup(t)
		ratchetDeleted.Store(id.PublicKey, true)
		tomb, err := a.tombstoneIdentityLocalData(id)
		restoreRatchetIdentity(id.PublicKey)
		if err != nil || tomb == "" {
			t.Fatal("tombstone", err)
		}
		requireGone(t, mine.all(), "moved local data")
		a.sweepRatchetStores() // next start: the identity file still exists
		requireKept(t, mine.all(), "local data of a surviving identity")
		if exists(tomb) {
			t.Fatal("restored tombstone must be removed")
		}
	})
	t.Run("crash after the commit: erased at start", func(t *testing.T) {
		a, id, idName, _, mine, theirs := localDataSetup(t)
		tomb, err := a.tombstoneIdentityLocalData(id)
		if err != nil || tomb == "" {
			t.Fatal("tombstone", err)
		}
		path, _ := identityPathFor(idName)
		if err := os.Remove(path); err != nil { // the commit point
			t.Fatal(err)
		}
		a.sweepRatchetStores()
		if exists(tomb) || len(localTombs(a.identityLocalBase(), "")) != 0 {
			t.Fatal("a committed deletion's local data must be erased")
		}
		requireGone(t, mine.all(), "deleted local data")
		requireKept(t, theirs.all(), "another identity's local data")
	})
	t.Run("crash after the commit: a re-import finishes it first", func(t *testing.T) {
		a, id, idName, _, mine, _ := localDataSetup(t)
		tomb, err := a.tombstoneIdentityLocalData(id)
		if err != nil || tomb == "" {
			t.Fatal("tombstone", err)
		}
		path, _ := identityPathFor(idName)
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if msg := a.adoptImportedIdentity(id, "one.json"); msg != "" {
			t.Fatal(msg)
		}
		if exists(tomb) || len(dmHistoryOf(t, a, id)) != 0 {
			t.Fatal("a re-import must erase, never restore, a committed deletion's local data")
		}
		requireGone(t, mine.all(), "deleted local data")
	})
	t.Run("erase failure after the commit: retried, never restored", func(t *testing.T) {
		a, id, idName, _, mine, _ := localDataSetup(t)
		oldErase, oldDelay := ratchetEraseDir, ratchetWipeRetryDelay
		t.Cleanup(func() { ratchetEraseDir, ratchetWipeRetryDelay = oldErase, oldDelay })
		ratchetWipeRetryDelay = 1 << 40 // the test drives the retry
		ratchetEraseDir = func(string) error { return errors.New("busy") }
		if msg := a.DeleteIdentity(idName, true); msg != "" {
			t.Fatal(msg)
		}
		tombs := localTombs(a.identityLocalBase(), ratchetStoreKey(id.PublicKey))
		if len(tombs) != 1 || tombs[0].marker != ratchetErasing {
			t.Fatalf("a committed tombstone must be marked .erasing-: %+v", tombs)
		}
		ratchetEraseDir = oldErase
		// The key is imported again before the retry ran.
		if msg := a.adoptImportedIdentity(id, "one.json"); msg != "" {
			t.Fatal(msg)
		}
		a.sweepRatchetStores()
		requireGone(t, mine.all(), "deleted local data")
		if len(localTombs(a.identityLocalBase(), "")) != 0 || len(dmHistoryOf(t, a, id)) != 0 {
			t.Fatal("a committed deletion must never come back")
		}
	})
}

// With incomplete liveness an uncommitted local tombstone could be
// authoritative: an import neither erases nor restores it.
func TestIdentityLocalImportRefusedWhenLivenessUnknown(t *testing.T) {
	a, id, idName, _, _, _ := localDataSetup(t)
	tomb, err := a.tombstoneIdentityLocalData(id)
	if err != nil || tomb == "" {
		t.Fatal("tombstone", err)
	}
	path, _ := identityPathFor(idName)
	_ = os.Remove(path)
	otherPath := ""
	for _, e := range a.ListIdentities() {
		if p, _ := identityPathFor(e.ID); p != path {
			otherPath = p
		}
	}
	if err := os.WriteFile(otherPath, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := a.finishIdentityLocalDeletion(id.PublicKey, false); err == nil {
		t.Fatal("import must be refused while liveness is unknown")
	}
	if !exists(tomb) {
		t.Fatal("the tombstone must be kept")
	}
}
