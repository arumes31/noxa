package main

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"noxa/internal/netproto"
)

func TestRoleJoinWaitsForMembershipResult(t *testing.T) {
	for _, channelID := range []int64{0, 7} {
		t.Run(fmt.Sprint(channelID), func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			t.Cleanup(unblock)
			app, cm := newPipedApp(t, func(f *netproto.Frame) (netproto.MessageType, any, bool) {
				close(entered)
				<-release
				return netproto.MsgError, netproto.Error{Code: 4, Message: "membership denied", OriginType: f.Type}, true
			})
			cm.mu.Lock()
			cm.authorizationModel = netproto.AuthorizationModelRolesV1
			cm.clientID = "self"
			cm.mu.Unlock()
			app.tabs = map[string]*tabState{"a": {cm: cm}}
			app.activeID = "a"
			done := make(chan string, 1)
			go func() { done <- app.JoinChannelForTab("a", channelID) }()
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("no join request")
			}
			select {
			case result := <-done:
				t.Fatalf("returned before membership result: %q", result)
			case <-time.After(50 * time.Millisecond):
			}
			unblock()
			select {
			case result := <-done:
				if !strings.Contains(result, "membership denied") {
					t.Fatalf("lost rejection: %q", result)
				}
			case <-time.After(time.Second):
				t.Fatal("no result")
			}
		})
	}
}

func TestRoleJoinAcknowledgementValidation(t *testing.T) {
	for _, channelID := range []int64{0, 7} {
		for _, outcome := range []string{"done", "wrong client", "wrong channel", "missing channel", "malformed", "empty rejection"} {
			t.Run(fmt.Sprintf("%d/%s", channelID, outcome), func(t *testing.T) {
				entered, release := make(chan struct{}), make(chan struct{})
				var once sync.Once
				unblock := func() { once.Do(func() { close(release) }) }
				t.Cleanup(unblock)
				app, cm := newPipedApp(t, func(f *netproto.Frame) (netproto.MessageType, any, bool) {
					var msg netproto.JoinChannel
					if err := netproto.Decode(f, &msg); err != nil || !msg.AckRequested || msg.ChannelID != channelID {
						t.Errorf("wrong join request: %+v / %v", msg, err)
					}
					close(entered)
					<-release
					result := netproto.ChannelJoined{ClientID: "self", ChannelID: channelID}
					switch outcome {
					case "wrong client":
						result.ClientID = "other"
					case "wrong channel":
						result.ChannelID++
					case "missing channel":
						return netproto.MsgChannelJoined, map[string]string{"client_id": "self"}, true
					case "malformed":
						return netproto.MsgChannelJoined, []string{"invalid"}, true
					case "empty rejection":
						return netproto.MsgError, netproto.Error{Code: 4, OriginType: f.Type}, true
					}
					return netproto.MsgChannelJoined, result, true
				})
				cm.mu.Lock()
				cm.authorizationModel = netproto.AuthorizationModelRolesV1
				cm.clientID = "self"
				cm.mu.Unlock()
				app.tabs = map[string]*tabState{"a": {cm: cm}}
				app.activeID = "a"
				done := make(chan string, 1)
				go func() { done <- app.JoinChannelForTab("a", channelID) }()
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
					if (result == "") != (outcome == "done") {
						t.Fatalf("result: %q", result)
					}
				case <-time.After(time.Second):
					t.Fatal("no result")
				}
			})
		}
	}
}

func TestLegacyJoinDoesNotRequestAcknowledgement(t *testing.T) {
	for _, channelID := range []int64{0, 7} {
		t.Run(fmt.Sprint(channelID), func(t *testing.T) {
			requests := make(chan *netproto.Frame, 1)
			app, cm := newPipedApp(t, func(f *netproto.Frame) (netproto.MessageType, any, bool) {
				requests <- f
				return 0, nil, false
			})
			app.tabs = map[string]*tabState{"a": {cm: cm}}
			app.activeID = "a"
			if result := app.JoinChannelForTab("a", channelID); result != "" {
				t.Fatal(result)
			}
			var msg netproto.JoinChannel
			if err := netproto.Decode(nextFrame(t, requests, netproto.MsgJoinChannel), &msg); err != nil || msg.AckRequested || msg.ChannelID != channelID {
				t.Fatalf("legacy request: %+v / %v", msg, err)
			}
		})
	}
}
