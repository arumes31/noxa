// ratchet_tombstone.go: resumable destruction of an identity's noxa-olm-v1
// stores (identity deletion only; a sign-out or disconnect keeps them).
//
// Tombstone states, per store directory <key> under a base:
//
//	<key>.deleted-<id>   renamed for a deletion that has NOT been proven
//	                     committed. Authoritative while the identity exists
//	                     (crash or failed rollback before the commit point):
//	                     restored, never erased. Erased only when the identity
//	                     is known to be gone.
//	<key>.erasing-<id>   the deletion committed (identity file removed): always
//	                     erased, never restored, so a later re-import of the
//	                     same key gets a fresh store.
//	<key>.conflict-<id>  a live directory found next to an authoritative
//	                     tombstone (created by an older build or a start that
//	                     raced a crash): kept aside, never erased automatically.
//
// Every resolution of one key runs under that key's store lock, so it cannot
// race a store open (startRatchet resolves its own key before opening) or a
// deletion. Liveness is decided from identity files' public keys only (no
// decode, no DPAPI unseal); an unreadable identities directory or file means
// "unknown", and unknown never erases an uncommitted tombstone.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"noxa/internal/e2ee/durable"
)

const (
	ratchetTombstone = ".deleted-"
	ratchetErasing   = ".erasing-"
	ratchetConflict  = ".conflict-"
)

// ratchetLiveness is what is known about whether a store key's identity exists.
type ratchetLiveness int

const (
	ratchetLiveUnknown ratchetLiveness = iota
	ratchetLiveNo
	ratchetLiveYes
)

// destroyRatchetNamespaces destroys an identity's stores without removing its
// identity file (tests; DeleteIdentity uses deleteIdentityWithStores).
func (a *App) destroyRatchetNamespaces(id *identity) error {
	return a.deleteIdentityWithStores(id, "")
}

