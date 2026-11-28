package main

import (
	"encoding/json"
	"fmt"
	"net"
	"testing"
	"time"

	"noxa/internal/e2ee/durable"
	"noxa/internal/netproto"
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
	var erased []map[string]bool
	old := dmClearRedact
	dmClearRedact = func(_ *connManager, keys map[string]bool) { erased = append(erased, keys) }
	t.Cleanup(func() { dmClearRedact = old })
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
	// The replay asks for the text of exactly the deleted messages to be
	// erased from the protected archive; erased records are not asked again.
	want := map[string]bool{ratchetSemanticKey("bob", "a1"): true, ratchetSemanticKey("me", "timer:1"): true, ratchetSemanticKey("me", "a2"): true}
	if len(erased) != 1 || fmt.Sprint(erased[0]) != fmt.Sprint(want) {
		t.Fatalf("erase requests: %+v", erased)
	}
	for i := range archive {
		archive[i].Body = ratchetExpiredBody
	}
	cm.ratchetProject(cm, archive, false)
	if len(erased) != 1 {
		t.Fatalf("erased records asked again: %+v", erased)
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
	if len(erased) != 1 {
		t.Fatalf("kept messages asked to be erased: %+v", erased)
	}
}

// "Delete history" erases the text of the deleted messages from the protected
// archive of a connected session, and of nothing else: not another
// conversation, not a message of the same conversation delivered afterwards.
func TestDMHistoryClearErasesArchiveText(t *testing.T) {
	app, cm, _ := projectionTestApp(t)
	cons, err := newTestRatchetConsumer(t, func(netproto.OlmAdmissionRequest) (json.RawMessage, error) { return nil, nil }, 300)
	if err != nil {
		t.Fatal(err)
	}
	conn, other := net.Pipe()
	defer func() { _ = conn.Close(); _ = other.Close() }()
	cm.conn = conn
	cm.ratchetSess = &ratchetSession{conn: conn, epoch: cm.connEpoch, consumer: cons}
	app.tabsMu.Lock()
	app.tabs["tab"] = &tabState{cm: cm}
	app.tabsMu.Unlock()
	receive := func(peer, logical string, at float64, isNew bool) {
		t.Helper()
		st, tr, sr := cons.d.Store, durable.Ref{Kind: durable.Trust, ID: peer}, durable.Ref{Kind: durable.Session, ID: "olm:" + peer}
		snap, err := st.Read(tr)
		if err == nil && snap.Records[tr].Revision == 0 {
			err = st.SetTrust(snap, peer, []byte("verified"), false)
		}
		if err != nil {
			t.Fatal(err)
		}
		if snap, err = st.Read(tr, sr); err != nil {
			t.Fatal(err)
		}
		rec := directRecord(peer, peer, logical, at)
		if _, err = st.CommitReceive(snap, "d-"+logical, durable.ReceiveCandidate{SessionID: "olm:" + peer, TrustID: peer, Session: []byte("state"), Envelope: []byte(logical), Archive: ratchetJSON(rec),
			Context: durable.SessionContext{LocalAccountID: "local", LocalEpoch: 1, PeerEpoch: 1, PeerPrincipal: peer, PeerDevice: peer + "-device", PeerIdentity: peer + "-olm", Scope: durable.DevicePairScope, Protocol: ratchetProtocol}}); err != nil {
			t.Fatal(err)
		}
		cm.ratchetProject(cm, []RatchetRecord{rec}, isNew)
	}
	now := float64(time.Now().Unix())
	receive("bob", "b1", now-20, true)
	receive("bob", "b2", now-10, true)
	receive("carl", "c1", now-20, true)
	receive("bob", "fast", now+600, true) // the sender's clock runs ahead
	if msg := app.DMHistoryClear("bob"); msg != "" {
		t.Fatal(msg)
	}
	receive("bob", "late", now-5, true) // written before the clear, delivered after it
	text := func() map[string]string {
		t.Helper()
		records, err := cons.history()
		if err != nil {
			t.Fatal(err)
		}
		// A session start replays the archive. A projection can also run
		// inside a consumer operation, which must not wait for the erase.
		cons.serial.Lock()
		cm.ratchetProject(cm, records, false)
		cons.serial.Unlock()
		out := map[string]string{}
		for _, r := range records {
			out[r.LogicalID] = ratchetBodyText(r.Body)
		}
		return out
	}
	var got map[string]string
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) { // the erase is asynchronous
		if got = text(); got["b1"] == "" && got["b2"] == "" && got["fast"] == "" {
			break
		}
	}
	if got = text(); len(got) != 5 || got["b1"] != "" || got["b2"] != "" || got["fast"] != "" || got["c1"] != "c1" || got["late"] != "late" {
		t.Fatalf("archive text after the clear: %+v", got)
	}
	entries, err := app.DMHistoryLoad("bob")
	if err != nil || len(entries) != 1 || entries[0].ClientMsgID != "late" {
		t.Fatalf("log after the replays: %+v, %v", entries, err)
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
