package main

import (
	"strings"
	"sync"
	"testing"
	"time"

	"noxa/internal/netproto"
)

func memberActionForTest(app *App, action string) string {
	if action == "move" {
		return app.MoveClientForTab("a", "target", 7)
	}
	if action == "disconnect" && app.cmLoad().usesRoleAuthorization() {
		return app.DisconnectMemberForTab("a", "target", 4, "reason")
	}
	return app.KickClientForTab("a", "target", action == "kick", action == "ban", "reason", 0)
}

func TestRoleMemberAcknowledgementValidation(t *testing.T) {
	for _, action := range []string{"move", "disconnect", "kick", "ban"} {
		for _, outcome := range []string{"done", "wrong target", "wrong scope", "malformed", "pending", "unconfirmed"} {
			if (outcome == "pending" && action != "kick" && action != "ban") || (outcome == "unconfirmed" && action != "ban") {
				continue
			}
			t.Run(action+"/"+outcome, func(t *testing.T) {
				entered, release := make(chan struct{}), make(chan struct{})
				var once sync.Once
				unblock := func() { once.Do(func() { close(release) }) }
				t.Cleanup(unblock)
				app, cm := newPipedApp(t, func(f *netproto.Frame) (netproto.MessageType, any, bool) {
					var flags struct {
						AckRequested bool `json:"ack_requested"`
					}
					if err := netproto.Decode(f, &flags); err != nil || !flags.AckRequested {
						t.Errorf("did not request acknowledgement: %v", err)
					}
					close(entered)
					<-release
					if action == "move" {
						if outcome == "malformed" {
							return netproto.MsgClientMoved, []string{"invalid"}, true
						}
						result := netproto.ClientMoved{ClientID: "target", ChannelID: 7}
						if outcome == "wrong target" {
							result.ClientID = "other"
						}
						if outcome == "wrong scope" {
							result.ChannelID = 8
						}
						return netproto.MsgClientMoved, result, true
					}
					if outcome == "malformed" {
						return netproto.MsgClientRemoved, []string{"invalid"}, true
					}
					result := netproto.ClientRemoved{ClientID: "target", FromServer: action == "kick", Ban: action == "ban"}
					if action == "disconnect" {
						result.ChannelID = 4
					}
					if action == "ban" {
						result.Persistence = netproto.BanSaved
					}
					switch outcome {
					case "wrong target":
						result.ClientID = "other"
					case "wrong scope":
						result.FromServer = !result.FromServer
					case "pending":
						result.CleanupPending = true
					case "unconfirmed":
						result.Persistence = netproto.BanUnconfirmed
					}
					return netproto.MsgClientRemoved, result, true
				})
				cm.mu.Lock()
				cm.authorizationModel = netproto.AuthorizationModelRolesV1
				cm.mu.Unlock()
				app.tabs = map[string]*tabState{"a": {cm: cm}}
				app.activeID = "a"
				done := make(chan string, 1)
				go func() { done <- memberActionForTest(app, action) }()
				select {
				case <-entered:
				case <-time.After(time.Second):
					t.Fatal("no action")
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
					if outcome == "pending" && !strings.Contains(result, "revoked; resource cleanup is pending") {
						t.Fatalf("lost committed removal: %q", result)
					}
					if outcome == "unconfirmed" && !strings.Contains(result, "sessions revoked; ban persistence is unconfirmed") {
						t.Fatalf("lost unconfirmed ban result: %q", result)
					}
				case <-time.After(time.Second):
					t.Fatal("no result")
				}
			})
		}
	}
}

func TestLegacyMemberActionsDoNotRequestAcknowledgements(t *testing.T) {
	for _, action := range []string{"move", "disconnect", "kick", "ban"} {
		t.Run(action, func(t *testing.T) {
			requests := make(chan *netproto.Frame, 1)
			app, cm := newPipedApp(t, func(f *netproto.Frame) (netproto.MessageType, any, bool) { requests <- f; return 0, nil, false })
			app.tabs = map[string]*tabState{"a": {cm: cm}}
			app.activeID = "a"
			if result := memberActionForTest(app, action); result != "" {
				t.Fatal(result)
			}
			kind := netproto.MsgKickClient
			if action == "move" {
				kind = netproto.MsgMoveClient
			}
			var flags struct {
				AckRequested bool `json:"ack_requested"`
			}
			if err := netproto.Decode(nextFrame(t, requests, kind), &flags); err != nil || flags.AckRequested {
				t.Fatalf("legacy request: %+v / %v", flags, err)
			}
		})
	}
}

func TestRoleMemberActionsWaitForCommit(t *testing.T) {
	for _, action := range []string{"move", "disconnect", "kick", "ban"} {
		t.Run(action, func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			t.Cleanup(unblock)
			app, cm := newPipedApp(t, func(f *netproto.Frame) (netproto.MessageType, any, bool) {
				close(entered)
				<-release
				return netproto.MsgError, netproto.Error{Code: 4, Message: "moderation denied", OriginType: f.Type}, true
			})
			cm.mu.Lock()
			cm.authorizationModel = netproto.AuthorizationModelRolesV1
			cm.mu.Unlock()
			app.tabs = map[string]*tabState{"a": {cm: cm}}
			app.activeID = "a"
			done := make(chan string, 1)
			go func() { done <- memberActionForTest(app, action) }()
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("no action")
			}
			select {
			case result := <-done:
				t.Fatalf("completed before server result: %q", result)
			case <-time.After(25 * time.Millisecond):
			}
			unblock()
			select {
			case result := <-done:
				if result != "moderation denied" {
					t.Fatalf("result: %q", result)
				}
			case <-time.After(time.Second):
				t.Fatal("no result")
			}
		})
	}
}
