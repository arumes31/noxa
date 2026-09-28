package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"noxa/internal/netproto"
)

func TestSavedMessageReadsStayInsideTheirDirectory(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "saved-messages")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(parent, "outside.json"), []byte(`[]`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readSavedMessageReferences(dir, "../outside.json"); err == nil {
		t.Fatal("read escaped the saved-message directory")
	}
	if err := os.WriteFile(filepath.Join(dir, "bounded.json"), []byte(strings.Repeat("x", 2*1024*1024)), 0600); err != nil {
		t.Fatal(err)
	}
	raw, err := readSavedMessageReferences(dir, "bounded.json")
	if err != nil || len(raw) != 1024*1024+1 {
		t.Fatalf("read limit: got %d bytes, error %v", len(raw), err)
	}
}

func TestSavedMessagesPersistReferencesAndIsolateServers(t *testing.T) {
	a, cm := newPipedApp(t, func(*netproto.Frame) (netproto.MessageType, any, bool) { return 0, nil, false })
	a.settingsPath = filepath.Join(t.TempDir(), "settings.json")
	a.tabs = map[string]*tabState{"a": {cm: cm}}
	a.activeID = "a"
	cm.addr = "example.org:1234"
	cm.fingerprint = "server-one"
	ref := SavedMessageReference{Kind: "channel", ChannelID: 7, MessageID: 31, Collection: "Work"}
	got, err := a.SavedMessagesForTab("a", SavedMessageRequest{Action: "save", Reference: ref})
	if err != nil || len(got.References) != 1 {
		t.Fatalf("save=%+v %v", got, err)
	}
	got, err = a.SavedMessagesForTab("a", SavedMessageRequest{Action: "list"})
	if err != nil || len(got.References) != 1 || got.References[0].Collection != "Work" {
		t.Fatalf("list=%+v %v", got, err)
	}
	cm.fingerprint = "server-two"
	got, err = a.SavedMessagesForTab("a", SavedMessageRequest{Action: "list"})
	if err != nil || len(got.References) != 0 {
		t.Fatalf("cross-server refs=%+v %v", got, err)
	}
	if _, err = a.SavedMessagesForTab("stale", SavedMessageRequest{Action: "save", Reference: ref}); err == nil {
		t.Fatal("accepted stale tab")
	}
	entries, _ := os.ReadDir(filepath.Join(filepath.Dir(a.settingsPath), "saved-messages"))
	for _, entry := range entries {
		raw, _ := os.ReadFile(filepath.Join(filepath.Dir(a.settingsPath), "saved-messages", entry.Name()))
		var refs []map[string]any
		if json.Unmarshal(raw, &refs) != nil {
			t.Fatal("bad refs file")
		}
		for _, r := range refs {
			for key := range r {
				if strings.Contains(key, "body") || strings.Contains(key, "key") {
					t.Fatalf("sensitive field %s", key)
				}
			}
		}
	}
}

func TestHistorySearchPageFiltersDecryptedHistory(t *testing.T) {
	key := randKey(t)
	a, cm := newPipedApp(t, func(*netproto.Frame) (netproto.MessageType, any, bool) {
		return netproto.MsgChatHistoryResponse, netproto.ChatHistoryResponse{Messages: []netproto.ChatHistoryEntry{
			{ID: 4, FromUniqueID: "alice", FromNickname: "Alice", SentAt: 120, BodyEnc: mustSeal(t, "report [file:payload]", key), KeyID: 3},
			{ID: 3, FromUniqueID: "bob", SentAt: 110, BodyEnc: mustSeal(t, "report", key), KeyID: 3},
			{ID: 2, FromUniqueID: "alice", SentAt: 90, BodyEnc: mustSeal(t, "report", key), KeyID: 3},
			{ID: 1, Body: "report"},
		}}, true
	})
	a.tabs = map[string]*tabState{"a": {cm: cm}}
	a.activeID = "a"
	cm.scopeKeys.put(7, 3, key)
	got, err := a.HistorySearchPageForTab("a", HistorySearchFilter{ChannelID: 7, Query: "REPORT", Sender: "alice", After: 100, HasAttachment: true})
	if err != nil || len(got.Messages) != 1 || got.Messages[0].ID != 4 || got.Undecryptable != 1 || !got.Complete {
		t.Fatalf("search=%+v %v", got, err)
	}
	if got.Messages[0].BodyEnc != "" || got.Messages[0].KeyID != 0 {
		t.Fatal("wire material exposed")
	}
	if _, err = a.HistorySearchPageForTab("stale", HistorySearchFilter{ChannelID: 7}); err == nil {
		t.Fatal("accepted stale tab")
	}
}

