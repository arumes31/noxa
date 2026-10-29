package main

import (
	"encoding/json"
	"errors"
	"net"
	"path/filepath"
	"strings"
	"testing"

	"noxa/internal/netproto"
)

// 1c-iii-b item 5: every authenticated session uses noxa-olm-v1. An old
// server or a guest session gets an explicit refusal (surfaced as the
// blocked reason), never a legacy send; legacy records stay read-only.
func TestRatchetCutoverGates(t *testing.T) {
	c, peer := net.Pipe()
	defer func() { _ = c.Close(); _ = peer.Close() }()
	for name, tc := range map[string]struct {
		keyless, supported bool
		want               error
	}{
		"old server":      {supported: false, want: errRatchetServerOutdated},
		"keyless session": {keyless: true, supported: true, want: errRatchetGuest},
	} {
		m := &connManager{}
		m.conn, m.ratchetDefault, m.keyless, m.supportsOlm = c, true, tc.keyless, tc.supported
		m.id = mustTempIdentity(t)
		m.uniqueID, m.deviceID = "alice", "a1"
		if !m.ratchetConfigured() {
			t.Fatalf("%s: an authenticated session must use the new protocol", name)
		}
		if err := m.startRatchet(c); !errors.Is(err, tc.want) {
			t.Fatalf("%s: start = %v, want %v", name, err, tc.want)
		}
		m.startRatchetRecorded(c)
		if reason := m.ratchetBlockedReason(); !strings.Contains(reason, tc.want.Error()) {
			t.Fatalf("%s: blocked reason %q", name, reason)
		}
	}
	// Ruling 2: a key-identity guest is never refused for being a guest.
	oldRoot := identityRootOverride
	identityRootOverride = t.TempDir()
	defer func() { identityRootOverride = oldRoot }()
	g := &connManager{}
	g.conn, g.ratchetDefault, g.isGuest, g.supportsOlm = c, true, true, true
	g.id = mustTempIdentity(t)
	g.uniqueID, g.deviceID = "guest-key-derived", "g1"
	if err := g.startRatchet(c); errors.Is(err, errRatchetGuest) || errors.Is(err, errRatchetServerOutdated) {
		t.Fatalf("key-identity guest refused: %v", err)
	}
	// A manager that never authenticated (and has no override) has no path.
	if (&connManager{}).ratchetConfigured() {
		t.Fatal("unauthenticated manager configured")
	}
	// Failure texts are explicit for devices without secure-messaging keys.
	if got := ratchetFailure(rfail("device_unpublished")).Error(); !strings.Contains(got, "has not set up secure messaging yet") {
		t.Fatalf("unpublished device text: %q", got)
	}
	// CodeRabbit: local store exhaustion never blames a contact's device.
	if got := ratchetFailure(rfail("exhausted")).Error(); !strings.Contains(got, "store is full") || strings.Contains(got, "device in this conversation") {
		t.Fatalf("local exhaustion text: %q", got)
	}
	if got := ratchetFailure(errRatchetServerOutdated).Error(); got != errRatchetServerOutdated.Error() {
		t.Fatalf("server outdated text: %q", got)
	}
}

