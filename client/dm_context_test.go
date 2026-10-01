package main

import (
	"strings"
	"testing"

	"noxa/internal/netproto"
)

func dmContextTestApp(t *testing.T) (*App, *connManager) {
	t.Helper()
	_, cm := newPipedApp(t, func(*netproto.Frame) (netproto.MessageType, any, bool) { return 0, nil, false })
	a := newLocalApp(t, cm)
	a.tabs = map[string]*tabState{"a": {cm: cm}}
	a.activeID = "a"
	return a, cm
}

// replaceDMIdentityForTest simulates an unexpected native manager rekey under
// identityMu. Normal identity selection now preserves established tab keys.
func replaceDMIdentityForTest(a *App, id *identity) {
	cm := a.cmLoad()
	cm.mu.Lock()
	cm.id = id
	cm.mu.Unlock()
	a.invalidateIdentityContexts()
}

func TestDMContextRejectsStaleOperations(t *testing.T) {
	for _, change := range []string{"tab", "reactivation", "identity", "identity round trip", "missing context"} {
		t.Run(change, func(t *testing.T) {
			a, cm := dmContextTestApp(t)
			owner, err := a.DMHistoryContextForTab("a")
			if err != nil {
				t.Fatal(err)
			}
			if err := a.DMHistoryAppendForContext(owner, "peer", "Peer", DMEntry{Body: "retained", SentAt: 1}); err != "" {
				t.Fatal(err)
			}
			originalStore, err := a.dmHistoryForContext(owner)
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "tab":
				a.tabsMu.Lock()
				a.activateLocked("")
				a.tabsMu.Unlock()
			case "reactivation":
				a.tabsMu.Lock()
				a.activateLocked("")
				a.activateLocked("a")
				a.tabsMu.Unlock()
			case "identity", "identity round trip":
				original, err := cm.identity()
				if err != nil {
					t.Fatal(err)
				}
				a.identityMu.Lock()
				replaceDMIdentityForTest(a, mustTempIdentity(t))
				if change == "identity round trip" {
					replaceDMIdentityForTest(a, original)
				}
				a.identityMu.Unlock()
			case "missing context":
				owner = DMHistoryContext{}
			}
			if _, err := a.DMHistoryLoadForContext(owner, "peer"); err == nil {
				t.Error("stale load accepted")
			}
			if _, err := a.DMHistoryPeersForContext(owner); err == nil {
				t.Error("stale peers accepted")
			}
			if _, err := a.DMSearchForContext(owner, "", "retained", 0); err == nil {
				t.Error("stale search accepted")
			}
			if _, err := a.DMExportHistoryForContext(owner, "peer"); err == nil {
				t.Error("stale export accepted")
			}
			if err := a.DMHistoryAppendForContext(owner, "peer", "Peer", DMEntry{Body: "wrong"}); err == "" {
				t.Error("stale append accepted")
			}
			if err := a.DMHistoryClearForContext(owner, "peer"); err == "" {
				t.Error("stale clear accepted")
			}
			// Inspect the original file independently of current ownership.
			log, err := originalStore.load("peer")
			if err != nil || len(log.Messages) != 1 || log.Messages[0].Body != "retained" {
				t.Fatalf("stale mutation changed history: %+v / %v", log, err)
			}
		})
	}
}

func TestDMContextUsesTabIdentityAndRetainsCapturedReads(t *testing.T) {
	for _, operation := range []string{"load", "search", "export"} {
		t.Run(operation, func(t *testing.T) {
			a, cm := dmContextTestApp(t)
			id, err := cm.identity()
			if err != nil {
				t.Fatal(err)
			}
			uid, err := id.uniqueID()
			if err != nil {
				t.Fatal(err)
			}
			owner, err := a.DMHistoryContextForTab("a")
			if err != nil || owner.IdentityUID != uid || owner.TabID != "a" {
				t.Fatalf("wrong context: %+v / %v", owner, err)
			}
			if err := a.DMHistoryAppendForContext(owner, "peer", "Peer", DMEntry{Body: "original", SentAt: 1}); err != "" {
				t.Fatal(err)
			}
			other := mustTempIdentity(t)
			oldHook := dmBeforeRead
			dmBeforeRead = func() {
				a.identityMu.Lock()
				replaceDMIdentityForTest(a, other)
				a.identityMu.Unlock()
			}
			t.Cleanup(func() { dmBeforeRead = oldHook })
			switch operation {
			case "load":
				rows, err := a.DMHistoryLoadForContext(owner, "peer")
				if err != nil || len(rows) != 1 || rows[0].Body != "original" {
					t.Fatalf("accepted load lost captured identity: %+v / %v", rows, err)
				}
			case "search":
				result, err := a.DMSearchForContext(owner, "", "original", 0)
				if err != nil || len(result.Messages) != 1 || result.Messages[0].Body != "original" {
					t.Fatalf("accepted search lost captured identity: %+v / %v", result, err)
				}
			case "export":
				result, err := a.DMExportHistoryForContext(owner, "peer")
				if err != nil || !strings.Contains(result.Text, "original") {
					t.Fatalf("accepted export lost captured identity: %+v / %v", result, err)
				}
			}
		})
	}
}

