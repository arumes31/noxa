package main

import (
	"fmt"
	"testing"
	"time"
)

func projectionTestApp(t *testing.T) (*App, *connManager, dmHistoryStore) {
	t.Helper()
	id, err := newIdentity("projection")
	if err != nil {
		t.Fatal(err)
	}
	old := identityRootOverride
	identityRootOverride = t.TempDir() // never touch a real identity
	t.Cleanup(func() { identityRootOverride = old })
	app := &App{tabs: map[string]*tabState{}, settingsPath: t.TempDir() + "/settings.json"}
	cm := app.newTabManager("tab", id)
	app.cmStore(cm)
	cm.sink = &productEvents{}
	cm.uniqueID = "me"
	return app, cm, mustStore(t, app, id)
}

func directRecord(peer, sender, logical string, sentAt float64) RatchetRecord {
	return RatchetRecord{Protocol: ratchetProtocol, Sender: sender, LogicalID: logical, Key: ratchetSemanticKey(sender, logical), Scope: "direct", Peer: peer,
		Purpose: "chat", Body: ratchetJSON(map[string]string{"text": logical}), SentAt: sentAt, Self: sender == "me"}
}

// The protected archive is replayed into the log on every session start, so
// "delete history" must outlive that replay while later messages still appear.
func TestDMHistoryClearSurvivesArchiveReplay(t *testing.T) {
	app, cm, store := projectionTestApp(t)
	now := float64(time.Now().Unix())
	timer := int64(3600)
	setting := directRecord("bob", "me", "timer:1", now-30)
	setting.Body = ratchetChatBodyWith("timer on", 0, &timer)
	archive := []RatchetRecord{directRecord("bob", "bob", "a1", now-20), setting, directRecord("bob", "me", "a2", now-10)}
	cm.ratchetProject(cm, archive, false)
	if entries, err := store.messages("bob"); err != nil || len(entries) != 3 {
		t.Fatalf("projected: %d %v", len(entries), err)
	}
	// A conversation that was never cleared keeps a record without a send time.
	cm.ratchetProject(cm, []RatchetRecord{directRecord("carl", "carl", "undated", 0)}, false)
	if entries, err := store.messages("carl"); err != nil || len(entries) != 1 {
		t.Fatalf("undated record: %d %v", len(entries), err)
	}
	if msg := app.DMHistoryClear("bob"); msg != "" {
		t.Fatal(msg)
	}
	if peers := store.peers(); len(peers) != 1 || peers[0].UniqueID != "carl" {
		t.Fatalf("a cleared conversation is still listed: %+v", peers)
	}
	cm.ratchetProject(cm, archive, false) // reconnect or restart
	if entries, err := store.messages("bob"); err != nil || len(entries) != 0 {
		t.Fatalf("deleted history came back: %+v %v", entries, err)
	}
	if ttl := app.dmDisappearingTTL(cm, "bob"); ttl != timer {
		t.Fatalf("clearing changed the conversation's timer: %d", ttl)
	}
	// A message delivered after the clear appears even when it was written
	// earlier (sent while this device was offline) or by a slow clock; so
	// does an own message sent now. Later replays keep both and nothing else.
	cm.ratchetProject(cm, []RatchetRecord{directRecord("bob", "bob", "late", now-5)}, true)
	archive = append(archive, directRecord("bob", "bob", "late", now-5), directRecord("bob", "me", "new", now+60))
	for range 2 {
		cm.ratchetProject(cm, archive, false)
	}
	entries, err := store.messages("bob")
	if err != nil || len(entries) != 2 || entries[0].ClientMsgID != "late" || entries[1].ClientMsgID != "new" {
		t.Fatalf("after the clear: %+v %v", entries, err)
	}
}

// A session start replays the whole archive: each conversation's log is read
// once and written at most once, and the result equals projecting one by one.
func TestRatchetReplayLoadsEachLogOnce(t *testing.T) {
	app, cm, store := projectionTestApp(t)
	var archive []RatchetRecord
	for i := range 40 {
		peer := []string{"bob", "carl"}[i%2]
		archive = append(archive, directRecord(peer, peer, fmt.Sprintf("m%02d", i), float64(100+i)))
	}
	edit := directRecord("bob", "bob", "e1", 200)
	edit.Purpose, edit.Reference, edit.Body = "edit", "m00", ratchetJSON(map[string]string{"text": "edited"})
	archive = append(archive, edit)
	loads, saves := 0, 0
	dmStoreIO = func(write bool) {
		if write {
			saves++
		} else {
			loads++
		}
	}
	t.Cleanup(func() { dmStoreIO = nil })
	cm.ratchetProject(cm, archive, false)
	if loads != 2 || saves != 2 {
		t.Fatalf("first replay: %d loads, %d saves for two conversations", loads, saves)
	}
	loads, saves = 0, 0
	cm.ratchetProject(cm, archive, false)
	if loads != 2 || saves != 0 {
		t.Fatalf("unchanged replay: %d loads, %d saves", loads, saves)
	}
	dmStoreIO = nil
	batched := map[string][]DMEntry{}
	for _, peer := range []string{"bob", "carl"} {
		if batched[peer], _ = store.messages(peer); len(batched[peer]) != 20 {
			t.Fatalf("%s: %d entries", peer, len(batched[peer]))
		}
	}
	if e := batched["bob"][0]; e.Body != "edited" || !e.Edited {
		t.Fatalf("edit lost in the batch: %+v", e)
	}
	// The same records projected one at a time give the same logs.
	_, single, singleStore := projectionTestApp(t)
	for _, r := range archive {
		single.ratchetProject(single, []RatchetRecord{r}, false)
	}
	for peer, want := range batched {
		got, err := singleStore.messages(peer)
		if err != nil || fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("%s differs:\n%+v\n%+v (%v)", peer, got, want, err)
		}
	}
	_ = app
}
