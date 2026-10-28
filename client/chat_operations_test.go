package main

import (
	"errors"
	"strconv"
	"testing"

	"noxa/internal/e2ee/admission"
)

// 1c-iii-a2 review P2-1/P2-2: an uncertain composer send survives a restart
// as an "uncertain" outbox entry under its SAME operation ID; only an
// explicit Retry of that entry resolves it. A new message with identical text
// is a new operation (its own ID), and success forgets the entry.
func TestUncertainComposerSendBecomesOutboxEntry(t *testing.T) {
	a, cm := outboxTestApp(t)
	cm.mu.Lock()
	cm.ratchetTest = &ratchetTestConfig{}
	cm.mu.Unlock()
	uncertain := &ratchetError{Code: "uncertain", Committed: &RatchetOutcome{}}
	if !ratchetUncertain(uncertain) || ratchetUncertain(errRatchetUnavailable) || ratchetUncertain(&ratchetError{Code: "rejected", Committed: &RatchetOutcome{Receipt: &admission.Receipt{}}}) {
		t.Fatal("uncertainty classification")
	}
	if ratchetUncertain(errors.New("x")) {
		t.Fatal("plain errors are not uncertain")
	}
	store, err := a.outboxStore(cm)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.keepOutboxOperation(cm, store, "direct", "bob", "ok", "op-1", "not confirmed"); err != nil {
		t.Fatal(err)
	}
	if err := a.keepOutboxOperation(cm, store, "direct", "bob", "ok", "op-2", "not confirmed"); err != nil { // a NEW identical message
		t.Fatal(err)
	}
	// A store captured for another binding is refused, never written.
	other := store
	other.path += ".other"
	if err := a.keepOutboxOperation(cm, other, "direct", "bob", "ok", "op-3", "x"); err == nil {
		t.Fatal("an outbox captured for another server must be refused")
	}
	list, err := a.ChatOutboxForTab("a", ChatOutboxRequest{Action: "list"})
	if err != nil || len(list.Entries) != 2 {
		t.Fatalf("entries = %+v %v", list.Entries, err)
	}
	for _, e := range list.Entries {
		if e.State != "uncertain" || !e.Attempted || e.Text != "ok" || (e.ID != "op-1" && e.ID != "op-2") {
			t.Fatalf("entry = %+v", e)
		}
	}
	// Explicit retry keeps the operation ID (the pump resolves the original).
	retried, err := a.ChatOutboxForTab("a", ChatOutboxRequest{Action: "retry", ID: "op-1"})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range retried.Entries {
		if e.ID == "op-1" && (e.State != "queued" || !e.Attempted) {
			t.Fatalf("retry = %+v", e)
		}
	}
	// A later success of the same operation forgets it.
	a.forgetOutboxOperation(cm, store, "op-1")
	list, _ = a.ChatOutboxForTab("a", ChatOutboxRequest{Action: "list"})
	if len(list.Entries) != 1 || list.Entries[0].ID != "op-2" {
		t.Fatalf("after success = %+v", list.Entries)
	}
}

// Fix-review P3-5: uncertain entries are kept beyond the regular 100 (up to
// outboxUncertainCap) and a full outbox is reported, never silently dropped.
func TestUncertainComposerEntriesAreNeverSilentlyDropped(t *testing.T) {
	a, cm := outboxTestApp(t)
	cm.mu.Lock()
	cm.ratchetTest = &ratchetTestConfig{}
	cm.mu.Unlock()
	store, err := a.outboxStore(cm)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < outboxUncertainCap; i++ {
		if err := a.keepOutboxOperation(cm, store, "direct", "bob", "m", "op-"+strconv.Itoa(i), "x"); err != nil {
			t.Fatalf("entry %d: %v", i, err)
		}
	}
	if err := a.keepOutboxOperation(cm, store, "direct", "bob", "m", "op-full", "x"); err == nil {
		t.Fatal("a full outbox must be reported")
	}
	if list, err := a.ChatOutboxForTab("a", ChatOutboxRequest{Action: "list"}); err != nil || len(list.Entries) != outboxUncertainCap {
		t.Fatalf("entries = %d %v", len(list.Entries), err)
	}
}
