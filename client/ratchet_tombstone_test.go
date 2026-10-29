package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// tombSetup creates two identities and returns the app, the first identity,
// its ID and its live store directory.
func tombSetup(t *testing.T) (*App, *identity, string, string) {
	t.Helper()
	old := identityRootOverride
	identityRootOverride = t.TempDir()
	t.Cleanup(func() { identityRootOverride = old })
	a := &App{tabs: map[string]*tabState{}, hotkeys: map[string]*hotkeyReg{}, settingsPath: t.TempDir() + "/settings.json"}
	for _, name := range []string{"one", "two"} {
		if msg := a.CreateIdentity(name); msg != "" {
			t.Fatal(msg)
		}
	}
	ids := a.ListIdentities()
	path, _ := identityPathFor(ids[0].ID)
	id, err := loadIdentityAtStrict(path)
	if err != nil {
		t.Fatal(err)
	}
	base, _ := defaultRatchetBase()
	return a, id, ids[0].ID, ratchetStoreDir(base, id)
}

func writeTomb(t *testing.T, dir, file, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func exists(p string) bool { _, err := os.Lstat(p); return err == nil }

func conflictDirs(t *testing.T, live string) []string {
	entries, _ := os.ReadDir(filepath.Dir(live))
	var out []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), filepath.Base(live)+ratchetConflict) {
			out = append(out, filepath.Join(filepath.Dir(live), e.Name()))
		}
	}
	return out
}

// Reviewer repro (fix3 P3-1): a failed rollback rename followed by a session
// restart created a fresh live dir; the sweep then erased the authoritative
// tombstone. Now the tombstone is restored and the fresh dir kept aside.
func TestRatchetSweepRestoresAuthoritativeTombstoneOverFreshDir(t *testing.T) {
	a, _, _, live := tombSetup(t)
	tomb := live + ratchetTombstone + "x"
	writeTomb(t, tomb, "old.db", "authoritative")
	writeTomb(t, live, "fresh.db", "fresh")
	a.sweepRatchetStores()
	if exists(tomb) {
		t.Fatal("tombstone must be resolved (restored), not left")
	}
	if b, err := os.ReadFile(filepath.Join(live, "old.db")); err != nil || string(b) != "authoritative" {
		t.Fatal("authoritative tombstone must become the live store", err)
	}
	aside := conflictDirs(t, live)
	if len(aside) != 1 || !exists(filepath.Join(aside[0], "fresh.db")) {
		t.Fatalf("the store created over it must be kept aside, got %v", aside)
	}
}

// startRatchet resolves its own identity's tombstone under the identity lock
// before opening (the open would otherwise create a fresh directory), so a
// start before the async startup sweep, or right after a lost rollback,
// cannot lose the store.
func TestRatchetStartResolvesOwnTombstoneFirst(t *testing.T) {
	_, id, _, live := tombSetup(t)
	tomb := live + ratchetTombstone + "x"
	writeTomb(t, tomb, "old.db", "authoritative")
	unlock := lockRatchetIdentity(id.PublicKey)
	err := resolveRatchetTombstones(filepath.Dir(live), ratchetStoreKey(id.PublicKey), ratchetLiveYes)
	unlock()
	if err != nil || !exists(filepath.Join(live, "old.db")) || exists(tomb) {
		t.Fatal("start-time resolution must restore the tombstone", err)
	}
	// The real start path calls it before OpenShared.
	src, _ := os.ReadFile("ratchet_session.go")
	i, j := strings.Index(string(src), "resolveRatchetTombstones(base"), strings.Index(string(src), "durable.OpenShared(storeDir")
	if i < 0 || j < 0 || i > j {
		t.Fatal("startRatchet must resolve tombstones before opening the store")
	}
}

// Liveness uses the public key alone: an identity whose protected key cannot
// be unsealed (moved profile) still counts as live, so its tombstone is
// restored rather than erased.
func TestRatchetSweepLivenessIgnoresUndecodableKeys(t *testing.T) {
	a, id, idName, live := tombSetup(t)
	path, _ := identityPathFor(idName)
	raw, _ := json.Marshal(map[string]string{"public_key": id.PublicKey, "private_key": dpapiPrefix + "!!not-base64!!"})
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadIdentityAtStrict(path); err == nil {
		t.Fatal("precondition: full decode must fail")
	}
	tomb := live + ratchetTombstone + "x"
	writeTomb(t, tomb, "a.db", "sealed")
	a.sweepRatchetStores()
	if !exists(filepath.Join(live, "a.db")) {
		t.Fatal("tombstone of a live (undecodable) identity must be restored")
	}
}