// Legacy NaCl records stay readable but are never changed (no legacy send).
func TestLegacyDirectChangeIsReadOnly(t *testing.T) {
	cm := newConnManager(t.Context())
	cm.id = mustTempIdentity(t)
	cm.ratchetDefault, cm.uniqueID = true, "alice"
	a := appWithCM(cm)
	a.settingsPath = filepath.Join(t.TempDir(), "settings.json")
	a.tabs = map[string]*tabState{"t": {cm: cm}}
	a.activeID = "t"
	dir, err := a.dmHistoryDir()
	if err != nil {
		t.Fatal(err)
	}
	id, _ := cm.identity()
	store, err := dmHistoryStoreForIdentity(dir, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.save(dmLog{Peer: "bob", Messages: []DMEntry{{Self: true, Body: "old legacy", SentAt: 5, ClientMsgID: "L1", FromUniqueID: "alice"}}}); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func() error{
		func() error { return a.EditDirectMessageForTab("t", "bob", "L1", "changed") },
		func() error { return a.DeleteDirectMessageForTab("t", "bob", "L1") },
	} {
		if err := change(); !errors.Is(err, errLegacyReadOnly) {
			t.Fatalf("legacy change: %v", err)
		}
	}
}

// CodeRabbit b46 finding 1: a session whose consumer is blocked by
// recovery_required is not reported as an active Olm path.
func TestDiagnosticsNotActiveWhileRecoveryRequired(t *testing.T) {
	c, peer := net.Pipe()
	defer func() { _ = c.Close(); _ = peer.Close() }()
	cons, err := newTestRatchetConsumer(t, func(netproto.OlmAdmissionRequest) (json.RawMessage, error) { return nil, nil }, 300)
	if err != nil {
		t.Fatal(err)
	}
	cm := newConnManager(t.Context())
	cm.id = mustTempIdentity(t)
	cm.conn = c
	cm.ratchetSess = &ratchetSession{conn: c, epoch: cm.connEpoch, consumer: cons}
	d, err := appWithCM(cm).E2EEDiagnostics("")
	if err != nil || !d.Active {
		t.Fatalf("healthy session: %+v %v", d, err)
	}
	cons.setSticky(rfail("recovery_required"))
	d, err = appWithCM(cm).E2EEDiagnostics("")
	if err != nil || d.Active || d.Protocol != "" || !strings.Contains(d.Unavailable, "recovery") {
		t.Fatalf("blocked session reported as active: %+v %v", d, err)
	}
}

// Review b46 B P3-1 (adopted repro): send paths report the specific blocked
// reason (here: the server does not offer secure messaging), not a generic
// "not ready".
func TestRatchetBlockedSendReportsReason(t *testing.T) {
	c, peer := net.Pipe()
	defer func() { _ = c.Close(); _ = peer.Close() }()
	m := &connManager{}
	m.conn, m.ratchetDefault, m.supportsOlm = c, true, false
	m.id = mustTempIdentity(t)
	m.uniqueID, m.deviceID = "alice", "a1"
	m.startRatchetRecorded(c)
	if _, err := sendChatOperationErr(m, "direct", "bob", "hi", 0, "op-1"); err == nil || !strings.Contains(err.Error(), "does not offer secure messaging") {
		t.Fatalf("direct send: %v", err)
	}
	if _, err := m.sendRatchetGroup("g1", "ref-1", "hi"); err == nil || !strings.Contains(err.Error(), "does not offer secure messaging") {
		t.Fatalf("group send: %v", err)
	}
}

// Review b7 P3-4: a send that never started (no session attached, whatever
// the recorded reason) keeps its outbox entry queued, never "uncertain".
func TestOutboxNeverStartedStaysQueued(t *testing.T) {
	c, peer := net.Pipe()
	defer func() { _ = c.Close(); _ = peer.Close() }()
	cm := newConnManager(t.Context())
	cm.id = mustTempIdentity(t)
	cm.conn, cm.ratchetDefault, cm.supportsOlm = c, true, false
	cm.uniqueID, cm.deviceID, cm.addr, cm.clientID = "alice", "a1", "srv.test:1", "c1"
	cm.startRatchetRecorded(c) // records "server does not offer secure messaging"
	a := appWithCM(cm)
	a.settingsPath = filepath.Join(t.TempDir(), "settings.json")
	store, err := a.outboxStore(cm)
	if err != nil {
		t.Fatal(err)
	}
	if got := a.sendOutboxEntry(cm, store, ChatOutboxEntry{ID: "op-1", Scope: "direct", Target: "bob", Text: "hi"}); got != outboxStayQueued {
		t.Fatalf("never-started send: %q", got)
	}
	// Another connection's start error never explains this connection's send.
	other, otherPeer := net.Pipe()
	defer func() { _ = other.Close(); _ = otherPeer.Close() }()
	if _, err := cm.ratchetSendConsumer(other); err == nil || errors.Is(err, errRatchetServerOutdated) {
		t.Fatalf("foreign connection got this connection's start error: %v", err)
	}
}

// Found by the real-path product step: the session's frame rate limit is a
// retryable rate limit for the consumer (backed off), never "unavailable".
func TestRatchetFrameRateLimitIsRetryable(t *testing.T) {
	if got := ratchetFailureCode(&requestFailure{response: netproto.Error{Message: netproto.RequestRateLimitMessage}}); got != "rate-limit" {
		t.Fatal(got)
	}
	if got := ratchetFailureCode(&requestFailure{response: netproto.Error{Message: "invalid Olm admission request"}}); got != "unavailable" {
		t.Fatal(got)
	}
}
