// Identity-bound, encrypted local direct-message history.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"noxa/internal/netproto"
)

// DMs are true E2EE and the server never stores them, so a DM "history" can
// only ever be local. It is treated exactly the way wave 1 treats
// chat_messages: ciphertext at rest, opened only here in Go, never handed to
// the webview sealed. One file per peer, sealed with a key derived from the
// identity's X25519 PRIVATE key — the same secret that opened the messages in
// the first place — so a log that leaves the device (a synced config folder, a
// backup, a stolen disk image) is worth nothing without identity.json, and no
// other identity can ever read it.
const (
	// dmHistoryMax caps a peer's log. The whole file is rewritten per append,
	// so this bounds write cost as well as disk.
	dmHistoryMax = 5000
	dmHistoryExt = ".vcxdm"
)

// DMEntry is one direct message in the local log: what the DM tab needs to
// re-render after a restart, and nothing the server ever saw.
type DMEntry struct {
	Seq          int64  `json:"seq"`
	FromUniqueID string `json:"from_unique_id"`
	FromNickname string `json:"from_nickname"`
	Body         string `json:"body"`
	SentAt       int64  `json:"sent_at"` // unix seconds
	Self         bool   `json:"self,omitempty"`
	ClientMsgID  string `json:"client_msg_id,omitempty"`
	Offline      bool   `json:"offline,omitempty"`
	EncVerified  bool   `json:"enc_verified,omitempty"` // body was opened locally; absent on legacy records
	// EncMode records how a verified body was protected: "e2e". Empty on a
	// verified record means end-to-end (records written before the field
	// existed). An unverified record may carry "downgraded": a body opened
	// with the server-held key, never treated as verified. A verified
	// "server" record (the retired fallback) reads back as downgraded.
	EncMode string `json:"enc_mode,omitempty"`
	// Edited/EditedAt and Deleted record a later change by the message's own
	// sender, received as per-device copies (dm_devices_v1). A deleted
	// record keeps its place in the log with an empty body.
	Edited   bool  `json:"edited,omitempty"`
	EditedAt int64 `json:"edited_at,omitempty"`
	Deleted  bool  `json:"deleted,omitempty"`
	// Protocol is "noxa-olm-v1" for a record projected from the protected
	// ratchet archive (empty = legacy). Such entries are keyed by (protocol,
	// sender, ClientMsgID = sender logical ID); Key is the semantic key and
	// Status the own-send receipt state or "received".
	Protocol string `json:"protocol,omitempty"`
	Key      string `json:"key,omitempty"`
	Status   string `json:"status,omitempty"`
}

// DMPeer summarises one stored conversation, so PM tabs can be restored on
// start without opening every log.
type DMPeer struct {
	UniqueID string `json:"unique_id"`
	Nickname string `json:"nickname,omitempty"`
	Messages int    `json:"messages"`
	LastAt   int64  `json:"last_at"`
	// Last is the newest stored entry, so the Messages list can show a
	// preview and its encryption badge without loading every whole log. It
	// never leaves this process: the frontend reads it over the local bridge.
	Last *DMEntry `json:"last,omitempty"`
}

// dmLog is the sealed file's plaintext. The peer id lives INSIDE the
// ciphertext: the file name is a hash, so the directory cannot be listed to
// learn who this user talks to.
type dmLog struct {
	Peer     string    `json:"peer"`
	Nickname string    `json:"nickname,omitempty"`
	Messages []DMEntry `json:"messages"` // oldest first
}

// dmBeforeRead is a test seam placed only around read-only public APIs. It
// proves those APIs do not hold dmMu while a disk read is pending; writers
// still retain dmMu for their read-modify-write transaction.
var dmBeforeRead func()

// dmBeforeWrite lets tests pause an accepted operation after owner capture,
// before it waits for the disk transaction lock.
var dmBeforeWrite func()

// dmHistoryDir returns the per-device log directory. An App with no settings
// path (tests, with the default-path fallback disarmed) gets an error rather
// than a write into the developer's real config directory.
func (a *App) dmHistoryDir() (string, error) {
	base := a.settingsFile()
	if base == "" {
		return "", errors.New("no local storage path for DM history")
	}
	return filepath.Join(filepath.Dir(base), "dmhistory"), nil
}

