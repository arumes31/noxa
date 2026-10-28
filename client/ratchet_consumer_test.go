package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"noxa/internal/e2ee/admission"
	"noxa/internal/e2ee/durable"
	"noxa/internal/e2ee/olmdir"
	"noxa/internal/e2ee/ratchet"
	"noxa/internal/netproto"
)

func TestRatchetReceiveClassification(t *testing.T) {
	relabel := func(err error) string {
		_, e := ratchetDeterministic("invalid_evidence", func() (int, error) { return 0, err })
		return ratchetCode(e)
	}
	if got := relabel(errors.New("core rejected")); got != "invalid_evidence" {
		t.Fatalf("uncoded core rejection: %s", got)
	}
	if got := relabel(rfail("something_else")); got != "invalid_evidence" {
		t.Fatalf("unknown code passed through: %s", got)
	}
	for _, code := range []string{"core_unavailable", "fenced", "rate-limit", "unavailable", "conflict", "aborted", "local_conflict", "lost_key", "unauthenticated", "digest_mismatch", "invalid"} {
		if got := relabel(rfail(code)); got != code {
			t.Fatalf("%s relabelled to %s", code, got)
		}
	}
	// Store sentinels keep their code (a local CAS loss is transient).
	if got := relabel(durable.ErrConflict); got != "conflict" {
		t.Fatal(got)
	}
	for _, c := range []struct {
		code      string
		elsewhere bool
		want      string
	}{{"core_unavailable", false, "retry"}, {"rate-limit", true, "retry"}, {"", false, "retry"}, {"lost_key", true, "skip"}, {"unauthenticated", true, "skip"},
		{"lost_key", false, "quarantine"}, {"digest_mismatch", true, "quarantine"}, {"invalid", true, "quarantine"}} {
		if got := ratchetReceiveDisposition(c.code, c.elsewhere); got != c.want {
			t.Fatalf("%s/%v = %s, want %s", c.code, c.elsewhere, got, c.want)
		}
	}
}

func TestRatchetPureRules(t *testing.T) {
	// Semantic keys: sender-qualified, same shape as the browser (no HTML escaping).
	if k := ratchetSemanticKey("a<b", "m&1"); k != `["noxa-olm-v1","a<b","m&1"]` {
		t.Fatal(k)
	}
	if ratchetSemanticKey("alice", "m1") == ratchetSemanticKey("bob", "m1") {
		t.Fatal("logical IDs collide across senders")
	}
	targets := func(n int) []admission.Target { return make([]admission.Target, n) }
	if ratchetCheckFanout("direct", targets(20)) != nil || ratchetCheckFanout("direct", targets(21)) == nil || ratchetCheckFanout("group", targets(160)) != nil ||
		ratchetCheckFanout("group", targets(161)) == nil || ratchetCheckFanout("group", nil) == nil {
		t.Fatal("fanout bounds")
	}
	logical := map[string]string{"1:9": "a", "1:10": "a", "1:2": "b", "2:1": "a"}
	got := ratchetLatestAttempts([]string{"1:9", "1:10", "1:2"}, func(id string) string { return logical[id] })
	if strings.Join(got, ",") != "1:2,1:10" {
		t.Fatalf("numeric attempt order: %v", got)
	}
	if got = ratchetLatestAttempts([]string{"2:1", "1:10"}, func(id string) string { return logical[id] }); strings.Join(got, ",") != "2:1" {
		t.Fatalf("epoch order: %v", got)
	}
	key := bytes.Repeat([]byte{7}, 32)
	body := base64.StdEncoding.EncodeToString(append([]byte{3, 0x0a, 32}, append(key, 1, 2, 3)...))
	if otk, err := ratchetInitialOneTimeKey(body); err != nil || otk != base64.RawStdEncoding.EncodeToString(key) {
		t.Fatal(otk, err)
	}
	for _, bad := range []string{"", "!!!", base64.StdEncoding.EncodeToString([]byte{2, 0x0a, 32}), strings.Repeat("A", 9000)} {
		if _, err := ratchetInitialOneTimeKey(bad); ratchetCode(err) != "invalid" {
			t.Fatalf("%q: %v", bad, err)
		}
	}
	if !ratchetValidDeliveryID("d-1") || ratchetValidDeliveryID("") || ratchetValidDeliveryID("bad\x00id") || ratchetValidDeliveryID(strings.Repeat("x", 600)) || ratchetValidDeliveryID("a​b") {
		t.Fatal("delivery ID shape")
	}
	if f := ratchetBodySentAt(json.RawMessage(`{"text":"x","sentAt":12.5}`), 1); f != 12.5 {
		t.Fatal(f)
	}
	if f := ratchetBodySentAt(json.RawMessage(`{}`), 3); f != 3 {
		t.Fatal(f)
	}
}

