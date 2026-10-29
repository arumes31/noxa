// identity_local_data.go: the per-identity local data that IDENTITY DELETION
// erases together with the key and its noxa-olm-v1 stores. A sign-out,
// disconnect or identity switch keeps all of it.
//
// Erased (whole-identity, legacy and noxa-olm-v1 alike):
//
//	dmhistory/*.vcxdm             every DM log this identity can open (its
//	                              plaintext projection of the ratchet archive
//	                              and its legacy DM history)
//	outbox/<owner>.sealed         queued messages, per known server
//	saved-messages/<owner>.json   saved message references, per known server
//	chat-tab-layout/<name>.sealed pinned conversations, per known server
//	dm-identities/<pin>.json      contact trust pins of the peers in its DM logs
//
// The per-server files are named from (identity, server, certificate
// fingerprint); they are found from every server this device knows (TLS pins,
// live tabs, bookmarks, recent servers).
//
// The deletion is resumable with the same tombstone states as the ratchet
// stores (see ratchet_tombstone.go): before the commit point (identity file
// removal) the files are MOVED into <data>/identity-deletion/<key>.deleted-<id>
// together with a manifest of their original paths; a rollback or a sweep
// that finds the identity still present moves them back. After the commit the
// tombstone is marked .erasing- and erased; a sweep, the startup sweep, or the
// import of the same key again finishes an interrupted erase, so a re-import
// never brings any of it back.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"noxa/internal/auth"
)

const (
	identityLocalBaseName = "identity-deletion"
	identityLocalManifest = "manifest.json"

	localKindDMHistory  = "dmhistory"
	localKindOutbox     = "outbox"
	localKindSaved      = "saved-messages"
	localKindTabLayout  = "chat-tab-layout"
	localKindContactPin = "contact-pin"
)

// errIdentityDeleted refuses a local-store write for an identity whose
// deletion is in progress or done in this process.
var errIdentityDeleted = errors.New("this identity was deleted from this device")

// identityDeleted reports the in-process deletion fence of an identity key.
func identityDeleted(publicKey string) bool {
	if publicKey == "" {
		return false
	}
	_, deleted := ratchetDeleted.Load(publicKey)
	return deleted
}

// localServerScope is the (identity, server, certificate) scope that names
// the per-server local files (outbox, saved messages, chat tab layout).
func localServerScope(pub [32]byte, canonical, fingerprint string) string {
	return fmt.Sprintf("%x\x00%s\x00%s", pub, canonical, fingerprint)
}

// localServerOwner names an outbox or saved-messages file.
func localServerOwner(pub [32]byte, canonical, fingerprint string) [32]byte {
	return sha256.Sum256([]byte(localServerScope(pub, canonical, fingerprint)))
}

// chatTabLayoutName names a chat tab layout file.
func chatTabLayoutName(pub [32]byte, canonical, fingerprint string) [32]byte {
	return sha256.Sum256([]byte(chatTabLayoutLabel + "|name|" + localServerScope(pub, canonical, fingerprint)))
}

// dmPinPathFor names a contact trust pin of local (raw Ed25519 public key).
func dmPinPathFor(local []byte, server, uid string) (string, error) {
	root, err := identityRoot()
	if dmTrustRootOverride != "" {
		root, err = dmTrustRootOverride, nil
	}
	if err != nil {
		return "", err
	}
	binding, _ := json.Marshal([]string{hex.EncodeToString(local), server, uid})
	hash := sha256.Sum256(binding)
	return filepath.Join(root, "dm-identities", hex.EncodeToString(hash[:])+".json"), nil
}

// identityLocalBase is the tombstone base, next to the local stores. An App
// without a settings path (tests) has no local stores and no base.
func (a *App) identityLocalBase() string {
	base := a.settingsFile()
	if base == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(base), identityLocalBaseName)
}

type serverScope struct{ canonical, fingerprint string }