func TestDMContextOfflineUsesAppSelectedIdentity(t *testing.T) {
	a := identityTestApp(t, "off")
	first, err := a.DMHistoryContextForTab("")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.DMHistoryAppendForContext(first, "peer", "First", DMEntry{Body: "first identity"}); err != "" {
		t.Fatal(err)
	}
	if err := a.CreateIdentity("second"); err != "" {
		t.Fatal(err)
	}
	if err := a.SwitchIdentity("second"); err != "" {
		t.Fatal(err)
	}
	second, err := a.DMHistoryContextForTab("")
	if err != nil || second.IdentityUID == first.IdentityUID || second.IdentityUID != a.IdentityUID() {
		t.Fatalf("wrong offline owner: %+v / %v", second, err)
	}
	peers, err := a.DMHistoryPeersForContext(second)
	if err != nil || len(peers) != 0 {
		t.Fatalf("foreign offline peers: %+v / %v", peers, err)
	}
	if _, err := a.DMHistoryLoadForContext(first, "peer"); err == nil {
		t.Fatal("old offline context accepted after switch")
	}
	if err := a.DMHistoryAppendForContext(second, "peer", "Second", DMEntry{Body: "second identity"}); err != "" {
		t.Fatal(err)
	}
	search, err := a.DMSearchForContext(second, "", "second", 0)
	if err != nil || len(search.Messages) != 1 || search.Messages[0].Body != "second identity" {
		t.Fatalf("offline search: %+v / %v", search, err)
	}
	if err := a.DMHistoryClearForContext(second, "peer"); err != "" {
		t.Fatal(err)
	}
	rows, err := a.DMHistoryLoadForContext(second, "peer")
	if err != nil || len(rows) != 0 {
		t.Fatalf("cleared offline history: %+v / %v", rows, err)
	}
}

func TestDMContextRejectsWrongTabAndUnconfiguredStorage(t *testing.T) {
	a, _ := dmContextTestApp(t)
	for _, tab := range []string{"", "missing"} {
		if _, err := a.DMHistoryContextForTab(tab); err == nil {
			t.Errorf("context accepted wrong tab %q", tab)
		}
	}
	if _, err := (&App{}).DMHistoryContextForTab(""); err == nil {
		t.Fatal("context accepted missing storage path")
	}
}

func TestDMContextWriterRetainsOwnerWhileWaiting(t *testing.T) {
	for _, operation := range []string{"append", "clear"} {
		t.Run(operation, func(t *testing.T) {
			a, _ := dmContextTestApp(t)
			owner, err := a.DMHistoryContextForTab("a")
			if err != nil {
				t.Fatal(err)
			}
			original, err := a.dmHistoryForContext(owner)
			if err != nil {
				t.Fatal(err)
			}
			if err := a.DMHistoryAppendForContext(owner, "peer", "Peer", DMEntry{Body: "original"}); err != "" {
				t.Fatal(err)
			}
			otherID := mustTempIdentity(t)
			other, err := dmHistoryStoreForIdentity(original.dir, otherID)
			if err != nil {
				t.Fatal(err)
			}
			if err := other.save(dmLog{Peer: "peer", Messages: []DMEntry{{Body: "other identity"}}}); err != nil {
				t.Fatal(err)
			}
			captured, resume, done := make(chan struct{}), make(chan struct{}), make(chan string, 1)
			oldHook := dmBeforeWrite
			dmBeforeWrite = func() { close(captured); <-resume }
			t.Cleanup(func() { dmBeforeWrite = oldHook })
			a.dmMu.Lock()
			locked := true
			defer func() {
				if locked {
					a.dmMu.Unlock()
				}
			}()
			go func() {
				if operation == "append" {
					done <- a.DMHistoryAppendForContext(owner, "peer", "Peer", DMEntry{Body: "accepted"})
				} else {
					done <- a.DMHistoryClearForContext(owner, "peer")
				}
			}()
			select {
			case <-captured:
			case <-timeoutC(t):
				close(resume)
				t.Fatal("writer did not capture ownership before waiting for dmMu")
			}
			a.identityMu.Lock()
			replaceDMIdentityForTest(a, otherID)
			a.identityMu.Unlock()
			a.tabsMu.Lock()
			a.activateLocked("")
			a.tabsMu.Unlock()
			close(resume)
			a.dmMu.Unlock()
			locked = false
			select {
			case err := <-done:
				if err != "" {
					t.Fatal(err)
				}
			case <-timeoutC(t):
				t.Fatal("accepted writer did not finish")
			}
			firstLog, err := original.load("peer")
			if err != nil {
				t.Fatal(err)
			}
			if operation == "clear" {
				if len(firstLog.Messages) != 0 {
					t.Fatalf("original history not cleared: %+v", firstLog)
				}
			} else if len(firstLog.Messages) != 2 || firstLog.Messages[1].Body != "accepted" {
				t.Fatalf("append lost captured owner: %+v", firstLog)
			}
			otherLog, err := other.load("peer")
			if err != nil || len(otherLog.Messages) != 1 || otherLog.Messages[0].Body != "other identity" {
				t.Fatalf("captured write changed other identity: %+v / %v", otherLog, err)
			}
		})
	}
}
