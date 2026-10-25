package main

import (
	"context"
	"sync"
	"testing"
	"time"
)

func captureContactIdentityChanges(t *testing.T) func() []ContactIdentityChange {
	t.Helper()
	var mu sync.Mutex
	var emitted []ContactIdentityChange
	previous := peerConfirmationEmitter
	peerConfirmationEmitter = func(_ context.Context, change ContactIdentityChange) {
		mu.Lock()
		emitted = append(emitted, change)
		mu.Unlock()
	}
	t.Cleanup(func() { peerConfirmationEmitter = previous })
	return func() []ContactIdentityChange {
		mu.Lock()
		defer mu.Unlock()
		return append([]ContactIdentityChange(nil), emitted...)
	}
}

func waitForContactIdentityChange(t *testing.T, emitted func() []ContactIdentityChange) ContactIdentityChange {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if changes := emitted(); len(changes) > 0 {
			return changes[len(changes)-1]
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("no contact_identity_changed event emitted")
	return ContactIdentityChange{}
}

func TestFrontendPeerConfirmationWaitsForDialogAnswer(t *testing.T) {
	emitted := captureContactIdentityChanges(t)
	confirm := frontendPeerConfirmation(context.Background())
	results := make(chan bool, 1)
	go func() { results <- confirm("chat.example.test:9987", "peer", "old-fp", "new-fp") }()
	change := waitForContactIdentityChange(t, emitted)
	if change.RequestID == "" || change.Server != "chat.example.test:9987" || change.Contact != "peer" || change.OldFingerprint != "old-fp" || change.NewFingerprint != "new-fp" {
		t.Fatalf("event payload = %+v", change)
	}
	app := &App{}
	if err := app.AnswerContactIdentityChange("not-a-request", true); err == "" {
		t.Fatal("unknown request accepted")
	}
	if err := app.AnswerContactIdentityChange(change.RequestID, true); err != "" {
		t.Fatalf("answer rejected: %s", err)
	}
	select {
	case trusted := <-results:
		if !trusted {
			t.Fatal("explicit trust was not delivered")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("confirmation did not return after the answer")
	}
	if err := app.AnswerContactIdentityChange(change.RequestID, true); err == "" {
		t.Fatal("a request answered twice")
	}
}

func TestFrontendPeerConfirmationDefaultsToBlocking(t *testing.T) {
	emitted := captureContactIdentityChanges(t)
	previous := peerConfirmationTimeout
	peerConfirmationTimeout = 20 * time.Millisecond
	t.Cleanup(func() { peerConfirmationTimeout = previous })
	confirm := frontendPeerConfirmation(context.Background())
	if confirm("srv", "peer", "old", "new") {
		t.Fatal("timeout trusted the new key")
	}
	change := waitForContactIdentityChange(t, emitted)
	if err := (&App{}).AnswerContactIdentityChange(change.RequestID, true); err == "" {
		t.Fatal("late answer accepted after timeout")
	}

	peerConfirmationTimeout = time.Minute
	ctx, cancel := context.WithCancel(context.Background())
	cancelled := frontendPeerConfirmation(ctx)
	results := make(chan bool, 1)
	go func() { results <- cancelled("srv", "peer", "old", "new") }()
	waitForContactIdentityChange(t, func() []ContactIdentityChange {
		changes := emitted()
		if len(changes) < 2 {
			return nil
		}
		return changes
	})
	cancel()
	select {
	case trusted := <-results:
		if trusted {
			t.Fatal("shutdown trusted the new key")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("confirmation did not return on shutdown")
	}
	if frontendPeerConfirmation(nil) != nil {
		t.Fatal("nil context must disable prompting")
	}
}