func TestHistorySearchRetainsOriginAcrossTabChange(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	key := randKey(t)
	a, cm := newPipedApp(t, func(frame *netproto.Frame) (netproto.MessageType, any, bool) {
		var request netproto.ChatHistory
		if err := netproto.Decode(frame, &request); err != nil || request.ChannelID != 7 || request.BeforeID != 30 {
			t.Errorf("history request=%+v error=%v", request, err)
		}
		close(entered)
		<-release
		return netproto.MsgChatHistoryResponse, netproto.ChatHistoryResponse{Messages: []netproto.ChatHistoryEntry{{ID: 29, BodyEnc: mustSeal(t, "original secret", key), KeyID: 3}}}, true
	})
	other, _ := newPipedApp(t, func(*netproto.Frame) (netproto.MessageType, any, bool) {
		t.Error("search reached other server")
		return 0, nil, false
	})
	a.tabs = map[string]*tabState{"a": {cm: cm}, "b": {cm: other.cmLoad()}}
	a.activeID = "a"
	cm.scopeKeys.put(7, 3, key)
	type answer struct {
		page HistorySearchPage
		err  error
	}
	done := make(chan answer, 1)
	go func() {
		page, err := a.HistorySearchPageForTab("a", HistorySearchFilter{ChannelID: 7, BeforeID: 30, Query: "secret"})
		done <- answer{page, err}
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("search did not start")
	}
	a.tabsMu.Lock()
	a.activateLocked("b")
	a.tabsMu.Unlock()
	close(release)
	select {
	case got := <-done:
		if got.err != nil || len(got.page.Messages) != 1 || got.page.Messages[0].Body != "original secret" {
			t.Fatalf("result=%+v %v", got.page, got.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("search did not finish")
	}
}

func TestHistorySearchRejectsRepeatedCursor(t *testing.T) {
	a, cm := newPipedApp(t, func(*netproto.Frame) (netproto.MessageType, any, bool) {
		return netproto.MsgChatHistoryResponse, netproto.ChatHistoryResponse{Messages: []netproto.ChatHistoryEntry{{ID: 30}}}, true
	})
	a.tabs = map[string]*tabState{"a": {cm: cm}}
	a.activeID = "a"
	if _, err := a.HistorySearchPageForTab("a", HistorySearchFilter{ChannelID: 7, BeforeID: 30}); err == nil {
		t.Fatal("accepted repeated cursor")
	}
}

func TestSavedDirectMessagesRequireOriginalHistoryOwner(t *testing.T) {
	a, cm := newPipedApp(t, func(*netproto.Frame) (netproto.MessageType, any, bool) { return 0, nil, false })
	a.settingsPath = filepath.Join(t.TempDir(), "settings.json")
	a.tabs = map[string]*tabState{"a": {cm: cm}}
	a.activeID = "a"
	cm.addr = "example.org:1234"
	owner, err := a.DMHistoryContextForTab("a")
	if err != nil {
		t.Fatal(err)
	}
	ref := SavedMessageReference{Kind: "dm", PeerID: "bob", ClientMessageID: "message-uuid"}
	if _, err := a.SavedMessagesForTab("a", SavedMessageRequest{Action: "save", Reference: ref}); err == nil {
		t.Fatal("accepted DM without history owner")
	}
	if got, err := a.SavedMessagesForTab("a", SavedMessageRequest{Action: "save", Reference: ref, DMOwner: &owner}); err != nil || len(got.References) != 1 {
		t.Fatalf("save=%+v %v", got, err)
	}
	a.identityMu.Lock()
	a.identityGeneration++
	a.identityMu.Unlock()
	if _, err := a.SavedMessagesForTab("a", SavedMessageRequest{Action: "remove", Reference: ref, DMOwner: &owner}); err == nil {
		t.Fatal("stale identity removed DM reference")
	}
}
