package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
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
	cm.projectRatchet(rec("bob", "m1", "chat", "", "olm m1", false), true)
	cm.projectRatchet(rec("bob", "m1", "chat", "", "olm m1", false), false) // replay: idempotent
	// An edit that overtakes its original waits, then applies.
	cm.projectRatchet(rec("me", "e1", "edit", "n1", "mine (edited)", true), false)
	cm.projectRatchet(rec("me", "n1", "chat", "", "mine", true), false)
	// A forged change by bob of my record is ignored; a legacy change never
	// touches a new-protocol entry.
	cm.projectRatchet(rec("bob", "x", "delete", "n1", "", false), true)
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
	// Edit/delete routing picks the new-protocol own record.
	if p, err := app.ownDirectMessageProtocol(cm, "bob", "n1"); err != nil || p != ratchetProtocol {
		t.Fatal(p, err)
	}
	if p, err := app.ownDirectMessageProtocol(cm, "bob", "m2"); err != nil || p != "" {
		t.Fatal(p, err)
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
