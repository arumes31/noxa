package main

import (
	"errors"
	"net"
	"path/filepath"
	"strings"
	"testing"
)

// 1c-iii-b item 5: every authenticated session uses noxa-olm-v1. An old
// server or a guest session gets an explicit refusal (surfaced as the
// blocked reason), never a legacy send; legacy records stay read-only.
func TestRatchetCutoverGates(t *testing.T) {
	c, peer := net.Pipe()
	defer func() { c.Close(); peer.Close() }()
	for name, tc := range map[string]struct {
		guest, supported bool
		want             error
	}{
		"old server": {supported: false, want: errRatchetServerOutdated},
		"guest":      {guest: true, supported: true, want: errRatchetGuest},
	} {
		m := &connManager{}
		m.conn, m.ratchetDefault, m.isGuest, m.supportsOlm = c, true, tc.guest, tc.supported
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
	// A manager that never authenticated (and has no override) has no path.
	if (&connManager{}).ratchetConfigured() {
		t.Fatal("unauthenticated manager configured")
	}
	// Failure texts are explicit for devices without secure-messaging keys.
	if got := ratchetFailure(rfail("exhausted")).Error(); !strings.Contains(got, "does not support secure messaging yet") || !strings.Contains(got, "exhausted") {
		t.Fatalf("exhausted text: %q", got)
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
