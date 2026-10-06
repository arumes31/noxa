package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"noxa/internal/netproto"
)

const (
	chatTabLayoutLabel    = "noxa/chat-tab-layout/v1"
	chatTabLayoutMaxBytes = 64 * 1024
)

// ChatTabLayout stores references and optional display names for pinned DMs.
// Names stay sealed with the layout; messages and unread state are not stored.
type ChatTabLayout struct {
	Order  []string          `json:"order"`
	Pinned []string          `json:"pinned"`
	Names  map[string]string `json:"names,omitempty"`
}

func (l ChatTabLayout) valid() bool {
	if len(l.Order) > 256 || len(l.Pinned) > 64 || len(l.Names) > 64 {
		return false
	}
	keys := make(map[string]bool, len(l.Order))
	for _, key := range l.Order {
		if !validChatTabKey(key) || keys[key] {
			return false
		}
		keys[key] = true
	}
	for _, key := range l.Pinned {
		if !keys[key] {
			return false
		}
		keys[key] = false // Reject duplicate pins as well as missing references.
	}
	for key, name := range l.Names {
		unpinned, exists := keys[key]
		if !exists || unpinned || !strings.HasPrefix(key, "dm:") ||
			len(name) > utf8.UTFMax*netproto.MaxDisplayNameLength || strings.ContainsAny(name, "\u2028\u2029") {
			return false
		}
		if _, err := netproto.NormalizeDisplayName(name); err != nil {
			return false
		}
	}
	return true
}

// canonicalized is called only after validation. Clone the optional map so
// trimming display names does not mutate a caller's in-memory preferences.
func (l ChatTabLayout) canonicalized() ChatTabLayout {
	if l.Order == nil {
		l.Order = []string{}
	}
	if l.Pinned == nil {
		l.Pinned = []string{}
	}
	l.Names = maps.Clone(l.Names)
	for key, name := range l.Names {
		l.Names[key] = strings.TrimSpace(name)
	}
	return l
}

func validChatTabKey(key string) bool {
	if channel, ok := strings.CutPrefix(key, "ch:"); ok {
		id, err := strconv.ParseInt(channel, 10, 64)
		return err == nil && id > 0 && strconv.FormatInt(id, 10) == channel
	}
	peer, ok := strings.CutPrefix(key, "dm:")
	return ok && len(peer) > 0 && len(peer) <= 128 && utf8.ValidString(peer) &&
		strings.IndexFunc(peer, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) < 0
}

type chatTabLayoutStore struct {
	path string
	key  [32]byte
}

// Only disk transactions share this lock; identity/activation/connection locks
// are released before waiting for I/O. Captured operations retain their owner.
var chatTabLayoutMu sync.Mutex

func (a *App) chatTabLayoutStore(owner DMHistoryContext) (chatTabLayoutStore, error) {
	var empty chatTabLayoutStore
	cm, err := a.requireTabCM(owner.TabID)
	if err != nil {
		return empty, err
	}
	history, err := a.dmHistoryForContext(owner)
	if err != nil {
		return empty, err
	}
	cm.mu.Lock()
	addr, fingerprint := cm.addr, cm.fingerprint
	ready := cm.conn != nil && !cm.closed && (!cm.tlsUsed || fingerprint != "")
	cm.mu.Unlock()
	if !ready || addr == "" {
		return empty, errors.New("chat tab layout requires a connected server")
	}
	canonical, err := normalizeServerAddr(addr)
	if err != nil {
		return empty, fmt.Errorf("chat tab layout server: %w", err)
	}
	// Revalidate after reading connection metadata: activation or identity may
	// have changed between the first owner capture and the connection snapshot.
	if _, err := a.dmHistoryForContext(owner); err != nil {
		return empty, err
	}
	current, err := a.requireTabCM(owner.TabID)
	if err != nil || current != cm {
		return empty, errDMHistoryContextChanged
	}
	scope := fmt.Sprintf("%x\x00%s\x00%s", history.publicKey, canonical, fingerprint)
	name := sha256.Sum256([]byte(chatTabLayoutLabel + "|name|" + scope))
	mac := hmac.New(sha256.New, history.key[:])
	_, _ = mac.Write([]byte(chatTabLayoutLabel + "|key|" + scope))
	var key [32]byte
	copy(key[:], mac.Sum(nil))
	return chatTabLayoutStore{
		path: filepath.Join(filepath.Dir(history.dir), "chat-tab-layout", hex.EncodeToString(name[:])+".sealed"),
		key:  key,
	}, nil
}

// ChatTabLayoutForContext reads only the current server and identity. A missing
// file is an empty layout; unavailable or unreadable storage remains an error.
func (a *App) ChatTabLayoutForContext(owner DMHistoryContext) (ChatTabLayout, error) {
	store, err := a.chatTabLayoutStore(owner)
	if err != nil {
		return ChatTabLayout{}, err
	}
	chatTabLayoutMu.Lock()
	defer chatTabLayoutMu.Unlock()
	return store.load()
}

// SaveChatTabLayoutForContext preserves the previous ciphertext on every
// failure. An obsolete owner never retries against the newly selected identity.
func (a *App) SaveChatTabLayoutForContext(owner DMHistoryContext, layout ChatTabLayout) string {
	if !layout.valid() {
		return "invalid chat tab layout"
	}
	layout = layout.canonicalized()
	store, err := a.chatTabLayoutStore(owner)
	if err != nil {
		return err.Error()
	}
	chatTabLayoutMu.Lock()
	defer chatTabLayoutMu.Unlock()
	// Do not replace a damaged/foreign file with a seemingly successful empty
	// layout. The user must be able to recover the existing encrypted data.
	if _, err := store.load(); err != nil {
		return err.Error()
	}
	raw, err := json.Marshal(layout)
	if err != nil {
		return err.Error()
	}
	if len(raw) > chatTabLayoutMaxBytes {
		return "chat tab layout exceeds size limit"
	}
	sealed, err := sealFile(raw, store.key)
	if err != nil {
		return err.Error()
	}
	if err := writePrivateFileAtomic(store.path, sealed); err != nil {
		return err.Error()
	}
	return ""
}

func (s chatTabLayoutStore) load() (ChatTabLayout, error) {
	result := ChatTabLayout{Order: []string{}, Pinned: []string{}}
	root, err := os.OpenRoot(filepath.Dir(s.path))
	if errors.Is(err, os.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	defer func() { _ = root.Close() }()
	file, err := root.Open(filepath.Base(s.path))
	if errors.Is(err, os.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	defer func() { _ = file.Close() }()
	limit := sealedAttachmentSizeForPlaintext(chatTabLayoutMaxBytes)
	blob, err := io.ReadAll(io.LimitReader(file, int64(limit)+1))
	if err != nil {
		return result, err
	}
	if len(blob) > limit {
		return result, errors.New("chat tab layout file exceeds size limit")
	}
	plain, err := openFileLimited(blob, s.key, chatTabLayoutMaxBytes)
	if err != nil || json.Unmarshal(plain, &result) != nil || !result.valid() {
		return ChatTabLayout{}, errors.New("chat tab layout is unreadable with this server and identity")
	}
	return result.canonicalized(), nil
}
