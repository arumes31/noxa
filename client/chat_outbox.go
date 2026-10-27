// Explicitly queued and undoable text messages. Payloads are sealed locally,
// bound to identity + canonical server + pinned certificate, and never logged.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type ChatOutboxEntry struct {
	ID        string `json:"id"`
	Scope     string `json:"scope"`
	Target    string `json:"target"`
	Text      string `json:"text"`
	ReplyToID int64  `json:"reply_to_id"`
	CreatedAt int64  `json:"created_at"`
	DueAt     int64  `json:"due_at"`
	State     string `json:"state"`
	Error     string `json:"error,omitempty"`
}
type ChatOutboxRequest struct {
	Action       string `json:"action"`
	ID           string `json:"id,omitempty"`
	Scope        string `json:"scope,omitempty"`
	Target       string `json:"target,omitempty"`
	Text         string `json:"text,omitempty"`
	ReplyToID    int64  `json:"reply_to_id,omitempty"`
	QueueOffline bool   `json:"queue_offline,omitempty"`
}
type ChatOutboxResult struct {
	Available bool              `json:"available"`
	Entries   []ChatOutboxEntry `json:"entries"`
}
type chatOutboxServer struct {
	addr, fingerprint string
	publicKey         [32]byte
}

// TLS's live fingerprint is intentionally cleared on disconnect. Retain the
// authenticated storage namespace separately; offline staging must not create
// an unrelated empty-fingerprint queue or follow a changed identity.
func (m *connManager) rememberOutboxServer() {
	identity, err := m.identity()
	if err != nil {
		return
	}
	pub, _, err := identity.x25519()
	if err != nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.conn != nil && m.clientID != "" && !m.closed {
		m.outboxServer = &chatOutboxServer{addr: m.addr, fingerprint: m.fingerprint, publicKey: pub}
	}
}

type chatOutboxStore struct {
	path string
	key  [32]byte
}

var outboxMu sync.Mutex
var outboxSending = map[string]bool{}

// This explicit capability probe lets older clients/servers and browser mocks
// retain their existing send path. Outbox support itself is entirely local.
func (a *App) ChatDeliveryFeatures() map[string]bool { return map[string]bool{"outbox": true} }

func (a *App) outboxStore(cm *connManager) (chatOutboxStore, error) {
	base := a.settingsFile()
	if base == "" {
		return chatOutboxStore{}, errors.New("local storage unavailable")
	}
	identity, err := cm.identity()
	if err != nil {
		return chatOutboxStore{}, err
	}
	pub, priv, err := identity.x25519()
	if err != nil {
		return chatOutboxStore{}, err
	}
	cm.mu.Lock()
	binding := cm.outboxServer
	if binding != nil && binding.publicKey != pub {
		cm.mu.Unlock()
		return chatOutboxStore{}, errors.New("outbox identity changed; reconnect with this identity")
	}
	if cm.conn != nil && cm.clientID != "" && !cm.closed {
		binding = &chatOutboxServer{addr: cm.addr, fingerprint: cm.fingerprint, publicKey: pub}
		cm.outboxServer = binding
	}
	cm.mu.Unlock()
	if binding == nil {
		return chatOutboxStore{}, errors.New("connect to this server before using its outbox")
	}
	addr, fingerprint := binding.addr, binding.fingerprint
	canonical, err := canonicalServerDestination(addr)
	if err != nil || canonical == "" {
		return chatOutboxStore{}, errors.New("server identity unavailable")
	}
	owner := sha256.Sum256([]byte(fmt.Sprintf("%x\x00%s\x00%s", pub, canonical, fingerprint)))
	key := sha256.Sum256(append(append([]byte("noxa/chat-outbox/v1|"), priv[:]...), owner[:]...))
	return chatOutboxStore{path: filepath.Join(filepath.Dir(base), "outbox", hex.EncodeToString(owner[:])+".sealed"), key: key}, nil
}