// deleteIdentityWithStores runs on IDENTITY DELETION only, with a.identityMu
// held by the caller in production. Steps, serialised with every store open
// of this identity:
//
//  1. resolve leftover tombstones, then fence new sessions and stop + await
//     this process's live sessions;
//  2. lock every store against any other opener (durable.LockForDestroy,
//     held across the renames on POSIX); a held store fails the deletion
//     with nothing changed;
//  3. rename every store directory to a .deleted- tombstone;
//  4. remove the identity file: the commit point;
//  5. mark the tombstones .erasing- and erase them (the sweep retries).
//
// A failure before the commit point rolls back (renames back, fence lifted,
// sessions restarted; a restart first restores any tombstone the rollback
// could not). A crash before it leaves .deleted- tombstones whose identity
// still exists: they are restored before the next open. A crash after it
// leaves tombstones whose identity is gone: they are erased.
func (a *App) deleteIdentityWithStores(id *identity, identityPath string) error {
	bases := a.ratchetBases()
	sweepRatchetTombstones(bases)
	a.sweepIdentityLocalTombs()
	unlock := lockRatchetIdentity(id.PublicKey)
	defer unlock()
	ratchetDeleted.Store(id.PublicKey, true)
	a.tabsMu.Lock()
	managers := make([]*connManager, 0, len(a.tabs))
	for _, tab := range a.tabs {
		if tab != nil && tab.cm != nil {
			managers = append(managers, tab.cm)
		}
	}
	a.tabsMu.Unlock()
	var stopped []*connManager
	for _, cm := range managers {
		cm.mu.Lock()
		var s *ratchetSession
		if cm.id != nil && cm.id.PublicKey == id.PublicKey {
			s = cm.takeRatchetLocked()
		}
		cm.mu.Unlock()
		if s != nil {
			s.stop()
			stopped = append(stopped, cm)
		}
	}
	var moved []move
	localTomb := "" // the identity's local data (identity_local_data.go)
	rollback := func(cause error) error {
		var lost []string
		for _, mv := range moved {
			if err := os.Rename(mv.tomb, mv.dir); err != nil {
				lost = append(lost, mv.tomb)
			}
		}
		// Moved back before the write fence is lifted; a failure leaves the
		// .deleted- tombstone, which the next sweep restores (identity live).
		if localTomb != "" {
			if err := a.restoreLocalTomb(localTomb); err != nil {
				lost = append(lost, localTomb)
			}
		}
		restoreRatchetIdentity(id.PublicKey) // the identity survives
		// Safe even when a rename back failed: a start restores the
		// tombstone under the identity lock before opening, and fails
		// (recorded, retried) rather than create a fresh store over it.
		for _, cm := range stopped {
			cm.mu.Lock()
			conn := cm.conn
			cm.mu.Unlock()
			go guardCrash("ratchet restart", func() { cm.startRatchetRecorded(conn) })
		}
		if len(lost) > 0 {
			return fmt.Errorf("%w; the store could not be restored yet and is restored before secure messaging starts again", cause)
		}
		return cause
	}
	// Our own sessions are stopped; any remaining lock belongs to another
	// opener (another process): fail before anything is renamed. On POSIX the
	// exclusive store locks are HELD across the renames (no other process can
	// open a store between check and rename); on Windows an open handle makes
	// the rename itself fail.
	var releases []func()
	defer func() {
		for _, release := range releases {
			release()
		}
	}()
	for base := range bases {
		release, err := durable.LockForDestroy(ratchetStoreDir(base, id))
		if err != nil {
			if errors.Is(err, durable.ErrInUse) {
				return rollback(fmt.Errorf("secure message store of this identity is in use by another noxa window or process: %w", err))
			}
			return rollback(fmt.Errorf("secure message store of this identity could not be checked: %w", err))
		}
		releases = append(releases, release)
	}
	for base := range bases {
		dir := ratchetStoreDir(base, id)
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			continue
		}
		tomb := dir + ratchetTombstone + newClientMsgID()
		if err := os.Rename(dir, tomb); err != nil {
			return rollback(fmt.Errorf("secure message store of this identity could not be moved for deletion: %w", err))
		}
		moved = append(moved, move{dir, tomb})
	}
	// The identity's DM history, outbox and other local data join the same
	// commit point (their writers are fenced by ratchetDeleted).
	var localErr error
	if localTomb, localErr = a.tombstoneIdentityLocalData(id); localErr != nil {
		return rollback(fmt.Errorf("local data of this identity could not be moved for deletion: %w", localErr))
	}
	// Another noxa process may have restored a tombstone meanwhile (the
	// identity locks are per process): never commit over that.
	ratchetDeletionHook("before-commit", movedPairs(moved))
	for _, mv := range moved {
		if _, err := os.Lstat(mv.tomb); err != nil {
			return rollback(errors.New("secure message store of this identity was reopened by another noxa window or process; close it and try again"))
		}
	}
	if localTomb != "" {
		if _, err := os.Lstat(localTomb); err != nil {
			return rollback(errors.New("local data of this identity was restored by another noxa window or process; close it and try again"))
		}
	}
	if identityPath != "" {
		if err := os.Remove(identityPath); err != nil {
			return rollback(err)
		}
	}
	ratchetDeletionHook("after-commit", movedPairs(moved))
	retry := false
	var reopened error
	for _, mv := range moved {
		if _, err := os.Lstat(mv.tomb); err != nil {
			// Restored between the check and the commit: take it again.
			if err := os.Rename(mv.dir, mv.tomb); err != nil {
				// Still open elsewhere: leave a committed marker so the sweep
				// erases the store once it can (never restored).
				_ = os.MkdirAll(erasingName(mv.tomb), 0o700)
				reopened = errRatchetErasedLater
				retry = true
				continue
			}
		}
		if !eraseCommittedTombstone(mv.tomb) {
			retry = true
		}
	}
	if localTomb != "" && !eraseLocalTomb(localTomb) {
		retry = true
	}
	// Stores kept aside (.conflict-) belong to this identity too.
	for base := range bases {
		if !eraseRatchetConflicts(base, ratchetStoreKey(id.PublicKey)) {
			retry = true
		}
	}
	if retry {
		delay := ratchetWipeRetryDelay // read once, before the goroutine (tests change it)
		go guardCrash("ratchet wipe retry", func() {
			time.Sleep(delay)
			a.sweepRatchetStoresIn(bases)
		})
	}
	return reopened
}