// An identity file that cannot be read makes liveness unknown: no
// uncommitted tombstone is erased (CodeRabbit), committed ones still are.
func TestRatchetSweepUnknownLivenessNeverErases(t *testing.T) {
	a, _, idName, live := tombSetup(t)
	base := filepath.Dir(live)
	orphan := filepath.Join(base, strings.Repeat("ab", 16)+ratchetTombstone+"y")
	committed := filepath.Join(base, strings.Repeat("cd", 16)+ratchetErasing+"z")
	writeTomb(t, orphan, "a.db", "maybe live")
	writeTomb(t, committed, "a.db", "deleted")
	path, _ := identityPathFor(idName)
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	a.sweepRatchetStores()
	if !exists(orphan) {
		t.Fatal("unknown liveness must never erase an uncommitted tombstone")
	}
	if exists(committed) {
		t.Fatal("a committed (.erasing-) tombstone is always erased")
	}
	// An unreadable identities directory is unknown too, not empty.
	dir, _ := identitiesDir()
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir, []byte("not a dir"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, complete := liveRatchetKeys(); complete {
		t.Fatal("unreadable identities dir must be incomplete")
	}
	a.sweepRatchetStores()
	if !exists(orphan) {
		t.Fatal("unreadable identities dir must never erase")
	}
	// Once liveness is known again (identities readable, key absent), the
	// orphan is erased.
	_ = os.Remove(dir)
	_ = os.MkdirAll(dir, 0o700)
	a.sweepRatchetStores()
	if exists(orphan) {
		t.Fatal("known-absent identity's tombstone must be erased")
	}
}

// A committed deletion's tombstone is never restored: re-importing the same
// key (or the sweep after it) gets a fresh store.
func TestRatchetCommittedTombstoneNeverResurrects(t *testing.T) {
	a, id, idName, live := tombSetup(t)
	writeTomb(t, live, "a.db", "store")
	if msg := a.DeleteIdentity(idName, true); msg != "" {
		t.Fatal(msg)
	}
	if exists(live) {
		t.Fatal("deleted identity's store must be gone")
	}
	// Erase failed after the commit point but the marker rename worked.
	committed := live + ratchetErasing + "x"
	writeTomb(t, committed, "a.db", "deleted")
	// Marker rename failed too: a plain tombstone of a gone identity.
	plain := live + ratchetTombstone + "y"
	writeTomb(t, plain, "a.db", "deleted")
	restoreRatchetIdentity(id.PublicKey)
	if err := a.finishRatchetDeletion(id.PublicKey); err != nil {
		t.Fatal(err)
	}
	if exists(plain) {
		t.Fatal("import must finish a committed deletion before storing the key again")
	}
	// Re-imported: the identity is live again; .erasing- is still erased.
	path, _ := identityPathFor(idName)
	if err := saveIdentityAt(path, id); err != nil {
		t.Fatal(err)
	}
	a.sweepRatchetStores()
	if exists(committed) || exists(filepath.Join(live, "a.db")) {
		t.Fatal("a committed deletion must never be restored for a re-imported key")
	}
}

// The wipe-retry sweep serialises with deletions and imports (identityMu).
func TestRatchetWipeRetryTakesIdentityMu(t *testing.T) {
	a, _, _, _ := tombSetup(t)
	a.identityMu.Lock()
	done := make(chan struct{})
	go func() { a.sweepRatchetStoresIn(a.ratchetBases()); close(done) }()
	select {
	case <-done:
		a.identityMu.Unlock()
		t.Fatal("sweep ran without identityMu")
	case <-time.After(80 * time.Millisecond):
	}
	a.identityMu.Unlock()
	<-done
	src, _ := os.ReadFile("ratchet_tombstone.go")
	if !strings.Contains(string(src), "a.sweepRatchetStoresIn(bases)") {
		t.Fatal("the wipe retry must use the identityMu sweep")
	}
}

// Reviewer repro (fix3 P3-2): a corrupt or 0-byte store file no longer makes
// the identity undeletable; deletion destroys it.
func TestRatchetCorruptStoreDoesNotBlockDeletion(t *testing.T) {
	for name, content := range map[string]string{"garbage": "garbage-garbage-garbage-garbage", "empty": ""} {
		t.Run(name, func(t *testing.T) {
			a, _, idName, live := tombSetup(t)
			writeTomb(t, live, "deadbeef.db", content)
			if msg := a.DeleteIdentity(idName, true); msg != "" {
				t.Fatalf("deletion must proceed over a broken store: %q", msg)
			}
			p, _ := identityPathFor(idName)
			if exists(p) || exists(live) {
				t.Fatal("identity and broken store must both be destroyed")
			}
		})
	}
}

// Adopted review repro (P3-1): a deleted key re-imported while another
// identity file is unreadable (liveness incomplete) never gets its deleted
// store back.
func TestRatchetReimportNeverResurrectsWhenLivenessIncomplete(t *testing.T) {
	a, id, idName, live := tombSetup(t)
	writeTomb(t, live, "a.db", "store")
	if msg := a.DeleteIdentity(idName, true); msg != "" {
		t.Fatal(msg)
	}
	plain := live + ratchetTombstone + "y"
	writeTomb(t, plain, "a.db", "deleted-secret")
	committed := live + ratchetErasing + "z"
	writeTomb(t, committed, "a.db", "deleted")
	writeTomb(t, live, "a.db", "reopened-deleted") // a deleted store another process reopened
	dir, _ := identitiesDir()
	if err := os.WriteFile(filepath.Join(dir, "broken.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Liveness is unknown (another identity file is unreadable): the import
	// is refused, never resurrecting or erasing anything.
	if msg := a.adoptImportedIdentity(id, "back.json"); !strings.Contains(msg, "could not be read") {
		t.Fatalf("import with unknown liveness must be refused: %q", msg)
	}
	if !exists(plain) {
		t.Fatal("a refused import must not erase the tombstone")
	}
	if err := os.Remove(filepath.Join(dir, "broken.json")); err != nil {
		t.Fatal(err)
	}
	if msg := a.adoptImportedIdentity(id, "back.json"); msg != "" {
		t.Fatal(msg)
	}
	if exists(plain) || exists(committed) || exists(live) {
		t.Fatal("import must finish the deleted key's tombstones, markers and reopened store")
	}
	unlock := lockRatchetIdentity(id.PublicKey)
	_ = resolveRatchetTombstones(filepath.Dir(live), ratchetStoreKey(id.PublicKey), ratchetLiveYes)
	unlock()
	if b, e := os.ReadFile(filepath.Join(live, "a.db")); e == nil {
		t.Fatalf("RESURRECTED deleted identity's store on re-import: %q", b)
	}
}

// Adopted review repro (P3-2): kept-aside stores are destroyed with the identity.
func TestRatchetConflictDirErasedOnDeletion(t *testing.T) {
	a, _, idName, live := tombSetup(t)
	writeTomb(t, live+ratchetTombstone+"x", "old.db", "authoritative")
	writeTomb(t, live, "fresh.db", "fresh-secret")
	a.sweepRatchetStores()
	if len(conflictDirs(t, live)) != 1 {
		t.Fatal("precondition")
	}
	if msg := a.DeleteIdentity(idName, true); msg != "" {
		t.Fatal(msg)
	}
	if left := conflictDirs(t, live); len(left) > 0 {
		t.Fatalf("store data of the deleted identity survives deletion: %v", left)
	}
	// A conflict dir of a known-absent key left by an older build is swept.
	orphan := filepath.Join(filepath.Dir(live), strings.Repeat("ef", 16)+ratchetConflict+"q")
	writeTomb(t, orphan, "a.db", "x")
	a.sweepRatchetStores()
	if exists(orphan) {
		t.Fatal("kept-aside store of an absent identity must be swept")
	}
}

// Review P3-3: a second process restoring the tombstone during a deletion.
func TestRatchetDeletionDetectsReopenedStore(t *testing.T) {
	t.Run("before the commit: rolls back, identity kept", func(t *testing.T) {
		a, _, idName, live := tombSetup(t)
		writeTomb(t, live, "a.db", "store")
		old := ratchetDeletionHook
		t.Cleanup(func() { ratchetDeletionHook = old })
		ratchetDeletionHook = func(stage string, moved [][2]string) {
			if stage == "before-commit" {
				for _, mv := range moved {
					_ = os.Rename(mv[1], mv[0]) // the other process restores it
				}
			}
		}
		if msg := a.DeleteIdentity(idName, true); !strings.Contains(msg, "reopened") {
			t.Fatalf("deletion must fail: %q", msg)
		}
		p, _ := identityPathFor(idName)
		if !exists(p) || !exists(filepath.Join(live, "a.db")) {
			t.Fatal("identity and store must survive a refused deletion")
		}
	})
	t.Run("after the commit: the store is taken again and erased", func(t *testing.T) {
		a, _, idName, live := tombSetup(t)
		writeTomb(t, live, "a.db", "store")
		old := ratchetDeletionHook
		t.Cleanup(func() { ratchetDeletionHook = old })
		ratchetDeletionHook = func(stage string, moved [][2]string) {
			if stage == "after-commit" {
				for _, mv := range moved {
					_ = os.Rename(mv[1], mv[0])
				}
			}
		}
		if msg := a.DeleteIdentity(idName, true); msg != "" {
			t.Fatal(msg)
		}
		if exists(live) {
			t.Fatal("a store reopened after the commit must still be erased")
		}
	})
}

// Adopted fix-review repro (P3-3): while the key's own identity file is
// unreadable, an authoritative .deleted- tombstone is neither erased nor
// resurrected: the import is refused with an explicit reason.
func TestRatchetImportRefusedWhenOwnFileUnreadable(t *testing.T) {
	a, id, idName, live := tombSetup(t)
	tomb := live + ratchetTombstone + "crash"
	writeTomb(t, tomb, "a.db", "authoritative-history")
	path, _ := identityPathFor(idName)
	if err := os.WriteFile(path, []byte("{damaged"), 0o600); err != nil {
		t.Fatal(err)
	}
	msg := a.adoptImportedIdentity(id, "backup.json")
	if !strings.Contains(msg, "could not be read") {
		t.Fatalf("import must be refused explicitly, got %q", msg)
	}
	if !exists(tomb) || exists(erasingName(tomb)) {
		t.Fatal("DATA LOSS: authoritative tombstone erased or marked by import")
	}
}

// Fix-review P3-4: the committed-deletion notice is a success-with-notice in
// the UI (security-settings.js isDeletionNotice matches this prefix).
func TestDeletionNoticeContractWithUI(t *testing.T) {
	const prefix = "the identity was deleted, but "
	if !strings.HasPrefix(errRatchetErasedLater.Error(), prefix) {
		t.Fatal("notice text changed")
	}
	src, err := os.ReadFile("frontend/src/security-settings.js")
	if err != nil || !strings.Contains(string(src), `startsWith("`+prefix+`")`) {
		t.Fatal("the settings UI must treat the notice as success", err)
	}
}

// Fix-review P3-6: with unknown liveness the sweep never erases a live store
// next to an .erasing- marker (nor the marker); once the key is known gone it
// erases both.
func TestRatchetSweepKeepsLiveDirNextToMarkerWhenLivenessUnknown(t *testing.T) {
	a, _, _, live := tombSetup(t)
	base := filepath.Dir(live)
	key := strings.Repeat("ab", 16)
	liveOther := filepath.Join(base, key)
	marker := liveOther + ratchetErasing + "m"
	writeTomb(t, liveOther, "a.db", "maybe live")
	writeTomb(t, marker, "x", "")
	dir, _ := identitiesDir()
	if err := os.WriteFile(filepath.Join(dir, "broken.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	a.sweepRatchetStores()
	if !exists(filepath.Join(liveOther, "a.db")) || !exists(marker) {
		t.Fatal("unknown liveness must never erase")
	}
	_ = os.Remove(filepath.Join(dir, "broken.json"))
	a.sweepRatchetStores()
	if exists(liveOther) || exists(marker) {
		t.Fatal("a reopened store of a known-gone key must be erased with its marker")
	}
}

// Adopted fix2-review repro (P3-1a): a REFUSED import has no destructive side
// effect: the kept-aside .conflict- store (sorted before .deleted-) survives.
func TestRatchetRefusedImportKeepsKeptAsideStore(t *testing.T) {
	a, id, idName, live := tombSetup(t)
	tomb := live + ratchetTombstone + "crash"
	writeTomb(t, tomb, "a.db", "authoritative-history")
	aside := live + ratchetConflict + "k"
	writeTomb(t, aside, "b.db", "kept-aside-history")
	path, _ := identityPathFor(idName)
	if err := os.WriteFile(path, []byte("{damaged"), 0o600); err != nil {
		t.Fatal(err)
	}
	msg := a.adoptImportedIdentity(id, "backup.json")
	if !strings.Contains(msg, "could not be read") {
		t.Fatalf("expected refusal, got %q", msg)
	}
	if !exists(tomb) {
		t.Fatal("tombstone erased")
	}
	if !exists(aside) {
		t.Fatal("DATA LOSS: refused import erased the kept-aside .conflict- store under unknown liveness")
	}
}

// Fix2-review P3-1: with unknown liveness an import erases nothing at all; a
// live directory next to a committed marker refuses (it would be reopened as
// the imported key's store), and inert leftovers are kept for later.
func TestRatchetImportUnknownLivenessErasesNothing(t *testing.T) {
	t.Run("live dir next to .erasing- refuses", func(t *testing.T) {
		a, id, idName, live := tombSetup(t)
		if msg := a.DeleteIdentity(idName, true); msg != "" {
			t.Fatal(msg)
		}
		marker := live + ratchetErasing + "z"
		writeTomb(t, marker, "a.db", "deleted")
		writeTomb(t, live, "a.db", "reopened-deleted")
		dir, _ := identitiesDir()
		if err := os.WriteFile(filepath.Join(dir, "broken.json"), []byte("{x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if msg := a.adoptImportedIdentity(id, "b.json"); !strings.Contains(msg, "could not be read") {
			t.Fatalf("expected refusal, got %q", msg)
		}
		if !exists(marker) || !exists(filepath.Join(live, "a.db")) {
			t.Fatal("unknown liveness must not erase the marker or the directory next to it")
		}
	})
	t.Run("inert leftovers are kept, import proceeds", func(t *testing.T) {
		a, id, idName, live := tombSetup(t)
		if msg := a.DeleteIdentity(idName, true); msg != "" {
			t.Fatal(msg)
		}
		aside := live + ratchetConflict + "q"
		writeTomb(t, aside, "a.db", "kept")
		marker := live + ratchetErasing + "m"
		writeTomb(t, marker, "a.db", "deleted")
		dir, _ := identitiesDir()
		if err := os.WriteFile(filepath.Join(dir, "broken.json"), []byte("{x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if msg := a.adoptImportedIdentity(id, "b.json"); msg != "" {
			t.Fatal(msg)
		}
		if !exists(aside) || !exists(marker) {
			t.Fatal("unknown liveness must never erase leftovers")
		}
		if exists(live) {
			t.Fatal("no leftover may become the imported key's store")
		}
	})
}

// CodeRabbit b1: a failed tombstone erase during an import is detected by
// eraseCommittedTombstone's result (the tombstone was already renamed to
// .erasing-, so its old path is gone either way): the import is refused.
func TestRatchetImportRefusesWhenTombstoneEraseFails(t *testing.T) {
	a, id, idName, live := tombSetup(t)
	if msg := a.DeleteIdentity(idName, true); msg != "" {
		t.Fatal(msg)
	}
	writeTomb(t, live+ratchetTombstone+"held", "a.db", "deleted")
	old := ratchetEraseDir
	t.Cleanup(func() { ratchetEraseDir = old })
	ratchetEraseDir = func(string) error { return errors.New("erase failed") }
	if msg := a.adoptImportedIdentity(id, "b.json"); !strings.Contains(msg, "still being finished") {
		t.Fatalf("a failed erase must refuse the import, got %q", msg)
	}
}