func (s chatOutboxStore) load() ([]ChatOutboxEntry, error) {
	raw, err := readSavedMessageReferences(filepath.Dir(s.path), filepath.Base(s.path))
	if errors.Is(err, os.ErrNotExist) {
		return []ChatOutboxEntry{}, nil
	}
	if err != nil {
		return nil, err
	}
	if len(raw) > 1024*1024 {
		return nil, errors.New("outbox file too large")
	}
	plain, err := openScope(string(raw), s.key)
	if err != nil {
		return nil, errors.New("cannot decrypt this outbox")
	}
	var entries []ChatOutboxEntry
	if json.Unmarshal([]byte(plain), &entries) != nil || len(entries) > 100 {
		return nil, errors.New("invalid outbox file")
	}
	for i := range entries {
		if entries[i].State == "sending" && !outboxSending[s.path+"|"+entries[i].ID] {
			entries[i].State = "uncertain"
			entries[i].Error = "Delivery was interrupted. Check history before retrying."
		}
	}
	return entries, nil
}
func (s chatOutboxStore) save(entries []ChatOutboxEntry) error {
	raw, err := json.Marshal(entries)
	if err != nil {
		return err
	}
	sealed, err := sealScope(string(raw), s.key)
	if err != nil {
		return err
	}
	if len(sealed) > 1024*1024 {
		return errors.New("outbox storage limit reached")
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return err
	}
	return writePrivateFileAtomic(s.path, []byte(sealed))
}

func validOutboxMessage(r ChatOutboxRequest) bool {
	if strings.TrimSpace(r.Text) == "" || len(r.Text) > 65536 || r.ReplyToID < 0 {
		return false
	}
	switch r.Scope {
	case "global":
		return r.Target == ""
	case "direct":
		return r.ReplyToID == 0 && len(r.Target) > 0 && len(r.Target) <= 128
	case "channel":
		id, err := strconv.ParseInt(r.Target, 10, 64)
		return err == nil && id > 0
	}
	return false
}

// ChatOutboxForTab stores text only; attachment tokens/bytes never enter this
// queue. Staging offline requires QueueOffline=true from the explicit UI action.
func (a *App) ChatOutboxForTab(tabID string, request ChatOutboxRequest) (ChatOutboxResult, error) {
	result := ChatOutboxResult{Available: true, Entries: []ChatOutboxEntry{}}
	cm, err := a.requireTabCM(tabID)
	if err != nil {
		return result, err
	}
	store, err := a.outboxStore(cm)
	if err != nil {
		return result, err
	}
	outboxMu.Lock()
	defer outboxMu.Unlock()
	if request.Action == "reset" {
		for key := range outboxSending {
			if strings.HasPrefix(key, store.path+"|") {
				return result, errors.New("sending in progress; wait before resetting the outbox")
			}
		}
		root, name, openErr := openParentRoot(store.path)
		if openErr != nil {
			return result, openErr
		}
		defer func() { _ = root.Close() }()
		if err := root.Rename(name, name+".backup-"+newClientMsgID()); err != nil {
			return result, err
		}
		return result, nil
	}
	entries, err := store.load()
	if err != nil {
		return result, err
	}
	index := -1
	for i, entry := range entries {
		if entry.ID == request.ID {
			index = i
			break
		}
	}
	switch request.Action {
	case "list":
		result.Entries = entries
		return result, nil
	case "stage":
		if !validOutboxMessage(request) {
			return result, errors.New("invalid outbox text or destination")
		}
		if len(entries) >= 100 {
			return result, errors.New("outbox limit reached (100 messages)")
		}
		cm.mu.Lock()
		connected := cm.conn != nil && cm.clientID != "" && !cm.closed
		cm.mu.Unlock()
		if !connected && !request.QueueOffline {
			return result, errors.New("offline: explicitly choose Queue when online")
		}
		a.settingsMu.Lock()
		seconds := a.settings.ChatUndoSeconds
		a.settingsMu.Unlock()
		seconds = max(0, min(30, seconds))
		now := time.Now().UnixMilli()
		state := "held"
		if !connected {
			state = "queued"
		}
		entries = append(entries, ChatOutboxEntry{ID: newClientMsgID(), Scope: request.Scope, Target: request.Target,
			Text: request.Text, ReplyToID: request.ReplyToID, CreatedAt: now, DueAt: now + int64(seconds)*1000, State: state})
	case "cancel", "retry", "send":
		if index < 0 {
			return result, errors.New("outbox entry no longer exists")
		}
		if outboxSending[store.path+"|"+request.ID] {
			return result, errors.New("sending has started; check history")
		}
		if request.Action == "cancel" {
			entries = append(entries[:index], entries[index+1:]...)
		} else {
			entries[index].State = "queued"
			entries[index].DueAt = time.Now().UnixMilli()
			entries[index].Error = ""
		}
	default:
		return result, errors.New("invalid outbox action")
	}
	if err := store.save(entries); err != nil {
		return result, err
	}
	result.Entries = entries
	return result, nil
}

