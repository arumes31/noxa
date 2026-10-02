package main

import (
	"testing"

	"noxa/internal/netproto"
)

func TestDisplayNameWaitsForMatchingAcknowledgement(t *testing.T) {
	for _, test := range []struct {
		name     string
		clientID string
		nickname string
		wantOK   bool
	}{
		{"saved", "self", "Daniel", true},
		{"wrong session", "other", "Daniel", false},
		{"wrong name", "self", "owner", false},
		{"missing name", "self", "", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			app, cm := newPipedApp(t, func(frame *netproto.Frame) (netproto.MessageType, any, bool) {
				var request netproto.DisplayNameSet
				if netproto.MessageType(frame.Type) != netproto.MsgDisplayNameSet || netproto.Decode(frame, &request) != nil || request.Nickname != "Daniel" {
					t.Errorf("unexpected display name request: %+v", request)
				}
				return netproto.MsgDisplayNameSaved, netproto.DisplayNameSaved{ClientID: test.clientID, Nickname: test.nickname}, true
			})
			cm.mu.Lock()
			cm.clientID, cm.nickname, cm.uniqueID = "self", "owner", "owner-uid"
			cm.mu.Unlock()
			app.tabs["one"] = &tabState{cm: cm, info: TabInfo{ID: "one", Nickname: "owner"}}
			app.activeID = "one"
			if err := app.SetDisplayNameForTab("one", " Daniel "); (err == "") != test.wantOK {
				t.Fatalf("rename = %q; success wanted=%v", err, test.wantOK)
			}
			cm.mu.Lock()
			defer cm.mu.Unlock()
			want := "owner"
			if test.wantOK {
				want = "Daniel"
			}
			if cm.nickname != want || cm.uniqueID != "owner-uid" || app.tabs["one"].info.Nickname != "owner" {
				t.Fatalf("name or account identity changed incorrectly: nickname=%q uniqueID=%q", cm.nickname, cm.uniqueID)
			}
		})
	}
}

func TestNamedLoginSeparatesAccountAndDisplayName(t *testing.T) {
	addr, requests, release := gatedReconnectServer(t)
	cm := newConnManager(t.Context())
	cm.sink = &eventRecorder{}
	cm.id = mustTempIdentity(t)
	t.Cleanup(cm.disconnect)
	result := make(chan string, 1)
	go func() { result <- cm.connectNamed(addr, "owner", " Daniel ", "pw", "server-pw") }()
	request := waitReconnect(t, requests)
	if request.Username != "owner" || request.Nickname != "Daniel" || request.Password != "pw" {
		t.Fatalf("alias replaced credentials: %+v", request)
	}
	close(release)
	if err := waitReconnect(t, result); err != "" {
		t.Fatal(err)
	}
	cm.mu.Lock()
	defer cm.mu.Unlock()
	if cm.nickname != "Daniel" || cm.displayName != "Daniel" {
		t.Fatalf("public name was not retained: %q, %q", cm.nickname, cm.displayName)
	}
}
