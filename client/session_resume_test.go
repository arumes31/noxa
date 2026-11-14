package main

import (
	"net"
	"slices"
	"testing"
	"time"

	"noxa/internal/netproto"
)

// ReconnectTab presents the lost session's resume token and keeps the rotated
// one the server returns, in memory only.
func TestReconnectTabPresentsAndRotatesResumeToken(t *testing.T) {
	addr, requests, release := gatedReconnectServer(t)
	a, tabID, source, _ := prepareReconnect(t, addr)
	source.cm.setResumeToken("lost-session-token")
	result := make(chan ConnectTabResult, 1)
	go func() { result <- a.ReconnectTab(tabID, "pw", "") }()
	request := waitReconnect(t, requests)
	if request.ResumeToken != "lost-session-token" || !slices.Contains(request.ClientFeatures, netproto.ClientFeatureResume) {
		t.Fatalf("reconnect request token %q features %v", request.ResumeToken, request.ClientFeatures)
	}
	close(release)
	if got := waitReconnect(t, result); got.Error != "" {
		t.Fatalf("reconnect = %+v", got)
	}
	a.tabsMu.Lock()
	cm := a.tabs[tabID].cm
	a.tabsMu.Unlock()
	if cm.resumeTokenSnapshot() != "rotated-token" {
		t.Fatal("the rotated token was not kept for the next reconnect")
	}
}

// The update handoff carries the token across the restart inside the
// OS-protected envelope, and the restored connection presents it.
func TestUpdateEnvelopeCarriesResumeToken(t *testing.T) {
	a := newTabApp(t)
	active, tab := a.newTabWithIdentity(mustTempIdentity(t))
	tab.info.Addr, tab.info.Nickname = "server.test:1234", "account"
	local, remote := net.Pipe()
	defer func() { _ = local.Close(); _ = remote.Close() }()
	tab.cm.conn = local
	tab.cm.setResumeToken("session-token")
	a.activate(active)
	saved := a.snapshotUpdateConnection()
	if saved == nil || saved.ResumeToken != "session-token" {
		t.Fatalf("snapshot = %+v, want the session's resume token", saved)
	}
	identity := func(b []byte) ([]byte, error) { return append([]byte(nil), b...), nil }
	encoded, err := encodeUpdateReconnect(*saved, identity)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeUpdateReconnect(encoded, time.Now(), identity)
	if err != nil || decoded.ResumeToken != "session-token" {
		t.Fatalf("decoded = %+v, %v", decoded, err)
	}

	addr, requests, release := gatedReconnectServer(t)
	b := newTabApp(t)
	restore := testUpdateEnvelope(t)
	restore.Addr, restore.ChannelID, restore.ResumeToken = addr, 0, "session-token"
	result := make(chan map[string]any, 1)
	go func() { result <- b.restoreUpdateConnection(&restore) }()
	if request := waitReconnect(t, requests); request.ResumeToken != "session-token" {
		t.Fatalf("restored login token = %q", request.ResumeToken)
	}
	close(release)
	if got := waitReconnect(t, result); got["error"] != nil {
		t.Fatalf("restore = %v", got)
	} else if id, ok := got["tab_id"].(string); ok {
		b.CloseTab(id)
	}
}