// knownServerScopes lists every (server, certificate) pair this device knows:
// TLS pins, live tabs, bookmarks and recent servers, each also without a
// fingerprint (a connection without TLS).
func (a *App) knownServerScopes() []serverScope {
	pins := map[string]string{}
	if ks := a.knownServers; ks != nil {
		ks.mu.Lock()
		for addr, fp := range ks.Servers {
			pins[addr] = fp
		}
		ks.mu.Unlock()
	}
	seen := map[serverScope]bool{}
	var out []serverScope
	add := func(addr, fp string) {
		canonical, err := canonicalServerDestination(addr)
		if err != nil || canonical == "" {
			return
		}
		for _, f := range []string{fp, pins[canonical], ""} {
			s := serverScope{canonical, f}
			if !seen[s] {
				seen[s] = true
				out = append(out, s)
			}
		}
	}
	for addr, fp := range pins {
		add(addr, fp)
	}
	a.tabsMu.Lock()
	for _, tab := range a.tabs {
		if tab != nil && tab.cm != nil {
			tab.cm.mu.Lock()
			addr, fp := tab.cm.addr, tab.cm.fingerprint
			var bound *chatOutboxServer
			if tab.cm.outboxServer != nil {
				b := *tab.cm.outboxServer
				bound = &b
			}
			tab.cm.mu.Unlock()
			add(addr, fp)
			if bound != nil {
				add(bound.addr, bound.fingerprint)
			}
		}
	}
	a.tabsMu.Unlock()
	a.settingsMu.Lock()
	var addrs []string
	for _, b := range a.settings.Bookmarks {
		addrs = append(addrs, b.Addr)
	}
	for _, r := range a.settings.Recents {
		addrs = append(addrs, r.Addr)
	}
	a.settingsMu.Unlock()
	for _, addr := range addrs {
		add(addr, "")
	}
	return out
}

// localTombEntry is one moved file: its original path and its name inside
// the tombstone.
type localTombEntry struct {
	Kind string `json:"kind"`
	Path string `json:"path"`
	Name string `json:"name"`
}

type localTombManifest struct {
	Version int              `json:"version"`
	Entries []localTombEntry `json:"entries"`
}

// localTomb collects one deletion's moved files. The manifest is written
// (atomically) before every batch of renames, so it always lists at least
// the files inside the tombstone.
type localTomb struct {
	dir      string
	manifest localTombManifest
}

func (t *localTomb) move(kind string, paths []string) error {
	var batch []localTombEntry
	for _, p := range paths {
		if _, err := os.Lstat(p); err == nil {
			batch = append(batch, localTombEntry{Kind: kind, Path: p, Name: fmt.Sprintf("%04d", len(t.manifest.Entries)+len(batch))})
		}
	}
	if len(batch) == 0 {
		return nil
	}
	if err := os.MkdirAll(t.dir, 0o700); err != nil {
		return err
	}
	t.manifest.Version = 1
	t.manifest.Entries = append(t.manifest.Entries, batch...)
	raw, err := json.Marshal(t.manifest)
	if err != nil {
		return err
	}
	if err := writePrivateFileAtomic(filepath.Join(t.dir, identityLocalManifest), raw); err != nil {
		return err
	}
	for _, e := range batch {
		if err := os.Rename(e.Path, filepath.Join(t.dir, e.Name)); err != nil {
			return err
		}
	}
	return nil
}

// localKindLock is the lock every writer of that kind of file holds.
func (a *App) localKindLock(kind string) sync.Locker {
	switch kind {
	case localKindDMHistory:
		return &a.dmMu
	case localKindOutbox:
		return &outboxMu
	case localKindSaved:
		return &savedMessagesMu
	case localKindTabLayout:
		return &chatTabLayoutMu
	}
	return &contactPinMoveMu
}

// contactPinMoveMu serialises moves of contact pins (their writers are
// fenced by the identity's deletion state instead).
var contactPinMoveMu sync.Mutex

// identityDMLogs lists the DM logs this identity can open, and their peers.
// The caller holds a.dmMu.
func identityDMLogs(dir string, id *identity) (paths, peers []string) {
	store, err := dmHistoryStoreForIdentity(dir, id)
	if err != nil {
		return nil, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil
	}
	for _, e := range entries {
		if !e.Type().IsRegular() || !strings.HasSuffix(e.Name(), dmHistoryExt) {
			continue
		}
		path := filepath.Join(dir, e.Name())
		blob, err := os.ReadFile(path) // #nosec G304 -- a regular file listed from the application-owned DM history directory.
		if err != nil {
			continue
		}
		l, err := store.open(blob)
		if err != nil {
			continue // another identity's log
		}
		paths = append(paths, path)
		if l.Peer != "" {
			peers = append(peers, l.Peer)
		}
	}
	return paths, peers
}

