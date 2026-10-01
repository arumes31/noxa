package main

import (
	"encoding/json"
	"net"
	"testing"

	"noxa/internal/netproto"
)

func TestSessionInfoIncludesNegotiatedAuthorizationModel(t *testing.T) {
	conn, peer := net.Pipe()
	t.Cleanup(func() { _ = conn.Close(); _ = peer.Close() })
	m := &connManager{conn: conn, authorizationModel: netproto.AuthorizationModelRolesV1}
	app := &App{activeID: "a", tabs: map[string]*tabState{"a": {cm: m}}}
	app.cmStore(m)
	session, err := app.SessionInfoForTab("a")
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(session)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	if fields["authorization_model"] != netproto.AuthorizationModelRolesV1 {
		t.Fatalf("negotiated authorization model missing from session: %s", encoded)
	}
}

func TestSessionInfoForTab(t *testing.T) {
	conn, peer := net.Pipe()
	t.Cleanup(func() { _ = conn.Close(); _ = peer.Close() })
	first := &connManager{conn: conn, clientID: "alice",
		tlsUsed: true, fingerprint: "first-fingerprint", newServer: true,
		authorizationModel: netproto.AuthorizationModelRolesV1}
	second := &connManager{conn: conn, clientID: "guest", isGuest: true}
	app := &App{activeID: "a", tabs: map[string]*tabState{"a": {cm: first}, "b": {cm: second}, "missing-manager": {}}}
	app.cmStore(first)
	got, err := app.SessionInfoForTab("a")
	want := SessionInfo{ClientID: "alice", Connected: true,
		Security:           "TLS (new server fingerprint pinned: first-fingerprint)",
		AuthorizationModel: netproto.AuthorizationModelRolesV1}
	if err != nil || got != want {
		t.Fatalf("session = %+v, %v; want %+v", got, err, want)
	}
	app.tabsMu.Lock()
	_, _, _, ok := app.activateLocked("b")
	app.tabsMu.Unlock()
	if !ok {
		t.Fatal("native activation failed")
	}
	for _, tabID := range []string{"", "a", "unknown", "missing-manager"} {
		if _, err := app.SessionInfoForTab(tabID); err == nil {
			t.Fatalf("accepted session lookup for %q", tabID)
		}
	}
	got, err = app.SessionInfoForTab("b")
	want = SessionInfo{ClientID: "guest", IsGuest: true, Connected: true,
		Security: "PLAINTEXT — traffic is NOT encrypted"}
	if err != nil || got != want {
		t.Fatalf("guest session = %+v, %v; want %+v", got, err, want)
	}
	// A retained offline tab must not expose stale account identity.
	second.mu.Lock()
	second.conn = nil
	second.isGuest = false
	second.mu.Unlock()
	got, err = app.SessionInfoForTab("b")
	want = SessionInfo{IsGuest: true, Security: "offline"}
	if err != nil || got != want {
		t.Fatalf("offline session = %+v, %v; want %+v", got, err, want)
	}
	app.cmStore(first)
	if _, err := app.SessionInfoForTab("b"); err == nil {
		t.Fatal("accepted mismatched active connection manager")
	}
}

func TestSessionInfoSnapshotIsCoherent(t *testing.T) {
	conn, peer := net.Pipe()
	t.Cleanup(func() { _ = conn.Close(); _ = peer.Close() })
	m := &connManager{conn: conn, clientID: "account", tlsUsed: true, fingerprint: "account-key",
		authorizationModel: netproto.AuthorizationModelRolesV1}
	app := &App{activeID: "a", tabs: map[string]*tabState{"a": {cm: m}}}
	app.cmStore(m)
	account := SessionInfo{ClientID: "account", Connected: true, Security: "TLS (fingerprint account-key)",
		AuthorizationModel: netproto.AuthorizationModelRolesV1}
	guest := SessionInfo{ClientID: "guest", IsGuest: true, Connected: true, Security: "PLAINTEXT — traffic is NOT encrypted"}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 1000 {
			m.mu.Lock()
			m.clientID, m.isGuest, m.tlsUsed = "guest", true, false
			m.authorizationModel = ""
			m.mu.Unlock()
			m.mu.Lock()
			m.clientID, m.isGuest, m.tlsUsed = "account", false, true
			m.authorizationModel = netproto.AuthorizationModelRolesV1
			m.mu.Unlock()
		}
	}()
	defer func() { <-done }()
	for range 1000 {
		got, err := app.SessionInfoForTab("a")
		if err != nil || (got != account && got != guest) {
			t.Fatalf("mixed session snapshot: %+v, %v", got, err)
		}
	}
}
