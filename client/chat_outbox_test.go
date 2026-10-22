package main

import (
	"bytes"
	"noxa/internal/netproto"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func outboxTestApp(t *testing.T) (*App, *connManager) {
	t.Helper()
	a, cm := dmContextTestApp(t)
	cm.mu.Lock()
	cm.addr = "example.com:12333"
	cm.fingerprint = "cert-a"
	cm.clientID = "self"
	cm.mu.Unlock()
	a.settings = DefaultSettings()
	cm.rememberOutboxServer()
	return a, cm
}
func TestChatOutboxSealedPersistenceAndDestinationIsolation(t *testing.T) {
	a, cm := outboxTestApp(t)
	request := ChatOutboxRequest{Action: "stage", Scope: "channel", Target: "7", Text: "outbox-private-canary", ReplyToID: 31}
	result, err := a.ChatOutboxForTab("a", request)
	if err != nil || len(result.Entries) != 1 {
		t.Fatalf("stage: %+v %v", result, err)
	}
	entry := result.Entries[0]
	if entry.State != "held" || entry.DueAt-entry.CreatedAt != 5000 || entry.ReplyToID != 31 {
		t.Fatalf("hold: %+v", entry)
	}
	store, err := a.outboxStore(cm)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(store.path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(request.Text)) || bytes.Contains(raw, []byte("channel")) {
		t.Fatal("outbox stored plaintext")
	}
	// A new App retains the same identity and file, without relying on frontend memory.
	reopened := appWithCM(cm)
	reopened.settingsPath = a.settingsPath
	reopened.tabs = map[string]*tabState{"a": {cm: cm}}
	reopened.activeID = "a"
	got, err := reopened.ChatOutboxForTab("a", ChatOutboxRequest{Action: "list"})
	if err != nil || len(got.Entries) != 1 || got.Entries[0].Text != request.Text {
		t.Fatal("restart lost queue", err)
	}
	cm.mu.Lock()
	cm.fingerprint = "cert-b"
	cm.mu.Unlock()
	got, err = a.ChatOutboxForTab("a", ChatOutboxRequest{Action: "list"})
	if err != nil || len(got.Entries) != 0 {
		t.Fatal("cross-certificate queue", err)
	}
	cm.mu.Lock()
	cm.fingerprint = "cert-a"
	cm.mu.Unlock()
	if _, err = a.ChatOutboxForTab("stale", request); err == nil {
		t.Fatal("stale tab accepted")
	}
	result, err = a.ChatOutboxForTab("a", ChatOutboxRequest{Action: "cancel", ID: entry.ID})
	if err != nil || len(result.Entries) != 0 {
		t.Fatal("cancel failed", err)
	}
}
func TestChatOutboxRequiresExplicitOfflineOptInAndStopsUncertainRetries(t *testing.T) {
	a, cm := outboxTestApp(t)
	cm.mu.Lock()
	cm.clientID = ""
	cm.mu.Unlock()
	request := ChatOutboxRequest{Action: "stage", Scope: "channel", Target: "7", Text: "queued"}
	if _, err := a.ChatOutboxForTab("a", request); err == nil {
		t.Fatal("implicitly queued offline")
	}
	request.QueueOffline = true
	result, err := a.ChatOutboxForTab("a", request)
	if err != nil || len(result.Entries) != 1 || result.Entries[0].State != "queued" {
		t.Fatal("explicit queue", err)
	}
	entry := result.Entries[0]
	if _, err := a.ChatOutboxForTab("a", ChatOutboxRequest{Action: "send", ID: entry.ID}); err != nil {
		t.Fatal(err)
	}
	cm.mu.Lock()
	cm.clientID = "self"
	cm.mu.Unlock()
	a.PumpChatOutbox() // no channel encryption key: must retain for explicit review
	result, err = a.ChatOutboxForTab("a", ChatOutboxRequest{Action: "list"})
	if err != nil || len(result.Entries) != 1 || result.Entries[0].State != "uncertain" {
		t.Fatalf("failure lost: %+v %v", result, err)
	}
	a.PumpChatOutbox()
	again, err := a.ChatOutboxForTab("a", ChatOutboxRequest{Action: "list"})
	if err != nil || len(again.Entries) != 1 || again.Entries[0].State != "uncertain" {
		t.Fatal("uncertain retried")
	}
}
func TestChatOutboxSendIsDelayedAndSingleFlight(t *testing.T) {
	var sends atomic.Int32
	a, cm := newPipedApp(t, func(frame *netproto.Frame) (netproto.MessageType, any, bool) {
		if netproto.MessageType(frame.Type) != netproto.MsgChatSend {
			return 0, nil, false
		}
		sends.Add(1)
		var msg netproto.ChatSend
		if err := netproto.Decode(frame, &msg); err != nil {
			t.Error(err)
		}
		return netproto.MsgChatAccepted, netproto.ChatAccepted{ClientMsgID: msg.ClientMsgID, ChannelID: msg.ChannelID, Disposition: netproto.ChatStored, MessageID: 5}, true
	})
	a.settingsPath = filepath.Join(t.TempDir(), "settings.json")
	a.settings = DefaultSettings()
	a.tabs = map[string]*tabState{"a": {cm: cm}}
	a.activeID = "a"
	cm.mu.Lock()
	cm.addr = "example.com:12333"
	cm.fingerprint = "cert-a"
	cm.clientID = "self"
	cm.authorizationModel = netproto.AuthorizationModelRolesV1
	cm.mu.Unlock()
	cm.scopeKeys.put(7, 1, randKey(t))
	result, err := a.ChatOutboxForTab("a", ChatOutboxRequest{Action: "stage", Scope: "channel", Target: "7", Text: "delayed"})
	if err != nil {
		t.Fatal(err)
	}
	a.PumpChatOutbox()
	if sends.Load() != 0 {
		t.Fatal("sent during undo window")
	}
	if _, err := a.ChatOutboxForTab("a", ChatOutboxRequest{Action: "send", ID: result.Entries[0].ID}); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { a.PumpChatOutbox(); close(done) }()
	a.PumpChatOutbox()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("pump stuck")
	}
	result, err = a.ChatOutboxForTab("a", ChatOutboxRequest{Action: "list"})
	if err != nil || len(result.Entries) != 0 || sends.Load() != 1 {
		t.Fatalf("send result %+v count %d err %v", result, sends.Load(), err)
	}
}
func TestChatOutboxInterruptedSendingRequiresManualReview(t *testing.T) {
	a, cm := outboxTestApp(t)
	store, err := a.outboxStore(cm)
	if err != nil {
		t.Fatal(err)
	}
	outboxMu.Lock()
	err = store.save([]ChatOutboxEntry{{ID: "interrupted", Scope: "global", Text: "private", State: "sending"}})
	outboxMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	result, err := a.ChatOutboxForTab("a", ChatOutboxRequest{Action: "list"})
	if err != nil || len(result.Entries) != 1 || result.Entries[0].State != "uncertain" {
		t.Fatal("interrupted auto-retried", err)
	}
	a.PumpChatOutbox()
}