// errRatchetErasedLater: the deletion COMMITTED (identity gone) but another
// process still holds the store; it is erased by a later sweep.
var errRatchetErasedLater = errors.New("the identity was deleted, but another noxa window or process still has its secure message store open; it is erased once that window closes")

// erasingName is the committed-deletion name of a .deleted- tombstone.
func erasingName(tomb string) string {
	base := filepath.Base(tomb)
	if i := strings.LastIndex(base, ratchetTombstone); i > 0 {
		return filepath.Join(filepath.Dir(tomb), base[:i]+ratchetErasing+base[i+len(ratchetTombstone):])
	}
	return tomb + ratchetErasing + "x"
}

// eraseRatchetConflicts erases the .conflict- stores of a deleted key; it
// reports whether none is left.
func eraseRatchetConflicts(base, key string) bool {
	entries, err := os.ReadDir(base)
	if err != nil {
		return os.IsNotExist(err)
	}
	ok := true
	for _, e := range entries {
		if k, marker, isTomb := splitRatchetTombstone(e); isTomb && k == key && marker == ratchetConflict {
			if err := os.RemoveAll(filepath.Join(base, e.Name())); err != nil {
				log.Printf("ratchet: erasing a deleted identity's kept-aside store failed: %v", err)
				ok = false
			}
		}
	}
	return ok
}

var ratchetWipeRetryDelay = 30 * time.Second

// ratchetDeletionHook lets tests act as a second process at the commit point.
var ratchetDeletionHook = func(stage string, moved [][2]string) {}

type move struct{ dir, tomb string }

func movedPairs(moved []move) [][2]string {
	out := make([][2]string, len(moved))
	for i, mv := range moved {
		out[i] = [2]string{mv.dir, mv.tomb}
	}
	return out
}

// ratchetEraseDir erases a committed tombstone (a test seam).
var ratchetEraseDir = os.RemoveAll

// eraseCommittedTombstone marks a tombstone of a committed deletion .erasing-
// (so it can never be restored, even if the key is imported again) and erases
// it. It reports whether nothing is left.
func eraseCommittedTombstone(tomb string) bool {
	path := tomb
	if strings.Contains(filepath.Base(tomb), ratchetTombstone) {
		erasing := erasingName(tomb)
		if err := os.Rename(tomb, erasing); err == nil {
			path = erasing
		} else {
			log.Printf("ratchet: marking a deleted store committed failed: %v", err)
		}
	}
	if err := ratchetEraseDir(path); err != nil {
		log.Printf("ratchet: erasing a deleted store failed, retried later: %v", err)
		return false
	}
	return true
}

// ratchetBases lists the store bases in use (production + test overrides).
func (a *App) ratchetBases() map[string]bool {
	bases := map[string]bool{}
	if base, err := defaultRatchetBase(); err == nil {
		bases[base] = true
	}
	a.tabsMu.Lock()
	for _, tab := range a.tabs {
		if tab != nil && tab.cm != nil {
			tab.cm.mu.Lock()
			if cfg := tab.cm.ratchetCfg; cfg != nil && cfg.Dir != "" {
				bases[cfg.Dir] = true
			}
			tab.cm.mu.Unlock()
		}
	}
	a.tabsMu.Unlock()
	return bases
}

// liveRatchetKeys returns the store keys of the stored identities, read from
// the public key field alone. complete is false when the directory or any
// identity file could not be read: absence is then unknown.
func liveRatchetKeys() (keys map[string]bool, complete bool) {
	keys = map[string]bool{}
	dir, err := identitiesDir()
	if err != nil {
		return keys, false
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return keys, false
	}
	complete = true
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		if validateIdentityID(strings.TrimSuffix(e.Name(), ".json")) != nil {
			continue
		}
		raw, err := readIdentityAt(filepath.Join(dir, e.Name()))
		var pub struct {
			PublicKey string `json:"public_key"`
		}
		if err != nil || json.Unmarshal(raw, &pub) != nil || pub.PublicKey == "" {
			complete = false
			continue
		}
		keys[ratchetStoreKey(pub.PublicKey)] = true
	}
	return keys, complete
}

