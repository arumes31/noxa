package main

import (
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestRatchetBodyDisappearingReadsOnlyValidFields(t *testing.T) {
	for _, c := range []struct {
		body       string
		ttl        int64
		timer      int64 // -1 = no setting change
		expiredOut bool
	}{
		{`{"text":"hi","sentAt":1,"ttl":3600}`, 3600, -1, false},
		{`{"text":"hi","ttl":-5}`, 0, -1, false},
		{`{"text":"hi","ttl":99999999}`, 0, -1, false}, // beyond four weeks
		{`{"text":"on","disappearing":300}`, 0, 300, false},
		{`{"text":"off","disappearing":0}`, 0, 0, false},
		{`{"text":"bad","disappearing":-1}`, 0, -1, false},
		{`{"expired":true}`, 0, -1, true},
		{`not json`, 0, -1, false},
	} {
		ttl, timer, expired := ratchetBodyDisappearing(json.RawMessage(c.body))
		got := int64(-1)
		if timer != nil {
			got = *timer
		}
		if ttl != c.ttl || got != c.timer || expired != c.expiredOut {
			t.Errorf("%s: ttl=%d timer=%d expired=%v", c.body, ttl, got, expired)
		}
	}
	// The body a sender writes round-trips, and a plain message carries none.
	five := int64(300)
	if ttl, timer, _ := ratchetBodyDisappearing(ratchetChatBodyWith("x", 0, &five)); ttl != 0 || timer == nil || *timer != 300 {
		t.Fatalf("setting body: ttl=%d timer=%v", ttl, timer)
	}
	if ttl, timer, _ := ratchetBodyDisappearing(ratchetChatBodyWith("x", 0, nil)); ttl != 0 || timer != nil {
		t.Fatal("plain body carries disappearing fields")
	}
}

func TestApplyRatchetDisappearingRecordsTimersAndSettings(t *testing.T) {
	l := dmLog{Peer: "bob"}
	own := DMEntry{}
	if !applyRatchetDisappearing(&l, &own, RatchetRecord{Self: true, SentAt: 1000, Body: json.RawMessage(`{"text":"a","ttl":60}`)}) || own.TTL != 60 || own.ExpiresAt != 1060 {
		t.Fatalf("own copy: %+v", own)
	}
	in := DMEntry{}
	if !applyRatchetDisappearing(&l, &in, RatchetRecord{SentAt: 1000, Body: json.RawMessage(`{"text":"b","ttl":60}`)}) || in.TTL != 60 || in.ExpiresAt != 0 {
		t.Fatalf("received copy starts on display only: %+v", in)
	}
	// Newest setting change by send time wins, whichever arrives last.
	newer, older := DMEntry{}, DMEntry{}
	applyRatchetDisappearing(&l, &newer, RatchetRecord{SentAt: 2000, Body: json.RawMessage(`{"text":"on","disappearing":3600}`)})
	applyRatchetDisappearing(&l, &older, RatchetRecord{SentAt: 1500, Body: json.RawMessage(`{"text":"off","disappearing":0}`)})
	if l.Disappearing != 3600 || newer.Timer == nil || *newer.Timer != 3600 || older.Timer == nil || *older.Timer != 0 || newer.TTL != 0 {
		t.Fatalf("settings: log=%d newer=%+v older=%+v", l.Disappearing, newer, older)
	}
	// A redacted record is never shown again.
	if applyRatchetDisappearing(&l, &DMEntry{}, RatchetRecord{Body: ratchetExpiredBody}) {
		t.Fatal("redacted record projected")
	}
}

func disappearingTestApp(t *testing.T) (*App, dmHistoryStore) {
	t.Helper()
	id, err := newIdentity("test")
	if err != nil {
		t.Fatal(err)
	}
	a := &App{settingsPath: filepath.Join(t.TempDir(), "settings.json")}
	dir, err := a.dmHistoryDir()
	if err != nil {
		t.Fatal(err)
	}
	store, err := dmHistoryStoreForIdentity(dir, id)
	if err != nil {
		t.Fatal(err)
	}
	return a, store
}

func TestDisappearingMessagesStartOnDisplayAndExpireLocally(t *testing.T) {
	a, store := disappearingTestApp(t)
	now := time.Unix(10_000, 0)
	for _, e := range []DMEntry{
		{FromUniqueID: "me", Self: true, Body: "mine", ClientMsgID: "m1", SentAt: 9_990, TTL: 30, ExpiresAt: 10_020, Protocol: ratchetProtocol},
		{FromUniqueID: "bob", Body: "theirs", ClientMsgID: "b1", SentAt: 9_995, TTL: 60, Protocol: ratchetProtocol},
		{FromUniqueID: "bob", Body: "keeps", ClientMsgID: "b2", SentAt: 9_996, Protocol: ratchetProtocol},
	} {
		if msg := a.dmHistoryAppendWith(store, "bob", "Bob", e); msg != "" {
			t.Fatal(msg)
		}
	}
	// Nothing received expires before it was displayed.
	expired, _, next := a.expireDueWith(store, now.Add(time.Hour))
	if len(expired["bob"]) != 1 || expired["bob"][0].ClientMsgID != "m1" || next != 0 {
		t.Fatalf("before display: %+v next=%d", expired, next)
	}
	started, err := a.startDisappearingWith(store, "bob", now)
	if err != nil || len(started) != 1 || started[0].ClientMsgID != "b1" || started[0].ExpiresAt != 10_060 {
		t.Fatalf("started %+v %v", started, err)
	}
	// Starting again does not restart a running timer.
	if again, _ := a.startDisappearingWith(store, "bob", now.Add(30*time.Second)); len(again) != 0 {
		t.Fatalf("timer restarted: %+v", again)
	}
	expired, needRedact, next := a.expireDueWith(store, now.Add(59*time.Second))
	if len(expired) != 0 || next != 10_060 || !needRedact {
		t.Fatalf("not yet due: %+v next=%d redact=%v", expired, next, needRedact)
	}
	expired, _, next = a.expireDueWith(store, now.Add(60*time.Second))
	if len(expired["bob"]) != 1 || expired["bob"][0].ClientMsgID != "b1" || next != 0 {
		t.Fatalf("due: %+v next=%d", expired, next)
	}
	msgs, err := store.messages("bob")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range msgs {
		switch m.ClientMsgID {
		case "m1", "b1":
			if m.Body != "" || !m.Deleted || !m.Expired || m.Redacted {
				t.Fatalf("expired entry kept text: %+v", m)
			}
		case "b2":
			if m.Body != "keeps" || m.Deleted {
				t.Fatalf("normal message touched: %+v", m)
			}
		}
	}
	// Search and previews skip expired text like any deleted message.
	if res := store.search("bob", "theirs", 0); len(res.Messages) != 0 {
		t.Fatalf("expired text still searchable: %+v", res.Messages)
	}
	// Archive redaction bookkeeping: pending until marked.
	pending := a.pendingRedactionsWith(store)
	if len(pending) != 2 || !pending[ratchetSemanticKey("bob", "b1")] || !pending[ratchetSemanticKey("me", "m1")] {
		t.Fatalf("pending redactions: %v", pending)
	}
	a.markRedactedWith(store, map[string]bool{ratchetSemanticKey("bob", "b1"): true})
	if pending = a.pendingRedactionsWith(store); len(pending) != 1 || !pending[ratchetSemanticKey("me", "m1")] {
		t.Fatalf("after marking: %v", pending)
	}
}

func TestSweepDisappearingEmitsExpiredMessagesAndSchedulesNext(t *testing.T) {
	a, store := disappearingTestApp(t)
	var mu sync.Mutex
	var events []map[string]any
	a.eventEmit = func(name string, payload any) {
		if name == dmDisappearEvent {
			mu.Lock()
			events = append(events, payload.(map[string]any))
			mu.Unlock()
		}
	}
	now := time.Now()
	for _, e := range []DMEntry{
		{FromUniqueID: "me", Self: true, Body: "soon", ClientMsgID: "s", TTL: 30, ExpiresAt: now.Unix() - 1, Protocol: ratchetProtocol},
		{FromUniqueID: "me", Self: true, Body: "later", ClientMsgID: "l", TTL: 3600, ExpiresAt: now.Unix() + 3600, Protocol: ratchetProtocol},
	} {
		if msg := a.dmHistoryAppendWith(store, "bob", "Bob", e); msg != "" {
			t.Fatal(msg)
		}
	}
	a.sweepDisappearingWith(store, now, 0, true, false)
	mu.Lock()
	got := append([]map[string]any(nil), events...)
	mu.Unlock()
	if len(got) != 1 || got[0]["peer"] != "bob" {
		t.Fatalf("events: %+v", got)
	}
	list := got[0]["messages"].([]dmExpiredMessage)
	if len(list) != 1 || list[0].ClientMsgID != "s" {
		t.Fatalf("expired list: %+v", list)
	}
	a.dmDisappear.mu.Lock()
	next, redact := a.dmDisappear.next, a.dmDisappear.redact
	a.dmDisappear.mu.Unlock()
	if next != now.Unix()+3600 || !redact {
		t.Fatalf("next=%d redact=%v", next, redact)
	}
	// Not due: a later sweep returns before opening any log (it would need
	// the identity) and emits nothing. With settings unset no identity is
	// ever loaded.
	a.settingsPath = ""
	a.sweepDisappearing(now.Add(time.Second))
	mu.Lock()
	defer mu.Unlock()
	if len(events) != 1 {
		t.Fatalf("idle sweep emitted: %+v", events)
	}
}

func TestRatchetRedactedArchiveKeepsMetadata(t *testing.T) {
	raw, err := ratchetRedactedArchive([]byte(`{"protocol":"noxa-olm-v1","sender":"bob","logical_id":"b1","body":{"text":"secret","ttl":60},"future":7}`))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got["sender"] != "bob" || got["logical_id"] != "b1" || got["future"] != float64(7) {
		t.Fatalf("metadata lost: %v", got)
	}
	if body, _ := got["body"].(map[string]any); body["expired"] != true || body["text"] != nil {
		t.Fatalf("body not redacted: %v", got["body"])
	}
}