func TestChatOutboxNamespaceSurvivesRealDisconnect(t *testing.T) {
	a, cm := outboxTestApp(t)
	result, err := a.ChatOutboxForTab("a", ChatOutboxRequest{Action: "stage", Scope: "channel", Target: "7", Text: "before drop"})
	if err != nil {
		t.Fatal(err)
	}
	id := result.Entries[0].ID
	cm.disconnect()
	result, err = a.ChatOutboxForTab("a", ChatOutboxRequest{Action: "stage", Scope: "channel", Target: "7", Text: "after drop", QueueOffline: true})
	if err != nil || len(result.Entries) != 2 || result.Entries[0].ID != id {
		t.Fatalf("lost authenticated namespace: %+v %v", result, err)
	}
}
func TestChatOutboxCorruptionCanBeBackedUpWithoutLosingCiphertext(t *testing.T) {
	a, cm := outboxTestApp(t)
	store, err := a.outboxStore(cm)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(store.path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.path, []byte("damaged-ciphertext"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.ChatOutboxForTab("a", ChatOutboxRequest{Action: "list"}); err == nil {
		t.Fatal("ignored damaged file")
	}
	if _, err := a.ChatOutboxForTab("a", ChatOutboxRequest{Action: "reset"}); err != nil {
		t.Fatal(err)
	}
	backups, err := filepath.Glob(store.path + ".backup-*")
	if err != nil || len(backups) != 1 {
		t.Fatal("backup missing", err)
	}
	raw, err := os.ReadFile(backups[0])
	if err != nil || string(raw) != "damaged-ciphertext" {
		t.Fatal("changed backup", err)
	}
	if _, err := a.ChatOutboxForTab("a", ChatOutboxRequest{Action: "stage", Scope: "global", Text: "usable again"}); err != nil {
		t.Fatal(err)
	}
}