// PumpChatOutbox visits each connected server, including background tabs.
// The frontend calls this while the client is running. No timer or callback
// changes a queued destination, nor does a failure trigger an automatic retry.
func (a *App) PumpChatOutbox() {
	a.tabsMu.Lock()
	managers := make([]*connManager, 0, len(a.tabs))
	for _, tab := range a.tabs {
		if tab != nil && tab.cm != nil {
			managers = append(managers, tab.cm)
		}
	}
	a.tabsMu.Unlock()
	var workers sync.WaitGroup
	slots := make(chan struct{}, 4)
	for _, cm := range managers {
		slots <- struct{}{}
		workers.Add(1)
		go func() { defer workers.Done(); defer func() { <-slots }(); a.pumpChatOutbox(cm) }()
	}
	workers.Wait()
}

func (a *App) pumpChatOutbox(cm *connManager) {
	cm.mu.Lock()
	connected := cm.conn != nil && cm.clientID != "" && !cm.closed
	cm.mu.Unlock()
	if !connected {
		return
	}
	store, err := a.outboxStore(cm)
	if err != nil {
		return
	}
	outboxMu.Lock()
	entries, err := store.load()
	if err != nil {
		outboxMu.Unlock()
		return
	}
	index := -1
	for i, entry := range entries {
		if outboxSending[store.path+"|"+entry.ID] {
			outboxMu.Unlock()
			return
		}
		if (entry.State == "held" || entry.State == "queued") && entry.DueAt <= time.Now().UnixMilli() {
			index = i
			break
		}
	}
	if index < 0 {
		outboxMu.Unlock()
		return
	}
	entry := entries[index]
	key := store.path + "|" + entry.ID
	entries[index].State = "sending"
	if store.save(entries) != nil {
		outboxMu.Unlock()
		return
	}
	outboxSending[key] = true
	outboxMu.Unlock()
	// Resolve current encryption keys and current authorization at actual send.
	failure := a.sendOutboxEntry(cm, store, entry)
	outboxMu.Lock()
	defer outboxMu.Unlock()
	defer delete(outboxSending, key)
	entries, err = store.load()
	if err != nil {
		return
	} // durable "sending" recovers as uncertain on restart
	for i := range entries {
		if entries[i].ID != entry.ID {
			continue
		}
		if failure == "" {
			entries = append(entries[:i], entries[i+1:]...)
		} else {
			entries[i].State = "uncertain"
			entries[i].Error = failure
		}
		_ = store.save(entries) // failure leaves durable interrupted state, never replays
		break
	}
}

// Pin both encryption and delivery to one authenticated connection. Even if a
// reconnect or identity change occurs during a DM key lookup, this attempt
// cannot send an old queue entry through its replacement.
func (a *App) sendOutboxEntry(cm *connManager, store chatOutboxStore, entry ChatOutboxEntry) string {
	cm.mu.Lock()
	conn, epoch, identity := cm.conn, cm.connEpoch, cm.id
	cm.mu.Unlock()
	if entry.Scope == "direct" && cm.ratchetConfigured() {
		// Same binding guard as the legacy path: the queued text belongs to
		// this outbox (identity + server) and the captured connection only.
		currentStore, err := a.outboxStore(cm)
		cm.mu.Lock()
		current := conn != nil && cm.conn == conn && cm.connEpoch == epoch && cm.id == identity
		cm.mu.Unlock()
		if err != nil || currentStore.path != store.path || !current {
			return "connection or identity changed; check history before retrying"
		}
		// The queued entry ID is the stable logical ID: a retry after an
		// uncertain attempt resolves that original operation, never reseals
		// changed text. The consumer is pinned to this connection.
		if _, err := cm.sendRatchetDirect(conn, entry.ID, entry.Target, entry.Text); err != nil {
			return ratchetFailure(err).Error()
		}
		return ""
	}
	msg, err := cm.encryptChat(entry.Scope, entry.Target, entry.Text)
	if err != nil {
		return encryptFailure(err).Error()
	}
	if entry.Scope != "direct" {
		msg.ReplyToID = entry.ReplyToID
	}
	currentStore, err := a.outboxStore(cm)
	cm.mu.Lock()
	current := conn != nil && cm.conn == conn && cm.connEpoch == epoch && cm.id == identity
	cm.mu.Unlock()
	if err != nil || currentStore.path != store.path || !current {
		return "connection or identity changed; check history before retrying"
	}
	if err := cm.sendChatWithKeyRetryOn(conn, msg, entry.Text); err != nil {
		return err.Error()
	}
	return ""
}