// tombstoneIdentityLocalData moves the identity's local data into a new
// .deleted- tombstone (step 3 of deleteIdentityWithStores). It returns the
// tombstone (empty when nothing was moved, even on error).
func (a *App) tombstoneIdentityLocalData(id *identity) (string, error) {
	base := a.identityLocalBase()
	if base == "" {
		return "", nil
	}
	root := filepath.Dir(base)
	t := &localTomb{dir: filepath.Join(base, ratchetStoreKey(id.PublicKey)+ratchetTombstone+newClientMsgID())}
	moved := func() string {
		if len(t.manifest.Entries) == 0 {
			return ""
		}
		return t.dir
	}
	run := func(kind string, paths func() []string) error {
		lock := a.localKindLock(kind)
		lock.Lock()
		defer lock.Unlock()
		return t.move(kind, paths())
	}
	var peers []string
	if err := run(localKindDMHistory, func() []string {
		paths, p := identityDMLogs(filepath.Join(root, "dmhistory"), id)
		peers = p
		return paths
	}); err != nil {
		return moved(), err
	}
	scopes := a.knownServerScopes()
	if pub, _, err := id.x25519(); err == nil {
		var outbox, saved, layouts []string
		for _, s := range scopes {
			ownerSum := localServerOwner(pub, s.canonical, s.fingerprint)
			owner := hex.EncodeToString(ownerSum[:])
			name := chatTabLayoutName(pub, s.canonical, s.fingerprint)
			outbox = append(outbox, filepath.Join(root, "outbox", owner+".sealed"))
			saved = append(saved, filepath.Join(root, "saved-messages", owner+".json"))
			layouts = append(layouts, filepath.Join(root, "chat-tab-layout", hex.EncodeToString(name[:])+".sealed"))
		}
		for _, step := range []struct {
			kind  string
			paths []string
		}{{localKindOutbox, outbox}, {localKindSaved, saved}, {localKindTabLayout, layouts}} {
			paths := step.paths
			if err := run(step.kind, func() []string { return paths }); err != nil {
				return moved(), err
			}
		}
	}
	if local, err := auth.LoadPublicKey(id.PublicKey); err == nil && len(peers) > 0 {
		servers := map[string]bool{}
		for _, s := range scopes {
			servers[s.canonical] = true
		}
		var pins []string
		for server := range servers {
			for _, peer := range peers {
				if p, err := dmPinPathFor(local, server, peer); err == nil {
					pins = append(pins, p)
				}
			}
		}
		if err := run(localKindContactPin, func() []string { return pins }); err != nil {
			return moved(), err
		}
	}
	return moved(), nil
}

// readLocalTomb reads a tombstone's manifest. A tombstone without one holds
// no moved file yet (a crash before the first manifest write).
func readLocalTomb(dir string) (localTombManifest, error) {
	var m localTombManifest
	raw, err := os.ReadFile(filepath.Join(dir, identityLocalManifest)) // #nosec G304 -- application-owned tombstone directory.
	if errors.Is(err, os.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return m, err
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return m, err
	}
	for _, e := range m.Entries {
		if e.Name == "" || e.Name != filepath.Base(e.Name) || e.Name == identityLocalManifest || !filepath.IsAbs(e.Path) {
			return m, errors.New("invalid identity deletion manifest")
		}
	}
	return m, nil
}

