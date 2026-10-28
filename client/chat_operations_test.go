package main

import (
	"os"
	"strings"
	"testing"
	"time"
)

// 1c-iii-a item 4 (native): composer operation IDs of failed new-protocol
// direct sends persist (sealed, bound to the outbox namespace) so a retry of
// the same text after a restart resolves the original; success forgets it.
func TestComposerOperationIDsPersistSealed(t *testing.T) {
	a, cm := outboxTestApp(t)
	if got := a.ComposerOperationForTab("a", "direct", "bob", "hello", 0); got != "" {
		t.Fatalf("legacy-configured tab must not recover IDs: %q", got)
	}
	cm.mu.Lock()
	cm.ratchetTest = &ratchetTestConfig{}
	cm.mu.Unlock()
	a.recordComposerOperation(cm, "direct", "bob", "hello", 0, "op-123", false)
	if got := a.ComposerOperationForTab("a", "direct", "bob", "hello", 0); got != "op-123" {
		t.Fatalf("recovered = %q", got)
	}
	for _, other := range [][2]string{{"bob", "hello!"}, {"eve", "hello"}} {
		if got := a.ComposerOperationForTab("a", "direct", other[0], other[1], 0); got != "" {
			t.Fatalf("another text/destination must not reuse it: %v -> %q", other, got)
		}
	}
	store, err := a.outboxStore(cm)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(store.operationsPath())
	if err != nil || strings.Contains(string(raw), "op-123") || strings.Contains(string(raw), "hello") {
		t.Fatalf("operations file must be sealed: %v", err)
	}
	// Expired entries are dropped.
	ops, _ := store.loadOperations(time.Now().Add(composerOperationTTL + time.Minute))
	if len(ops) != 0 {
		t.Fatal("expired operation kept")
	}
	// A successful send forgets it.
	a.recordComposerOperation(cm, "direct", "bob", "hello", 0, "op-123", true)
	if got := a.ComposerOperationForTab("a", "direct", "bob", "hello", 0); got != "" {
		t.Fatalf("sent operation still recovered: %q", got)
	}
	// Bounded.
	for i := 0; i < composerOperationMax+10; i++ {
		a.recordComposerOperation(cm, "direct", "bob", "m"+strings.Repeat("x", i), 0, "op-"+strings.Repeat("y", i%50+1), false)
	}
	if ops, _ := store.loadOperations(time.Now()); len(ops) != composerOperationMax {
		t.Fatalf("bound: %d", len(ops))
	}
}
