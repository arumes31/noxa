package main

import (
	"sync"
	"testing"
	"time"

	"noxa/internal/netproto"
)

func TestRoleChatSendWaitsForAcceptance(t *testing.T) {
	for _, scope := range []string{"global", "channel", "direct"} {
		t.Run(scope, func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			t.Cleanup(unblock)
			app, cm := newPipedApp(t, func(f *netproto.Frame) (netproto.MessageType, any, bool) {
				var msg netproto.ChatSend
				if err := netproto.Decode(f, &msg); err != nil {
					t.Error(err)
				}
				if !msg.AckRequested || msg.ClientMsgID == "" {
					t.Errorf("invalid acknowledged send: %+v", msg)
				}
				close(entered)
				<-release
				return netproto.MsgError, netproto.Error{Code: 4, Message: "send denied", OriginType: uint16(netproto.MsgChatSend)}, true
			})
			cm.mu.Lock()
			cm.authorizationModel = netproto.AuthorizationModelRolesV1
			cm.mu.Unlock()
			cm.scopeKeys.put(0, 1, randKey(t))
			cm.scopeKeys.put(7, 1, randKey(t))
			target := "7"
			if scope == "direct" {
				peer := mustTempIdentity(t)
				public, _, err := peer.x25519()
				if err != nil {
					t.Fatal(err)
				}
				cm.pubKeys.put("peer", public)
				target = "peer"
			}
			app.tabs = map[string]*tabState{"a": {cm: cm}}
			app.activeID = "a"
			done := make(chan string, 1)
			go func() { done <- app.SendChatForTab("a", scope, target, "hello") }()
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("no send")
			}
			select {
			case result := <-done:
				t.Fatalf("completed before acceptance: %q", result)
			case <-time.After(25 * time.Millisecond):
			}
			unblock()
			select {
			case result := <-done:
				if result != "send denied" {
					t.Fatalf("send result: %q", result)
				}
			case <-time.After(time.Second):
				t.Fatal("no send result")
			}
		})
	}
}

func TestRoleChatSendAcceptance(t *testing.T) {
	for _, scope := range []string{"global", "channel", "direct", "offline"} {
		for _, response := range []string{"accepted", "reference", "destination", "outcome", "malformed"} {
			t.Run(scope+"/"+response, func(t *testing.T) {
				entered, release := make(chan struct{}), make(chan struct{})
				var once sync.Once
				unblock := func() { once.Do(func() { close(release) }) }
				t.Cleanup(unblock)
				app, cm := newPipedApp(t, func(f *netproto.Frame) (netproto.MessageType, any, bool) {
					var msg netproto.ChatSend
					if err := netproto.Decode(f, &msg); err != nil {
						t.Error(err)
					}
					if !msg.AckRequested {
						t.Error("did not request acceptance")
					}
					close(entered)
					<-release
					a := netproto.ChatAccepted{ClientMsgID: msg.ClientMsgID, ChannelID: msg.ChannelID, ToUniqueID: msg.ToUniqueID, Disposition: netproto.ChatStored, MessageID: 11}
					if scope == "direct" {
						a.Disposition, a.MessageID = netproto.ChatRelayed, 0
					}
					if scope == "offline" {
						a.Disposition, a.MessageID = netproto.ChatQueued, 0
					}
					switch response {
					case "reference":
						a.ClientMsgID = "other"
					case "destination":
						a.ToUniqueID = "other"
					case "outcome":
						a.Disposition = "unknown"
					case "malformed":
						return netproto.MsgChatAccepted, []string{"invalid"}, true
					}
					return netproto.MsgChatAccepted, a, true
				})
				cm.mu.Lock()
				cm.authorizationModel = netproto.AuthorizationModelRolesV1
				cm.mu.Unlock()
				cm.scopeKeys.put(0, 1, randKey(t))
				cm.scopeKeys.put(7, 1, randKey(t))
				sendScope, target := scope, "7"
				if scope == "direct" || scope == "offline" {
					peer := mustTempIdentity(t)
					public, _, err := peer.x25519()
					if err != nil {
						t.Fatal(err)
					}
					cm.pubKeys.put("peer", public)
					sendScope, target = "direct", "peer"
				}
				app.tabs = map[string]*tabState{"a": {cm: cm}}
				app.activeID = "a"
				done := make(chan string, 1)
				go func() { done <- app.SendChatForTab("a", sendScope, target, "hello") }()
				select {
				case <-entered:
				case <-time.After(time.Second):
					t.Fatal("no send")
				}
				// The original manager must receive the reply even after activation changes.
				app.tabsMu.Lock()
				app.activeID = "b"
				app.cmStore(&connManager{})
				app.tabsMu.Unlock()
				unblock()
				select {
				case result := <-done:
					if (result == "") != (response == "accepted") {
						t.Fatalf("unexpected result: %q", result)
					}
				case <-time.After(time.Second):
					t.Fatal("no result")
				}
			})
		}
	}
}
