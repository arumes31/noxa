package main

import (
	"crypto/tls"
	"encoding/json"
	"net"
	"sync"
	"testing"
	"time"

	"noxa/internal/netproto"
	"noxa/internal/tlscert"
)

func TestBackgroundDisconnectPublishesReconnectOwner(t *testing.T) {
	a := newTabApp(t)
	background, _ := a.newTab()
	selected, selectedState := a.newTab()
	a.activate(selected)
	var lost []string
	plain := 0
	a.eventEmit = func(name string, payload any) {
		if name == "tab_disconnected" {
			lost = append(lost, payload.(string))
		}
		if name == "disconnected" {
			plain++
		}
	}
	a.relayTabEvent(background, "disconnected", nil)
	if len(lost) != 1 || lost[0] != background {
		t.Fatalf("background loss owners = %v, want only %s", lost, background)
	}
	if plain != 0 || a.cmLoad() != selectedState.cm {
		t.Fatal("background loss was routed to the selected server")
	}
}

// The authentication response is held until the test has changed tab ownership.
// This exercises the actual TLS/authentication boundary rather than a fake swap.
func gatedReconnectServer(t *testing.T) (string, <-chan netproto.Authenticate, chan<- struct{}) {
	t.Helper()
	cert, _, err := tlscert.Ensure(t.TempDir(), "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13})
	if err != nil {
		t.Fatal(err)
	}
	auth := make(chan netproto.Authenticate, 1)
	release, stop, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var mu sync.Mutex
	var accepted net.Conn
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		mu.Lock()
		accepted = conn
		mu.Unlock()
		defer func() { _ = conn.Close() }()
		frame, err := netproto.ReadFrame(conn)
		if err != nil {
			return
		}
		var request netproto.Authenticate
		if netproto.Decode(frame, &request) != nil {
			return
		}
		auth <- request
		select {
		case <-release:
		case <-stop:
			return
		}
		nickname := request.Nickname
		if nickname == "" {
			nickname = request.Username
		}
		response, _ := netproto.Encode(netproto.MsgAuthResponse, netproto.AuthResponse{OK: true, AuthorizationModel: netproto.AuthorizationModelRolesV1, ClientID: "recovered-client", UniqueID: "original-user", Nickname: nickname})
		if netproto.WriteFrame(conn, response) != nil {
			return
		}
		for {
			if _, err := netproto.ReadFrame(conn); err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() {
		close(stop)
		_ = listener.Close()
		mu.Lock()
		if accepted != nil {
			_ = accepted.Close()
		}
		mu.Unlock()
		<-done
	})
	return listener.Addr().String(), auth, release
}

func waitReconnect[T any](t *testing.T, results <-chan T) T {
	t.Helper()
	select {
	case result := <-results:
		return result
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for reconnect")
		var zero T
		return zero
	}
}

func prepareReconnect(t *testing.T, addr string) (*App, string, *tabState, *identity) {
	t.Helper()
	a := newTabApp(t)
	id := mustTempIdentity(t)
	tabID, source := a.newTabWithIdentity(id)
	source.info.Addr, source.info.Nickname = addr, "original-account"
	a.activate(tabID)
	source.cm.sink.Emit("disconnected", nil)
	t.Cleanup(func() {
		for _, tab := range a.ListTabs() {
			a.CloseTab(tab.ID)
		}
	})
	return a, tabID, source, id
}

func TestReconnectTabPreservesSelectionIdentityAndBufferedEvents(t *testing.T) {
	addr, requests, release := gatedReconnectServer(t)
	a, tabID, source, identity := prepareReconnect(t, addr)
	source.cm.displayName = "Daniel"
	otherID, other := a.newTabWithIdentity(mustTempIdentity(t))
	var mu sync.Mutex
	var emitted []journalEntry
	a.eventEmit = func(name string, payload any) {
		text, _ := payload.(string)
		mu.Lock()
		emitted = append(emitted, journalEntry{name, text})
		mu.Unlock()
	}
	result := make(chan ConnectTabResult, 1)
	go func() { result <- a.ReconnectTab(tabID, "original-password", "original-server-password") }()
	request := waitReconnect(t, requests)
	if request.Username != "original-account" || request.Nickname != "Daniel" || request.Password != "original-password" || request.ServerPassword != "original-server-password" || request.PublicKey != identity.PublicKey || request.X25519PublicKey != identity.X25519Public {
		t.Fatal("reconnect did not retain original account credentials and encryption identity")
	}
	a.activate(otherID)
	a.tabsMu.Lock()
	candidate := source.replacement
	a.tabsMu.Unlock()
	const chat = `{"type":"chat","data":{"from":"peer","text":"buffered message"}}`
	candidate.cm.sink.Emit("snapshot", `{"channels":[],"clients":[]}`)
	candidate.cm.sink.Emit("event", chat)
	mu.Lock()
	emitted = nil
	mu.Unlock()
	// ListTabs concurrently snapshots managers while the candidate is installed.
	stop, stopped := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(stopped)
		for {
			select {
			case <-stop:
				return
			default:
				a.ListTabs()
			}
		}
	}()
	close(release)
	got := waitReconnect(t, result)
	close(stop)
	<-stopped
	if got.Error != "" || got.TabID != tabID {
		t.Fatalf("reconnect = %+v", got)
	}
	if a.cmLoad() != other.cm {
		t.Fatal("background recovery stole the selected connection")
	}
	tabs := a.ListTabs()
	if len(tabs) != 2 || tabs[0].ID != tabID || !tabs[0].Connected || !tabs[1].Active {
		t.Fatalf("tab identity/order changed: %+v", tabs)
	}
	mu.Lock()
	for _, entry := range emitted {
		if entry.name == "tab_reset" || entry.name == "event" {
			t.Errorf("background candidate published %s into the foreground", entry.name)
		}
	}
	emitted = nil
	mu.Unlock()
	// The retired reader cannot replace its successor's snapshot or schedule retries.
	source.cm.sink.Emit("snapshot", `{"stale":true}`)
	source.cm.sink.Emit("event", `{"type":"chat","data":{"text":"stale"}}`)
	source.cm.sink.Emit("disconnected", nil)
	a.activate(tabID)
	mu.Lock()
	defer mu.Unlock()
	resets, chats := 0, 0
	for _, entry := range emitted {
		if entry.name == "tab_disconnected" || entry.payload == `{"stale":true}` {
			t.Errorf("retired manager event escaped: %+v", entry)
		}
		if entry.name == "tab_reset" {
			resets++
		}
		if entry.name == "event" && entry.payload == chat {
			chats++
		}
	}
	if resets != 1 || chats != 1 {
		t.Fatalf("buffer replay reset=%d chat=%d, want one of each", resets, chats)
	}
}

