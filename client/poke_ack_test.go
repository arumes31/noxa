package main

import (
	"sync"
	"testing"
	"time"

	"noxa/internal/netproto"
)

func TestRolePokeWaitsForAcceptance(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	app, cm := newPipedApp(t, func(f *netproto.Frame) (netproto.MessageType, any, bool) {
		if netproto.MessageType(f.Type) != netproto.MsgPoke {
			t.Errorf("unexpected frame: %d", f.Type)
		}
		close(entered)
		<-release
		return netproto.MsgError, netproto.Error{Code: 4, Message: "poke denied", OriginType: uint16(netproto.MsgPoke)}, true
	})
	cm.mu.Lock()
	cm.authorizationModel = netproto.AuthorizationModelRolesV1
	cm.mu.Unlock()
	app.tabs = map[string]*tabState{"a": {cm: cm}}
	app.activeID = "a"
	done := make(chan string, 1)
	go func() { done <- app.PokeForTab("a", "target", "hello") }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("no poke request")
	}
	select {
	case result := <-done:
		t.Fatalf("completed before acceptance: %q", result)
	case <-time.After(25 * time.Millisecond):
	}
	unblock()
	select {
	case result := <-done:
		if result != "poke denied" {
			t.Fatalf("result: %q", result)
		}
	case <-time.After(time.Second):
		t.Fatal("no result")
	}
}

func TestRolePokeAcceptanceValidationAndTabCapture(t *testing.T) {
	for _, outcome := range []string{"accepted", "wrong target", "malformed"} {
		t.Run(outcome, func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			t.Cleanup(unblock)
			app, cm := newPipedApp(t, func(f *netproto.Frame) (netproto.MessageType, any, bool) {
				var msg netproto.Poke
				if err := netproto.Decode(f, &msg); err != nil || !msg.AckRequested || msg.ClientID != "target" || msg.Message != "hello" {
					t.Errorf("request: %+v / %v", msg, err)
				}
				close(entered)
				<-release
				if outcome == "malformed" {
					return netproto.MsgPokeAccepted, []string{"invalid"}, true
				}
				target := msg.ClientID
				if outcome == "wrong target" {
					target = "other"
				}
				return netproto.MsgPokeAccepted, netproto.PokeAccepted{ClientID: target}, true
			})
			cm.mu.Lock()
			cm.authorizationModel = netproto.AuthorizationModelRolesV1
			cm.mu.Unlock()
			app.tabs = map[string]*tabState{"a": {cm: cm}}
			app.activeID = "a"
			done := make(chan string, 1)
			go func() { done <- app.PokeForTab("a", "target", "hello") }()
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
			case result := <-done:
				if (result == "") != (outcome == "accepted") {
					t.Fatalf("result: %q", result)
				}
			case <-time.After(time.Second):
				t.Fatal("no result")
			}
		})
	}
}

func TestLegacyPokeDoesNotRequestAcknowledgement(t *testing.T) {
	requests := make(chan *netproto.Frame, 1)
	app, cm := newPipedApp(t, func(f *netproto.Frame) (netproto.MessageType, any, bool) { requests <- f; return 0, nil, false })
	app.tabs = map[string]*tabState{"a": {cm: cm}}
	app.activeID = "a"
	if result := app.PokeForTab("a", "target", "hello"); result != "" {
		t.Fatal(result)
	}
	var msg netproto.Poke
	if err := netproto.Decode(nextFrame(t, requests, netproto.MsgPoke), &msg); err != nil || msg.AckRequested {
		t.Fatalf("legacy request: %+v / %v", msg, err)
	}
}
