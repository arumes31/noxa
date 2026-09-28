package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"noxa/internal/netproto"
)

// SavedMessageReference deliberately contains no message text or encryption material.
// Resolving a reference always rechecks the server's current history permissions.
type SavedMessageReference struct {
	Kind            string `json:"kind"`
	ChannelID       int64  `json:"channel_id,omitempty"`
	ThreadID        int64  `json:"thread_id,omitempty"`
	GroupID         string `json:"group_id,omitempty"`
	PeerID          string `json:"peer_id,omitempty"`
	ClientMessageID string `json:"client_message_id,omitempty"`
	LocalSeq        int64  `json:"local_seq,omitempty"`
	MessageID       int64  `json:"message_id"`
	Collection      string `json:"collection"`
	SavedAt         int64  `json:"saved_at"`
}
type SavedMessageRequest struct {
	Action    string                `json:"action"`
	Reference SavedMessageReference `json:"reference"`
	DMOwner   *DMHistoryContext     `json:"dm_owner,omitempty"`
}
type SavedMessageResult struct {
	References []SavedMessageReference `json:"references"`
}

var savedMessagesMu sync.Mutex

func (r SavedMessageReference) valid() bool {
	if utf8.RuneCountInString(r.Collection) > 60 || strings.IndexFunc(r.Collection, unicode.IsControl) >= 0 {
		return false
	}
	if r.Kind == "dm" {
		return r.MessageID == 0 && r.ChannelID == 0 && r.ThreadID == 0 && r.GroupID == "" && len(r.PeerID) > 0 && len(r.PeerID) <= 128 && len(r.ClientMessageID) <= 128 && r.LocalSeq >= 0 && (r.ClientMessageID != "" || r.LocalSeq > 0)
	}
	if r.MessageID <= 0 || r.PeerID != "" || r.ClientMessageID != "" || r.LocalSeq != 0 {
		return false
	}
	switch r.Kind {
	case "channel":
		return r.ChannelID >= 0 && r.ThreadID == 0 && r.GroupID == ""
	case "thread":
		return r.ChannelID > 0 && r.ThreadID > 0 && r.GroupID == ""
	case "group":
		return r.ChannelID == 0 && r.ThreadID == 0 && len(r.GroupID) > 0 && len(r.GroupID) <= 128
	}
	return false
}
func (r SavedMessageReference) same(other SavedMessageReference) bool {
	if r.Kind == "dm" || other.Kind == "dm" {
		if r.Kind != other.Kind || r.PeerID != other.PeerID {
			return false
		}
		if r.ClientMessageID != "" {
			return r.ClientMessageID == other.ClientMessageID
		}
		return r.LocalSeq == other.LocalSeq && other.ClientMessageID == ""
	}
	return r.Kind == other.Kind && r.ChannelID == other.ChannelID && r.ThreadID == other.ThreadID && r.GroupID == other.GroupID && r.MessageID == other.MessageID
}