func TestReconnectTabCanceledWhileAuthenticating(t *testing.T) {
	for _, action := range []string{"close", "disconnect", "disable", "server removal"} {
		t.Run(action, func(t *testing.T) {
			addr, requests, release := gatedReconnectServer(t)
			a, tabID, source, _ := prepareReconnect(t, addr)
			result := make(chan ConnectTabResult, 1)
			go func() { result <- a.ReconnectTab(tabID, "password", "") }()
			waitReconnect(t, requests)
			a.tabsMu.Lock()
			candidate := source.replacement
			a.tabsMu.Unlock()
			if duplicate := a.ReconnectTab(tabID, "password", ""); duplicate.Error == "" {
				t.Fatal("allowed concurrent reconnect candidates")
			}
			switch action {
			case "close":
				a.CloseTab(tabID)
			case "disconnect":
				a.DisconnectTab(tabID)
			case "disable":
				a.settingsMu.Lock()
				a.settings.ReconnectOnLoss = false
				a.settingsMu.Unlock()
			case "server removal":
				source.cm.sink.Emit("event", `{"type":"server_shutdown","data":{}}`)
			}
			close(release)
			if got := waitReconnect(t, result); got.Error == "" {
				t.Fatal("canceled candidate was accepted")
			}
			if candidate.cm.connected() {
				t.Fatal("canceled candidate leaked a connection")
			}
			a.tabsMu.Lock()
			remaining := a.tabs[tabID]
			a.tabsMu.Unlock()
			if action == "close" || action == "disconnect" {
				if remaining != nil {
					t.Fatal("closed tab was resurrected")
				}
			} else if remaining != source || remaining.replacement != nil {
				t.Fatal("failed recovery did not leave the original tab available")
			}
		})
	}
}

func TestReconnectTabSelectedReplaysOnce(t *testing.T) {
	addr, requests, release := gatedReconnectServer(t)
	a, tabID, source, _ := prepareReconnect(t, addr)
	var mu sync.Mutex
	resets, events := 0, 0
	var replayedTransfers []ftProgress
	source.recordTransfer(ftProgress{ID: "resume", Direction: "download", Status: "canceled", Transferred: 12, Total: 100})
	a.eventEmit = func(name string, payload any) {
		mu.Lock()
		defer mu.Unlock()
		if name == "tab_reset" {
			resets++
		}
		if name == "event" {
			events++
		}
		if name == "ft_snapshot" {
			var snapshot struct {
				Transfers []ftProgress `json:"transfers"`
			}
			if err := json.Unmarshal([]byte(payload.(string)), &snapshot); err != nil {
				t.Error(err)
			}
			replayedTransfers = snapshot.Transfers
		}
	}
	result := make(chan ConnectTabResult, 1)
	go func() { result <- a.ReconnectTab(tabID, "password", "") }()
	waitReconnect(t, requests)
	a.tabsMu.Lock()
	candidate := source.replacement
	a.tabsMu.Unlock()
	candidate.cm.sink.Emit("event", `{"type":"chat","data":{"text":"offline delivery"}}`)
	close(release)
	if got := waitReconnect(t, result); got.Error != "" {
		t.Fatal(got.Error)
	}
	mu.Lock()
	defer mu.Unlock()
	if resets != 1 || events != 1 || a.cmLoad() != candidate.cm {
		t.Fatalf("selected replay resets=%d events=%d", resets, events)
	}
	if len(replayedTransfers) != 1 || replayedTransfers[0].ID != "resume" || replayedTransfers[0].Transferred != 12 {
		t.Fatalf("same-tab reconnect discarded resumable transfer history: %+v", replayedTransfers)
	}
}

func TestTabReconnectSuppression(t *testing.T) {
	for _, tc := range []struct {
		name, event string
		allowed     bool
	}{
		{"shutdown", `{"type":"server_shutdown","data":{}}`, false},
		{"server kick", `{"type":"kicked","data":{"client_id":"self","from_server":true}}`, false},
		{"ban", `{"type":"kicked","data":{"client_id":"self","ban":true}}`, false},
		{"channel kick", `{"type":"kicked","data":{"client_id":"self"}}`, true},
		{"other client", `{"type":"kicked","data":{"client_id":"peer","from_server":true}}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := newTabApp(t)
			tabID, source := a.newTab()
			source.cm.clientID = "self"
			a.relayTabEvent(tabID, "event", tc.event)
			a.relayTabEvent(tabID, "disconnected", nil)
			if source.reconnectAllowed != tc.allowed {
				t.Fatalf("reconnect allowed=%v, want %v", source.reconnectAllowed, tc.allowed)
			}
		})
	}
}