type ratchetFakeCore struct{}

func (ratchetFakeCore) Apply(context.Context, ratchet.Request) (ratchet.Response, error) {
	return ratchet.Response{}, errors.New("ratchet: unexpected")
}

func newTestRatchetConsumer(t *testing.T, admissionFn func(netproto.OlmAdmissionRequest) (json.RawMessage, error), ttl int64) (*ratchetConsumer, error) {
	t.Helper()
	ns := durable.Namespace{Server: "tls-sha256:" + strings.Repeat("e", 64), Principal: "alice", Device: "a1"}
	s, err := durable.Open(t.TempDir(), ns, durable.IdentityProtection(bytes.Repeat([]byte{9}, 32)), 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return newRatchetConsumer(ratchetDeps{Store: s, Core: ratchetFakeCore{}, Namespace: ns, AttemptTTL: ttl,
		Admission: func(_ context.Context, r netproto.OlmAdmissionRequest) (json.RawMessage, error) {
			return admissionFn(r)
		},
		Directory: func(context.Context, netproto.OlmDirectoryRequest) (netproto.OlmDirectoryResult, error) {
			return netproto.OlmDirectoryResult{}, errors.New("unused")
		},
		DeviceKey: func(context.Context, string, string) (ed25519.PublicKey, error) {
			return nil, rfail("untrusted_device")
		},
		SignBundle:    func(b olmdir.Bundle) (olmdir.Bundle, error) { return b, nil },
		AssertCurrent: func() error { return nil }})
}

func markReady(c *ratchetConsumer) {
	c.ready = &ratchetReadiness{identity: "id", epoch: 1, expiresAt: time.Now().Unix() + 86400}
	c.readyAt, c.identity = time.Now(), "id"
}

func TestRatchetConfigBounds(t *testing.T) {
	ok := func(netproto.OlmAdmissionRequest) (json.RawMessage, error) { return nil, nil }
	for _, ttl := range []int64{30, 3600} {
		if _, err := newTestRatchetConsumer(t, ok, ttl); ratchetCode(err) != "invalid_config" {
			t.Fatalf("ttl %d: %v", ttl, err)
		}
	}
	if c, err := newTestRatchetConsumer(t, ok, 300); err != nil || c.d.AttemptTTL != 300 {
		t.Fatal(err)
	}
}

func TestRatchetDrainPageShapes(t *testing.T) {
	for name, page := range map[string]string{"missing deliveries": `{"after":"","has_more":false}`, "null deliveries": `{"deliveries":null,"has_more":false}`,
		"object deliveries": `{"deliveries":{},"has_more":false}`, "null page": `null`} {
		c, err := newTestRatchetConsumer(t, func(r netproto.OlmAdmissionRequest) (json.RawMessage, error) { return json.RawMessage(page), nil }, 300)
		if err != nil {
			t.Fatal(err)
		}
		markReady(c)
		if _, err := c.drain(); ratchetCode(err) != "invalid" {
			t.Fatalf("%s: a malformed page must be rejected, got %v", name, err)
		}
	}
	pages := 0
	malformed := `{"deliveries":[{"manifest":{}},{"id":"bad\u0000id","manifest":null},null,{"id":"` + strings.Repeat("x", 600) + `","copy":{}}],"after":"","has_more":false}`
	c, err := newTestRatchetConsumer(t, func(r netproto.OlmAdmissionRequest) (json.RawMessage, error) {
		if r.Action != "deliveries" {
			return nil, errors.New("unexpected " + r.Action)
		}
		pages++
		return json.RawMessage(malformed), nil
	}, 300)
	if err != nil {
		t.Fatal(err)
	}
	markReady(c)
	first, err := c.drain()
	if err != nil || len(first) != 4 {
		t.Fatalf("malformed deliveries: %v %v", first, err)
	}
	for _, q := range first {
		if q.Code != "invalid" || !strings.HasPrefix(q.ID, "malformed:") {
			t.Fatalf("placeholder: %+v", q)
		}
	}
	second, err := c.drain()
	if err != nil || len(second) != 4 || pages != 2 {
		t.Fatalf("quarantined once, not re-processed: %v %v pages=%d", second, err, pages)
	}
	// A closed consumer is fenced before any store or network work.
	c.close()
	if _, err := c.drain(); ratchetCode(err) != "fenced" {
		t.Fatal(err)
	}
}

func TestRatchetDMProjectionMixedLegacy(t *testing.T) {
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
	// Legacy history: bob's legacy message "m1" and my legacy "m2".
	for _, e := range []DMEntry{{FromUniqueID: "bob", Body: "legacy m1", ClientMsgID: "m1", EncVerified: true, EncMode: "e2e"},
		{FromUniqueID: "me", Body: "legacy m2", ClientMsgID: "m2", Self: true, EncVerified: true, EncMode: "e2e"}} {
		if msg := app.DMHistoryAppend("bob", "bob", e); msg != "" {
			t.Fatal(msg)
		}
	}
	rec := func(sender, logical, purpose, reference, text string, self bool) RatchetRecord {
		return RatchetRecord{Protocol: ratchetProtocol, Sender: sender, LogicalID: logical, Key: ratchetSemanticKey(sender, logical), Scope: "direct", Peer: "bob",
			Purpose: purpose, Reference: reference, Body: ratchetJSON(map[string]string{"text": text}), SentAt: 100, Self: self}
	}
	// bob reuses the logical string "m1" under the new protocol: no collision.
	cm.projectRatchet(rec("bob", "m1", "chat", "", "olm m1", false), true, nil)
	cm.projectRatchet(rec("bob", "m1", "chat", "", "olm m1", false), false, nil) // replay: idempotent
	// An edit that overtakes its original waits, then applies.
	cm.projectRatchet(rec("me", "e1", "edit", "n1", "mine (edited)", true), false, nil)
	cm.projectRatchet(rec("me", "n1", "chat", "", "mine", true), false, nil)
	// A forged change by bob of my record is ignored; a legacy change never
	// touches a new-protocol entry.
	cm.projectRatchet(rec("bob", "x", "delete", "n1", "", false), true, nil)
	if _, err := app.dmHistoryApplyChangeWith(mustStore(t, app, id), "bob", dmChange{Action: netproto.DMActionDelete, Ref: "m1", From: "bob"}); err != nil {
		t.Fatal(err)
	}
	entries, err := mustStore(t, app, id).messages("bob")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range entries {
		got = append(got, strings.Join([]string{e.Protocol, e.FromUniqueID, e.ClientMsgID, e.Body, map[bool]string{true: "deleted"}[e.Deleted], map[bool]string{true: "edited"}[e.Edited]}, "|"))
	}
	want := []string{"|bob|m1||deleted|", "|me|m2|legacy m2||", "noxa-olm-v1|bob|m1|olm m1||", "noxa-olm-v1|me|n1|mine (edited)||edited"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("mixed projection:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	// The legacy NaCl history copy never exports new-protocol records.
	for _, m := range historyMessagesSince(mustStore(t, app, id), 0) {
		if strings.Contains(m.Body, "olm") || strings.Contains(m.Body, "mine") {
			t.Fatalf("legacy history copy exported a new-protocol record: %+v", m)
		}
	}
	// Edit/delete routing picks the new-protocol own record.
	if p, err := app.ownDirectMessageProtocol(cm, "bob", "n1"); err != nil || p != ratchetProtocol {
		t.Fatal(p, err)
	}
	if p, err := app.ownDirectMessageProtocol(cm, "bob", "m2"); err != nil || p != "" {
		t.Fatal(p, err)
	}
	// P3-1: a pending (or failed) own new-protocol message cannot be changed.
	pending := rec("me", "n2", "chat", "", "not yet", true)
	pending.Status = "pending"
	cm.projectRatchet(pending, false, nil)
	if _, err := app.ownDirectMessageProtocol(cm, "bob", "n2"); !errors.Is(err, errDMChangeNotDelivered) {
		t.Fatal("pending message changed", err)
	}
	// P3-4: a stale replay never regresses a terminal status to pending.
	accepted := pending
	accepted.Status = "accepted"
	cm.projectRatchet(accepted, false, nil)
	cm.projectRatchet(pending, false, nil)
	if p, err := app.ownDirectMessageProtocol(cm, "bob", "n2"); err != nil || p != ratchetProtocol {
		t.Fatal("accepted status regressed", p, err)
	}
	if _, err := app.ownDirectMessageProtocol(cm, "bob", "m1"); !errors.Is(err, errDMChangeNotOwn) {
		t.Fatal("another sender's record is not own", err)
	}
}

func mustStore(t *testing.T, app *App, id *identity) dmHistoryStore {
	t.Helper()
	dir, err := app.dmHistoryDir()
	if err != nil {
		t.Fatal(err)
	}
	s, err := dmHistoryStoreForIdentity(dir, id)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestIdentityDeletionDestroysOnlyItsRatchetNamespaces(t *testing.T) {
	old := identityRootOverride
	identityRootOverride = t.TempDir()
	t.Cleanup(func() { identityRootOverride = old })
	a := &App{tabs: map[string]*tabState{}, hotkeys: map[string]*hotkeyReg{}, settingsPath: t.TempDir() + "/settings.json"}
	if msg := a.CreateIdentity("first"); msg != "" {
		t.Fatal(msg)
	}
	if msg := a.CreateIdentity("work"); msg != "" {
		t.Fatal(msg)
	}
	list := a.ListIdentities()
	if len(list) != 2 {
		t.Fatalf("identities: %+v", list)
	}
	base, err := defaultRatchetBase()
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]*identity{}
	for _, e := range list {
		path, _ := identityPathFor(e.ID)
		id, err := loadIdentityAtStrict(path)
		if err != nil {
			t.Fatal(err)
		}
		ids[e.ID] = id
		ns := durable.Namespace{Server: "tls-sha256:" + strings.Repeat("f", 64), Principal: e.UniqueID, Device: "d1"}
		s, err := durable.Open(ratchetStoreDir(base, id), ns, durable.IdentityProtection(bytes.Repeat([]byte{3}, 32)), 0)
		if err != nil {
			t.Fatal(err)
		}
		s.Close()
	}
	victim, keep := list[1], list[0]
	// Fix2 P3-F: a store held by another process fails the deletion as a
	// whole: nothing is removed, the identity stays usable (fence lifted).
	held, err := durable.Open(ratchetStoreDir(base, ids[victim.ID]), durable.Namespace{Server: "tls-sha256:" + strings.Repeat("f", 64), Principal: victim.UniqueID, Device: "d1"}, durable.IdentityProtection(bytes.Repeat([]byte{3}, 32)), 0)
	if err != nil {
		t.Fatal(err)
	}
	if msg := a.DeleteIdentity(victim.ID, true); msg == "" {
		t.Fatal("deletion with a held store must fail")
	}
	held.Close()
	if _, err := os.Stat(ratchetStoreDir(base, ids[victim.ID])); err != nil {
		t.Fatal("a failed deletion must keep the stores", err)
	}
	if _, fenced := ratchetDeleted.Load(ids[victim.ID].PublicKey); fenced {
		t.Fatal("a failed deletion must not leave the identity fenced")
	}
	if len(a.ListIdentities()) != 2 {
		t.Fatal("the identity survives a failed deletion")
	}
	if msg := a.DeleteIdentity(victim.ID, true); msg != "" {
		t.Fatal(msg)
	}
	if _, err := os.Stat(ratchetStoreDir(base, ids[victim.ID])); !os.IsNotExist(err) {
		t.Fatal("deleted identity's ratchet namespaces must be destroyed", err)
	}
	if _, err := os.Stat(ratchetStoreDir(base, ids[keep.ID])); err != nil {
		t.Fatal("another identity's namespaces must be kept", err)
	}
	if _, deleted := ratchetDeleted.Load(ids[victim.ID].PublicKey); !deleted {
		t.Fatal("new sessions of the deleted identity must be fenced")
	}
	// Re-importing the same key lifts the fence.
	restoreRatchetIdentity(ids[victim.ID].PublicKey)
	if _, deleted := ratchetDeleted.Load(ids[victim.ID].PublicKey); deleted {
		t.Fatal("re-import must lift the fence")
	}
}

func TestRatchetSameContent(t *testing.T) {
	rec := RatchetRecord{Scope: "direct", Peer: "bob", Purpose: "chat", Body: ratchetJSON(map[string]any{"text": "hi", "sentAt": 1.5})}
	same := ratchetIntent{Scope: "direct", Peer: "bob", Purpose: "chat", Body: ratchetChatBody("hi")}
	if !ratchetSameContent(rec, same) {
		t.Fatal("a retry with a new sentAt is the same content")
	}
	for name, in := range map[string]ratchetIntent{
		"text":    {Scope: "direct", Peer: "bob", Purpose: "chat", Body: ratchetChatBody("other")},
		"peer":    {Scope: "direct", Peer: "carol", Purpose: "chat", Body: ratchetChatBody("hi")},
		"purpose": {Scope: "direct", Peer: "bob", Purpose: "edit", Reference: "x", Body: ratchetChatBody("hi")},
		"scope":   {Scope: "group", Conversation: "bob", Purpose: "chat", Body: ratchetChatBody("hi")},
	} {
		if ratchetSameContent(rec, in) {
			t.Fatalf("%s change treated as the same content", name)
		}
	}
}

func TestRatchetStartGatePerConnection(t *testing.T) {
	c1, c1b := net.Pipe()
	c2, c2b := net.Pipe()
	defer func() { c1.Close(); c1b.Close(); c2.Close(); c2b.Close() }()
	m := &connManager{ratchetTest: &ratchetTestConfig{Dir: t.TempDir()}}
	// conn1's start is still in flight when the client reconnects (conn2).
	m.conn, m.ratchetStartingConn = c2, c1
	m.startRatchetRecorded(c2)
	if m.ratchetStartAt.IsZero() {
		t.Fatal("the new connection's start must not be skipped")
	}
	// This manager has no identity, so the start fails: it is recorded and
	// surfaced (never a silent wait).
	if m.ratchetStartErr == nil || m.ratchetBlockedReason() == "" {
		t.Fatal("start failure must be recorded and surfaced")
	}
	// A second start for the same connection while one runs is skipped.
	m.ratchetStartingConn, m.ratchetStartAt = c2, time.Time{}
	m.startRatchetRecorded(c2)
	if !m.ratchetStartAt.IsZero() {
		t.Fatal("same-connection start must be single-flight")
	}
	// A disconnect clears the previous connection's failure.
	m.ratchetStartingConn = nil
	m.mu.Lock()
	m.detachLocked()
	m.mu.Unlock()
	if m.ratchetStartErr != nil {
		t.Fatal("stale start failure carried across reconnect")
	}
}

func TestRatchetTombstoneSweep(t *testing.T) {
	old := identityRootOverride
	identityRootOverride = t.TempDir()
	t.Cleanup(func() { identityRootOverride = old })
	a := &App{tabs: map[string]*tabState{}, hotkeys: map[string]*hotkeyReg{}, settingsPath: t.TempDir() + "/settings.json"}
	if msg := a.CreateIdentity("kept"); msg != "" {
		t.Fatal(msg)
	}
	path, _ := identityPathFor(a.ListIdentities()[0].ID)
	kept, err := loadIdentityAtStrict(path)
	if err != nil {
		t.Fatal(err)
	}
	base, _ := defaultRatchetBase()
	live := ratchetStoreDir(base, kept)
	// Crash before the commit point: the identity still exists -> restore.
	if err := os.MkdirAll(live+ratchetTombstone+"x", 0o700); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(live+ratchetTombstone+"x", "a.db"), []byte("sealed"), 0o600)
	// A deleted identity's leftover tombstone (erase failed) -> erase.
	orphan := filepath.Join(base, strings.Repeat("ab", 16)+ratchetTombstone+"y")
	if err := os.MkdirAll(orphan, 0o700); err != nil {
		t.Fatal(err)
	}
	a.sweepRatchetStores()
	if _, err := os.Stat(filepath.Join(live, "a.db")); err != nil {
		t.Fatal("interrupted deletion of a surviving identity must be restored", err)
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatal("tombstone of a deleted identity must be erased", err)
	}
}

// 1c-iii-a item 5: committed attempts beyond the 64-attempt window are never
// stranded; the oldest (which hold the retired-through watermark) come first.
func TestRatchetResolveOrderNeverStrandsOldAttempts(t *testing.T) {
	var ids []string
	for i := 200; i >= 1; i-- {
		ids = append(ids, "1:"+strconv.Itoa(i))
	}
	got := ratchetResolveOrder(ids, ratchetRetireWin)
	if len(got) != 2*ratchetRetireWin || got[0] != "1:1" || got[ratchetRetireWin-1] != "1:64" || got[ratchetRetireWin] != "1:137" || got[len(got)-1] != "1:200" {
		t.Fatalf("order = %v", got)
	}
	// Under two windows: everything, sorted, once.
	small := ratchetResolveOrder([]string{"1:9", "1:2", "1:70"}, ratchetRetireWin)
	if strings.Join(small, ",") != "1:2,1:9,1:70" {
		t.Fatalf("small = %v", small)
	}
	// Draining repeatedly (each resolved attempt is noted retired) reaches every one.
	left := map[string]bool{}
	for _, id := range ids {
		left[id] = true
	}
	for round := 0; round < 4 && len(left) > 0; round++ {
		var rest []string
		for id := range left {
			rest = append(rest, id)
		}
		for _, id := range ratchetResolveOrder(rest, ratchetRetireWin) {
			delete(left, id)
		}
	}
	if len(left) != 0 {
		t.Fatalf("stranded: %d", len(left))
	}
}