// sweepRatchetTombstones finishes interrupted deletions in every base, each
// key under its store lock.
func sweepRatchetTombstones(bases map[string]bool) {
	live, complete := liveRatchetKeys()
	seen := map[string]bool{}
	for base := range bases {
		entries, err := os.ReadDir(base)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if key, _, ok := splitRatchetTombstone(e); ok && !seen[key] {
				seen[key] = true
			}
		}
	}
	for key := range seen {
		state := ratchetLiveUnknown
		switch {
		case live[key]:
			state = ratchetLiveYes
		case complete:
			state = ratchetLiveNo
		}
		unlock := lockRatchetStore(key)
		for base := range bases {
			if err := resolveRatchetTombstones(base, key, state); err != nil {
				log.Printf("ratchet: restoring an interrupted deletion failed: %v", err)
			}
		}
		unlock()
	}
}

// splitRatchetTombstone parses a tombstone entry name into key and marker.
func splitRatchetTombstone(e os.DirEntry) (key, marker string, ok bool) {
	if !e.IsDir() { // symlinks and files are never followed or touched
		return "", "", false
	}
	name := e.Name()
	for _, m := range []string{ratchetTombstone, ratchetErasing, ratchetConflict} {
		if i := strings.Index(name, m); i > 0 {
			return name[:i], m, true
		}
	}
	return "", "", false
}

// resolveRatchetTombstones resolves one key's tombstones in base. The caller
// holds lockRatchetStore(key). An error means an authoritative tombstone of a
// live identity is still unresolved: the store must not be opened.
func resolveRatchetTombstones(base, key string, state ratchetLiveness) error {
	entries, err := os.ReadDir(base)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		if state == ratchetLiveYes {
			return err
		}
		return nil
	}
	dir := filepath.Join(base, key)
	restored := false
	var unresolved error
	for _, e := range entries {
		k, marker, ok := splitRatchetTombstone(e)
		if !ok || k != key {
			continue
		}
		tomb := filepath.Join(base, e.Name())
		switch {
		case marker == ratchetConflict:
			// A kept-aside store: erased only once its identity is known gone.
			if state == ratchetLiveNo {
				if err := os.RemoveAll(tomb); err != nil {
					log.Printf("ratchet: erasing a deleted identity's kept-aside store failed: %v", err)
				}
			}
		case marker == ratchetErasing:
			// A committed deletion. A live directory next to it (reopened by
			// another process during the deletion) is that deleted store:
			// erased first, and the marker only once it is gone.
			// Only when the key is KNOWN gone (unknown never erases), and under
			// the destroy lock (never under another process's open store).
			if state == ratchetLiveUnknown {
				if _, err := os.Lstat(dir); err == nil {
					continue // keep the marker: it is the only record of that store
				}
			}
			if state == ratchetLiveNo {
				release, err := durable.LockForDestroy(dir)
				if err != nil {
					log.Printf("ratchet: a deleted store is still open elsewhere: %v", err)
					continue
				}
				err = os.RemoveAll(dir)
				release()
				if err != nil {
					log.Printf("ratchet: erasing a deleted store failed: %v", err)
					continue
				}
			}
			if err := os.RemoveAll(tomb); err != nil {
				log.Printf("ratchet: erasing a deleted store failed: %v", err)
			}
		case state == ratchetLiveNo:
			eraseCommittedTombstone(tomb)
		case state == ratchetLiveUnknown:
			// Liveness unknown: never erase; the next sweep retries.
		case restored:
			// A second uncommitted tombstone of a live identity: keep it.
			log.Printf("ratchet: keeping an extra interrupted deletion of a live identity: %s", e.Name())
		default:
			if _, err := os.Lstat(dir); err == nil {
				aside := dir + ratchetConflict + newClientMsgID()
				if err := os.Rename(dir, aside); err != nil {
					unresolved = err
					continue
				}
				log.Printf("ratchet: kept a store created over an interrupted deletion aside: %s", filepath.Base(aside))
			}
			if err := os.Rename(tomb, dir); err != nil {
				unresolved = err
				continue
			}
			restored = true
		}
	}
	if restored {
		return nil
	}
	return unresolved
}