// dmIdentity resolves the key material the logs are bound to. It prefers the
// active tab's already-loaded identity and falls back to the file, so PM tabs
// can be restored before the client has connected to anything. Every caller
// resolves the storage directory first, so an App with no settings path (a
// test) can never reach this and touch the real identity.json.
func (a *App) dmIdentity() (*identity, error) {
	if m := a.cmLoad(); m != nil {
		return m.identity()
	}
	return loadOrCreateIdentity()
}

// dmHistoryStore retains one identity across an entire local operation,
// including waits for another writer and scans of multiple conversations.
type dmHistoryStore struct {
	dir       string
	publicKey [32]byte
	key       [32]byte
	// identityKey fences writes once the identity is deleted.
	identityKey string
}

func (a *App) captureDMHistory() (dmHistoryStore, error) {
	dir, err := a.dmHistoryDir()
	if err != nil {
		return dmHistoryStore{}, err
	}
	id, err := a.dmIdentity()
	if err != nil {
		return dmHistoryStore{}, err
	}
	return dmHistoryStoreForIdentity(dir, id)
}

func dmHistoryStoreForIdentity(dir string, id *identity) (dmHistoryStore, error) {
	pub, _, err := id.x25519()
	if err != nil {
		return dmHistoryStore{}, err
	}
	key, err := dmHistoryKey(id)
	if err != nil {
		return dmHistoryStore{}, err
	}
	return dmHistoryStore{dir: dir, publicKey: pub, key: key, identityKey: id.PublicKey}, nil
}

// dmHistoryPath names a peer's log from a hash of (own public key, peer), so
// the file name leaks neither the peer nor the owner.
func (a *App) dmHistoryPath(peer string) (string, error) {
	store, err := a.captureDMHistory()
	if err != nil {
		return "", err
	}
	return store.path(peer), nil
}

func (s dmHistoryStore) path(peer string) string {
	sum := sha256.Sum256(append(append([]byte(dmHistoryKeyLabel+"|name|"), s.publicKey[:]...), peer...))
	return filepath.Join(s.dir, hex.EncodeToString(sum[:])[:32]+dmHistoryExt)
}

// load opens a peer's log. A missing file is an empty log, not an error:
// the first DM with someone is the normal case.
func (s dmHistoryStore) load(peer string) (dmLog, error) {
	root, name, err := openParentRoot(s.path(peer))
	if err != nil {
		if os.IsNotExist(err) {
			return dmLog{Peer: peer}, nil
		}
		return dmLog{}, err
	}
	defer func() { _ = root.Close() }()
	blob, err := root.ReadFile(name)
	if err != nil {
		if os.IsNotExist(err) {
			return dmLog{Peer: peer}, nil
		}
		return dmLog{}, err
	}
	return s.open(blob)
}

// open unseals and decodes a log file's bytes.
func (s dmHistoryStore) open(blob []byte) (dmLog, error) {
	plain, err := openFile(blob, s.key)
	if err != nil {
		// A log this identity cannot open is not this identity's log. Failing
		// closed keeps a corrupt or foreign file from being silently replaced.
		return dmLog{}, errors.New("DM history unreadable with this identity")
	}
	var l dmLog
	if err := json.Unmarshal(plain, &l); err != nil {
		return dmLog{}, err
	}
	return l, nil
}

