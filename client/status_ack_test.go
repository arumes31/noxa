package main

import (
	"sync"
	"testing"
	"time"

	"noxa/internal/netproto"
)

func TestRoleStatusWaitsForConfirmation(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	app, cm := newPipedApp(t, func(_ *netproto.Frame) (netproto.MessageType, any, bool) {
		close(entered)
		<-release
		return netproto.MsgError, netproto.Error{Message: "status denied", OriginType: uint16(netproto.MsgSetStatus)}, true
	})
	cm.mu.Lock()
	cm.authorizationModel = netproto.AuthorizationModelRolesV1
	cm.clientID = "self"
	cm.mu.Unlock()
	done := make(chan string, 1)
	go func() { done <- app.SetStatus("invisible", "private") }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("no status request")
	}
	select {
	case got := <-done:
		t.Fatalf("returned before confirmation: %q", got)
	case <-time.After(25 * time.Millisecond):
	}
	unblock()
	select {
	case got := <-done:
		if got != "status denied" {
			t.Fatalf("result: %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("no status result")
	}
}

func TestRoleStatusReplyValidationAndTabCapture(t *testing.T) {
	for _, outcome := range []string{"saved", "online", "wrong client", "wrong status", "wrong message", "missing status", "missing message", "null status", "null message", "malformed", "empty error"} {
		t.Run(outcome, func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			defer unblock()
			status, message := "away", "Stepped out"
			if outcome == "online" {
				status, message = "", ""
			}
			app, cm := newPipedApp(t, func(f *netproto.Frame) (netproto.MessageType, any, bool) {
				var msg netproto.SetStatus
				if err := netproto.Decode(f, &msg); err != nil || !msg.AckRequested || msg.Message != message {
					t.Errorf("request: %+v / %v", msg, err)
				}
				close(entered)
				<-release
				if outcome == "malformed" {
					return netproto.MsgStatusSaved, []int{1}, true
				}
				if outcome == "empty error" {
					return netproto.MsgError, netproto.Error{OriginType: uint16(netproto.MsgSetStatus)}, true
				}
				reply := map[string]any{"client_id": "self", "status": status, "message": message}
				switch outcome {
				case "wrong client":
					reply["client_id"] = "other"
				case "wrong status":
					reply["status"] = "busy"
				case "wrong message":
					reply["message"] = "different"
				case "missing status":
					delete(reply, "status")
				case "missing message":
					delete(reply, "message")
				case "null status":
					reply["status"] = nil
				case "null message":
					reply["message"] = nil
				}
				return netproto.MsgStatusSaved, reply, true
			})
			cm.mu.Lock()
			cm.authorizationModel = netproto.AuthorizationModelRolesV1
			cm.clientID = "self"
			cm.mu.Unlock()
			app.settings.AutoAwayMessage = "Stepped out"
			app.activeID = "a"
			app.tabs = map[string]*tabState{"a": {cm: cm}}
			done := make(chan string, 1)
			go func() {
				if outcome == "online" {
					done <- app.SetStatusForTab("a", " ONLINE ", "")
					return
				}
				done <- app.SetStatusForTab("a", "away", autoAwaySentinel)
			}()
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("no request")
			}
			app.tabsMu.Lock()
			app.activeID = "b"
			app.cmStore(&connManager{})
			app.tabsMu.Unlock()
			unblock()
			select {
			case got := <-done:
				if (got == "") != (outcome == "saved" || outcome == "online") {
					t.Fatalf("result: %q", got)
				}
			case <-time.After(time.Second):
				t.Fatal("no result")
			}
		})
	}
}