func (a *App) SavedMessagesForTab(tabID string, request SavedMessageRequest) (SavedMessageResult, error) {
	result := SavedMessageResult{References: []SavedMessageReference{}}
	if request.Action != "list" && request.Action != "save" && request.Action != "remove" && request.Action != "move" {
		return result, errors.New("invalid saved message action")
	}
	request.Reference.Collection = strings.TrimSpace(request.Reference.Collection)
	if request.Action != "list" && !request.Reference.valid() {
		return result, errors.New("invalid saved message reference")
	}
	cm, err := a.requireTabCM(tabID)
	if err != nil {
		return result, err
	}
	base := a.settingsFile()
	if base == "" {
		return result, errors.New("local storage unavailable")
	}
	identity, err := cm.identity()
	if err != nil {
		return result, err
	}
	pub, _, err := identity.x25519()
	if err != nil {
		return result, err
	}
	if request.Reference.Kind == "dm" && request.Action != "list" {
		if request.DMOwner == nil || request.DMOwner.TabID != tabID {
			return result, errDMHistoryContextChanged
		}
		dmStore, err := a.dmHistoryForContext(*request.DMOwner)
		if err != nil {
			return result, err
		}
		pub = dmStore.publicKey
	}
	cm.mu.Lock()
	addr, fingerprint := cm.addr, cm.fingerprint
	cm.mu.Unlock()
	if addr == "" {
		return result, errors.New("server identity unavailable")
	}
	canonical, err := normalizeServerAddr(addr)
	if err != nil {
		return result, err
	}
	owner := sha256.Sum256([]byte(fmt.Sprintf("%x\x00%s\x00%s", pub, canonical, fingerprint)))
	dir := filepath.Join(filepath.Dir(base), "saved-messages")
	path := filepath.Join(dir, hex.EncodeToString(owner[:])+".json")
	savedMessagesMu.Lock()
	defer savedMessagesMu.Unlock()
	raw, err := readSavedMessageReferences(dir, filepath.Base(path))
	if err == nil {
		if len(raw) > 1024*1024 || json.Unmarshal(raw, &result.References) != nil {
			return result, errors.New("invalid saved messages file")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return result, err
	}
	if len(result.References) > 1000 {
		return SavedMessageResult{}, errors.New("saved messages limit exceeded")
	}
	if request.Action == "list" {
		return result, nil
	}
	index := -1
	for i, r := range result.References {
		if r.same(request.Reference) {
			index = i
			break
		}
	}
	switch request.Action {
	case "remove":
		if index >= 0 {
			result.References = append(result.References[:index], result.References[index+1:]...)
		}
	case "move":
		if index < 0 {
			return result, errors.New("saved message no longer exists")
		}
		result.References[index].Collection = request.Reference.Collection
	case "save":
		if index >= 0 {
			return result, nil
		}
		if len(result.References) >= 1000 {
			return result, errors.New("saved messages limit reached (1000)")
		}
		request.Reference.SavedAt = time.Now().Unix()
		result.References = append([]SavedMessageReference{request.Reference}, result.References...)
	}
	raw, err = json.Marshal(result.References)
	if err != nil {
		return result, err
	}
	if err = os.MkdirAll(dir, 0700); err != nil {
		return result, err
	}
	if err = writePrivateFileAtomic(path, raw); err != nil {
		return result, err
	}
	return result, nil
}

func readSavedMessageReferences(dir, name string) ([]byte, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	file, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	return io.ReadAll(io.LimitReader(file, 1024*1024+1))
}

type HistorySearchFilter struct {
	ChannelID     int64  `json:"channel_id"`
	ThreadID      int64  `json:"thread_id"`
	BeforeID      int64  `json:"before_id"`
	Query         string `json:"query"`
	Sender        string `json:"sender"`
	After         int64  `json:"after"`
	Before        int64  `json:"before"`
	HasAttachment bool   `json:"has_attachment"`
}
type HistorySearchPage struct {
	Messages      []netproto.ChatHistoryEntry `json:"messages"`
	Scanned       int                         `json:"scanned"`
	Undecryptable int                         `json:"undecryptable"`
	Complete      bool                        `json:"complete"`
	NextBeforeID  int64                       `json:"next_before_id"`
}

// HistorySearchPageForTab scans one bounded page; the UI can stop between pages
// and resume beyond its initial budget without retaining plaintext on disk.
func (a *App) HistorySearchPageForTab(tabID string, filter HistorySearchFilter) (HistorySearchPage, error) {
	result := HistorySearchPage{Messages: []netproto.ChatHistoryEntry{}}
	if filter.ChannelID < 0 || filter.ThreadID < 0 || filter.BeforeID < 0 || filter.After < 0 || filter.Before < 0 || len(filter.Query) > 1024 || len(filter.Sender) > 256 || (filter.After > 0 && filter.Before > 0 && filter.After > filter.Before) {
		return result, errors.New("invalid history search filter")
	}
	cm, err := a.requireTabCM(tabID)
	if err != nil {
		return result, err
	}
	var entries []netproto.ChatHistoryEntry
	if filter.ThreadID > 0 {
		if filter.ChannelID <= 0 {
			return result, errors.New("thread requires a channel")
		}
		request := netproto.DiscussionRequest{Action: "history", ChannelID: filter.ChannelID, ThreadID: filter.ThreadID, BeforeID: filter.BeforeID}
		frame, err := cm.request(netproto.MsgDiscussionRequest, netproto.MsgDiscussionResult, request, 10*time.Second)
		if err != nil {
			return result, err
		}
		var page netproto.DiscussionResult
		if err = netproto.Decode(frame, &page); err != nil {
			return result, err
		}
		if page.Action != request.Action || page.ChannelID != request.ChannelID || page.ThreadID != request.ThreadID || len(page.Messages) > 50 {
			return result, errors.New("invalid discussion history response")
		}
		refused := installPageKeys(cm, filter.ChannelID, page.Keys, page.Refused)
		for i := range page.Messages {
			openChatEntry(cm, filter.ChannelID, &page.Messages[i], refused)
		}
		entries = page.Messages
		result.Complete = !page.HasMore
	} else {
		page, err := chatHistoryWith(cm, filter.ChannelID, filter.BeforeID, chatSearchPage)
		if err != nil {
			return result, err
		}
		entries = page.Messages
		result.Complete = len(entries) < chatSearchPage
	}
	query, sender := strings.ToLower(strings.TrimSpace(filter.Query)), strings.ToLower(strings.TrimSpace(filter.Sender))
	for _, entry := range entries {
		if entry.ID <= 0 || (filter.BeforeID > 0 && entry.ID >= filter.BeforeID) {
			return HistorySearchPage{}, errors.New("history cursor did not advance")
		}
		if result.NextBeforeID == 0 || entry.ID < result.NextBeforeID {
			result.NextBeforeID = entry.ID
		}
		result.Scanned++
		if entry.Deleted {
			continue
		}
		if !entry.EncVerified {
			result.Undecryptable++
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(entry.Body), query) {
			continue
		}
		if sender != "" && !strings.Contains(strings.ToLower(entry.FromNickname), sender) && strings.ToLower(entry.FromUniqueID) != sender {
			continue
		}
		if filter.After > 0 && entry.SentAt < filter.After || filter.Before > 0 && entry.SentAt > filter.Before {
			continue
		}
		if filter.HasAttachment && !strings.Contains(entry.Body, "[file:") {
			continue
		}
		result.Messages = append(result.Messages, entry)
	}
	if len(entries) == 0 {
		result.Complete = true
	}
	return result, nil
}