// save seals and writes a peer's log, trimming it to the newest
// dmHistoryMax messages.
func (s dmHistoryStore) save(l dmLog) error {
	if identityDeleted(s.identityKey) {
		return errIdentityDeleted
	}
	if n := len(l.Messages); n > dmHistoryMax {
		l.Messages = append([]DMEntry(nil), l.Messages[n-dmHistoryMax:]...)
	}
	path := s.path(l.Peer)
	raw, err := json.Marshal(l)
	if err != nil {
		return err
	}
	blob, err := sealFile(raw, s.key)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".dm-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		_ = tmp.Close()
		if tmpName != "" {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(blob); err != nil {
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	tmpName = ""
	return nil
}

// DMHistoryLoad returns a peer's stored conversation, oldest first, decrypted.
func (a *App) DMHistoryLoad(peer string) ([]DMEntry, error) {
	if peer == "" {
		return nil, errors.New("peer is required")
	}
	store, err := a.captureDMHistory()
	if err != nil {
		return nil, err
	}
	return store.messages(peer)
}

func (s dmHistoryStore) messages(peer string) ([]DMEntry, error) {
	if peer == "" {
		return nil, errors.New("peer is required")
	}
	if hook := dmBeforeRead; hook != nil {
		hook()
	}
	l, err := s.load(peer)
	if err != nil {
		return nil, err
	}
	if l.Messages == nil {
		l.Messages = []DMEntry{}
	}
	for i := range l.Messages {
		m := &l.Messages[i]
		m.EncVerified, m.EncMode = dmEncState(*m)
	}
	return l.Messages, nil
}

// DMHistoryAppend records one DM (sent or received) in the peer's local log
// and returns "" or the error. Seq is assigned here when the caller leaves it
// zero, so the log keeps a stable local order the server never provided.
func (a *App) DMHistoryAppend(peer, nickname string, e DMEntry) string {
	if peer == "" {
		return "peer is required"
	}
	store, err := a.captureDMHistory()
	if err != nil {
		return err.Error()
	}
	return a.dmHistoryAppendWith(store, peer, nickname, e)
}

func (a *App) dmHistoryAppendWith(store dmHistoryStore, peer, nickname string, e DMEntry) string {
	if peer == "" {
		return "peer is required"
	}
	if hook := dmBeforeWrite; hook != nil {
		hook()
	}
	a.dmMu.Lock()
	defer a.dmMu.Unlock()
	l, err := store.load(peer)
	if err != nil {
		return err.Error()
	}
	l.Peer = peer
	if nickname != "" {
		l.Nickname = nickname
	}
	if e.Seq == 0 {
		if n := len(l.Messages); n > 0 {
			e.Seq = l.Messages[n-1].Seq + 1
		} else {
			e.Seq = 1
		}
	}
	if e.SentAt == 0 {
		e.SentAt = time.Now().Unix()
	}
	normalizeDMEncryption(&e)
	if e.ClientMsgID != "" {
		// Re-delivery of the same message (a reconnect replaying an offline
		// spool) must not duplicate the log.
		for _, old := range l.Messages {
			if old.ClientMsgID == e.ClientMsgID && old.Self == e.Self && old.Protocol == e.Protocol {
				return ""
			}
		}
	}
	l.Messages = append(l.Messages, e)
	// A change that overtook its original (spool order) applies now.
	for _, change := range a.dmPending.take(store.path(peer), e.ClientMsgID, time.Now()) {
		if change.From == e.FromUniqueID {
			applyDMChange(&l.Messages[len(l.Messages)-1], change)
		}
	}
	if err := store.save(l); err != nil {
		return err.Error()
	}
	return ""
}

// DMHistoryClear deletes a peer's stored conversation (explicit user action).
func (a *App) DMHistoryClear(peer string) string {
	if peer == "" {
		return "peer is required"
	}
	store, err := a.captureDMHistory()
	if err != nil {
		return err.Error()
	}
	return a.dmHistoryClearWith(store, peer)
}

func (a *App) dmHistoryClearWith(store dmHistoryStore, peer string) string {
	if peer == "" {
		return "peer is required"
	}
	if hook := dmBeforeWrite; hook != nil {
		hook()
	}
	a.dmMu.Lock()
	defer a.dmMu.Unlock()
	if err := os.Remove(store.path(peer)); err != nil && !os.IsNotExist(err) {
		return err.Error()
	}
	return ""
}

// DMHistoryPeers lists the stored conversations, most recent first, so PM tabs
// survive a restart. Logs sealed to a different identity are skipped rather
// than reported: they are not this user's conversations.
func (a *App) DMHistoryPeers() []DMPeer {
	store, err := a.captureDMHistory()
	if err != nil {
		return []DMPeer{}
	}
	return store.peers()
}

func (s dmHistoryStore) peers() []DMPeer {
	out := []DMPeer{}
	root, err := os.OpenRoot(s.dir)
	if err != nil {
		return out
	}
	defer func() { _ = root.Close() }()
	ents, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		return out
	}
	for _, ent := range ents {
		if ent.IsDir() || !strings.HasSuffix(ent.Name(), dmHistoryExt) {
			continue
		}
		blob, err := root.ReadFile(ent.Name())
		if err != nil {
			continue
		}
		l, err := s.open(blob)
		if err != nil || l.Peer == "" {
			continue
		}
		p := DMPeer{UniqueID: l.Peer, Nickname: l.Nickname, Messages: len(l.Messages)}
		if n := len(l.Messages); n > 0 {
			last := l.Messages[n-1]
			p.LastAt = last.SentAt
			p.Last = &last
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastAt > out[j].LastAt })
	return out
}

// DMSearch searches the local DM logs and answers in the SAME shape as
// ChatSearch, so the existing client-side search UI renders DM hits with no
// second code path (122/110). An empty peer searches every stored
// conversation. Each hit carries its record's own verification and mode, so
// a server-encrypted or unverified record never shows the lock.
func (a *App) DMSearch(peer, query string, maxMessages int) (ChatSearchResult, error) {
	store, err := a.captureDMHistory()
	if err != nil {
		return ChatSearchResult{Messages: []netproto.ChatHistoryEntry{}}, nil
	}
	return store.search(peer, query, maxMessages), nil
}

func (s dmHistoryStore) search(peer, query string, maxMessages int) ChatSearchResult {
	res := ChatSearchResult{Messages: []netproto.ChatHistoryEntry{}, Complete: true}
	q := strings.ToLower(strings.TrimSpace(query))
	if maxMessages <= 0 {
		maxMessages = chatSearchDefaultMax
	}
	peers := []string{peer}
	if peer == "" {
		peers = nil
		for _, p := range s.peers() {
			peers = append(peers, p.UniqueID)
		}
	}

	for _, uid := range peers {
		if hook := dmBeforeRead; hook != nil {
			hook()
		}
		l, err := s.load(uid)
		if err != nil {
			res.Complete = false
			continue
		}
		for i := len(l.Messages) - 1; i >= 0; i-- { // newest first, like ChatSearch
			if res.Scanned >= maxMessages {
				res.Complete = false
				return res
			}
			m := l.Messages[i]
			res.Scanned++
			if m.Deleted {
				continue
			}
			if q != "" && !strings.Contains(strings.ToLower(m.Body), q) {
				continue
			}
			entry := netproto.ChatHistoryEntry{
				ID:           m.Seq,
				FromUniqueID: m.FromUniqueID,
				FromNickname: m.FromNickname,
				Body:         m.Body,
				SentAt:       m.SentAt,
			}
			entry.EncVerified, entry.EncMode = dmEncState(m)
			entry.Protocol = m.Protocol
			res.Messages = append(res.Messages, entry)
		}
	}
	return res
}

// dmEncState names how a stored DM was protected, as every reader (history
// load, search, export) presents it. A verified record without a mode
// predates the mode field (appends since then always carry one, see
// normalizeDMEncryption), so it was end-to-end. DMs are end-to-end only: a
// record verified as "server" (the retired client→server→client fallback) or
// marked "downgraded" is shown unverified under the downgraded warning.
func dmEncState(m DMEntry) (verified bool, mode string) {
	if m.EncVerified {
		switch m.EncMode {
		case "", "e2e":
			return true, "e2e"
		case "server", dmModeDowngraded:
			return false, dmModeDowngraded
		}
		return false, ""
	}
	if m.EncMode == dmModeDowngraded {
		return false, dmModeDowngraded
	}
	return false, ""
}

// dmEncMode is the mode half of dmEncState.
func dmEncMode(m DMEntry) string {
	_, mode := dmEncState(m)
	return mode
}

// normalizeDMEncryption keeps the stored marker unambiguous: a new verified
// record must say "e2e", otherwise it is stored as unverified rather than
// read back later as a pre-mode end-to-end record. A verified "server" or
// any "downgraded" record is stored as unverified downgraded, never verified.
func normalizeDMEncryption(e *DMEntry) {
	if e.EncVerified && e.EncMode == "" {
		e.EncVerified = false
		return
	}
	e.EncVerified, e.EncMode = dmEncState(*e)
}