// sweepRatchetStores runs at startup.
func (a *App) sweepRatchetStores() { a.sweepRatchetStoresIn(a.ratchetBases()) }

// sweepRatchetStoresIn sweeps under identityMu, so it never interleaves with
// a deletion's rename..commit window or an import.
func (a *App) sweepRatchetStoresIn(bases map[string]bool) {
	a.identityMu.Lock()
	defer a.identityMu.Unlock()
	sweepRatchetTombstones(bases)
	a.sweepIdentityLocalTombs()
}

// finishRatchetDeletion runs before a key that is not stored is imported
// (identityMu held): any tombstone of it belongs to a committed deletion and
// is erased (or at least marked .erasing-), so the import never resurrects
// the deleted store.
//
// It scans first and refuses before touching anything: with incomplete
// liveness (an identity file could not be read) nothing at all is erased,
// not even a kept-aside .conflict- store, and the import is refused whenever
// a leftover could be authoritative (.deleted-) or could be reopened as the
// re-imported key's store (a live directory next to .erasing-).
func (a *App) finishRatchetDeletion(publicKey string) error {
	// The caller found no readable stored copy of this key. A readable one
	// keeps its tombstones (resolved at start).
	live, complete := liveRatchetKeys()
	key := ratchetStoreKey(publicKey)
	if live[key] {
		return nil
	}
	// The deleted key's DM history, outbox and other local data never come
	// back either.
	if err := a.finishIdentityLocalDeletion(publicKey, complete); err != nil {
		return err
	}
	unlock := lockRatchetStore(key)
	defer unlock()
	type leftover struct{ base, tomb, marker string }
	var found []leftover
	for base := range a.ratchetBases() {
		entries, err := os.ReadDir(base)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if k, marker, ok := splitRatchetTombstone(e); ok && k == key {
				found = append(found, leftover{base, filepath.Join(base, e.Name()), marker})
			}
		}
	}
	busy := errors.New("an earlier deletion of this identity's secure message store is still being finished; try the import again shortly")
	if !complete {
		for _, f := range found {
			_, liveErr := os.Lstat(filepath.Join(f.base, key))
			// Unknown liveness: the key's own identity file may exist but be
			// unreadable, and a .deleted- tombstone may then be authoritative;
			// a live directory next to .erasing- would be reopened as the
			// imported key's store. Neither erase nor resurrect: refuse.
			if f.marker == ratchetTombstone || (f.marker == ratchetErasing && liveErr == nil) {
				return errors.New("an identity file could not be read; fix or remove it, then import this identity again")
			}
		}
		// Only inert leftovers (.conflict- stores, bare .erasing- markers):
		// never restored, kept until liveness is known.
		return nil
	}
	for _, f := range found {
		switch f.marker {
		case ratchetTombstone:
			// The tombstone is renamed to .erasing- before the erase, so its
			// old path is gone even when the erase failed: use the result.
			if !eraseCommittedTombstone(f.tomb) {
				return busy
			}
			continue
		case ratchetErasing:
			// Next to a committed marker, a live directory is the deleted
			// store that another process reopened: erased first, under the
			// destroy lock (never under another process's open store).
			dir := filepath.Join(f.base, key)
			if _, err := os.Lstat(dir); err == nil {
				release, err := durable.LockForDestroy(dir)
				if err != nil {
					return busy
				}
				err = os.RemoveAll(dir)
				release()
				if err != nil {
					return busy
				}
			}
		}
		// Committed markers and kept-aside stores of the deleted key go too,
		// so nothing of it can come back for the re-imported key.
		if err := os.RemoveAll(f.tomb); err != nil {
			return busy
		}
	}
	return nil
}