// restoreLocalTomb moves an uncommitted deletion's files back (the identity
// survives). A file whose original path was recreated meanwhile is kept
// aside: the tombstone becomes .conflict- and is never erased while the
// identity exists.
func (a *App) restoreLocalTomb(dir string) error {
	m, err := readLocalTomb(dir)
	if err != nil {
		return err
	}
	conflict := false
	var failed error
	for _, e := range m.Entries {
		src := filepath.Join(dir, e.Name)
		if _, err := os.Lstat(src); err != nil {
			continue
		}
		lock := a.localKindLock(e.Kind)
		lock.Lock()
		if _, err := os.Lstat(e.Path); err == nil {
			conflict = true
		} else if err := os.MkdirAll(filepath.Dir(e.Path), 0o700); err != nil {
			failed = err
		} else if err := os.Rename(src, e.Path); err != nil {
			failed = err
		}
		lock.Unlock()
	}
	if failed != nil {
		return failed
	}
	if conflict {
		name := filepath.Base(dir)
		if i := strings.LastIndex(name, ratchetTombstone); i > 0 {
			aside := filepath.Join(filepath.Dir(dir), name[:i]+ratchetConflict+name[i+len(ratchetTombstone):])
			if err := os.Rename(dir, aside); err != nil {
				return err
			}
			log.Printf("identity deletion: kept local data recreated over an interrupted deletion aside: %s", filepath.Base(aside))
		}
		return nil
	}
	return os.RemoveAll(dir)
}

// eraseLocalTomb marks a committed deletion's tombstone .erasing- (never
// restored) and erases it. It reports whether nothing is left.
func eraseLocalTomb(dir string) bool {
	return eraseCommittedTombstone(dir)
}

// localTombs lists the tombstones of base, optionally of one key only.
func localTombs(base, onlyKey string) []struct{ path, key, marker string } {
	entries, err := os.ReadDir(base)
	if err != nil {
		return nil
	}
	var out []struct{ path, key, marker string }
	for _, e := range entries {
		key, marker, ok := splitRatchetTombstone(e)
		if !ok || (onlyKey != "" && key != onlyKey) {
			continue
		}
		out = append(out, struct{ path, key, marker string }{filepath.Join(base, e.Name()), key, marker})
	}
	return out
}

// sweepIdentityLocalTombs finishes interrupted local-data deletions (callers
// hold a.identityMu): a committed one is erased, an uncommitted one of a
// live identity restored, one of a gone identity erased; unknown liveness
// changes nothing. It reports whether nothing is left to finish.
func (a *App) sweepIdentityLocalTombs() bool {
	base := a.identityLocalBase()
	if base == "" {
		return true
	}
	tombs := localTombs(base, "")
	if len(tombs) == 0 {
		return true
	}
	live, complete := liveRatchetKeys()
	done := true
	for _, tomb := range tombs {
		state := ratchetLiveUnknown
		switch {
		case live[tomb.key]:
			state = ratchetLiveYes
		case complete:
			state = ratchetLiveNo
		}
		switch {
		case tomb.marker == ratchetErasing:
			if err := os.RemoveAll(tomb.path); err != nil {
				log.Printf("identity deletion: erasing deleted local data failed: %v", err)
				done = false
			}
		case state == ratchetLiveNo:
			if tomb.marker == ratchetConflict {
				if err := os.RemoveAll(tomb.path); err != nil {
					done = false
				}
			} else if !eraseLocalTomb(tomb.path) {
				done = false
			}
		case state == ratchetLiveYes && tomb.marker == ratchetTombstone:
			if err := a.restoreLocalTomb(tomb.path); err != nil {
				log.Printf("identity deletion: restoring an interrupted deletion's local data failed: %v", err)
				done = false
			}
		case state == ratchetLiveUnknown && tomb.marker != ratchetConflict:
			done = false
		}
	}
	return done
}

// finishIdentityLocalDeletion runs before a key that is not stored is
// imported (identityMu held; complete reports full liveness knowledge):
// every leftover of a deletion of that key is erased, so the import never
// brings the deleted data back. With incomplete liveness an uncommitted
// tombstone could be authoritative: the import is refused.
func (a *App) finishIdentityLocalDeletion(publicKey string, complete bool) error {
	base := a.identityLocalBase()
	if base == "" {
		return nil
	}
	tombs := localTombs(base, ratchetStoreKey(publicKey))
	if !complete {
		for _, t := range tombs {
			if t.marker == ratchetTombstone {
				return errors.New("an identity file could not be read; fix or remove it, then import this identity again")
			}
		}
		return nil
	}
	for _, t := range tombs {
		ok := true
		if t.marker == ratchetTombstone {
			ok = eraseLocalTomb(t.path)
		} else if err := os.RemoveAll(t.path); err != nil {
			ok = false
		}
		if !ok {
			return errors.New("an earlier deletion of this identity's local data is still being finished; try the import again shortly")
		}
	}
	return nil
}
