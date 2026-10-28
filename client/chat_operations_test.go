package main

import (
	"errors"
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
	a.keepOutboxOperation(cm, "direct", "bob", "ok", "op-1", "not confirmed")
	a.keepOutboxOperation(cm, "direct", "bob", "ok", "op-2", "not confirmed") // a NEW identical message
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
	a.forgetOutboxOperation(cm, "op-1")
	list, _ = a.ChatOutboxForTab("a", ChatOutboxRequest{Action: "list"})
	if len(list.Entries) != 1 || list.Entries[0].ID != "op-2" {
		t.Fatalf("after success = %+v", list.Entries)
	}
}
