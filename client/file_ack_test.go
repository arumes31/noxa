package main

import (
	"strings"
	"sync"
	"testing"
	"time"

	"noxa/internal/netproto"
)

func fileActionForTest(app *App, action string) string {
	if action == "delete" {
		return app.FileDeleteForTab("a", 7, "docs", "report.txt")
	}
	target := int64(0)
	if action == "move" {
		target = 8
	}
	return app.FileRenameForTab("a", 7, "docs", "report.txt", "archive", "renamed.txt", target)
}

func TestRoleFileMutationsWaitForResult(t *testing.T) {
	for _, action := range []string{"delete", "rename", "move"} {
		t.Run(action, func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			t.Cleanup(unblock)
			app, cm := newPipedApp(t, func(f *netproto.Frame) (netproto.MessageType, any, bool) {
				close(entered)
				<-release
				return netproto.MsgError, netproto.Error{Code: 4, Message: "file mutation denied", OriginType: f.Type}, true
			})
			cm.mu.Lock()
			cm.authorizationModel = netproto.AuthorizationModelRolesV1
			cm.clientID = "self"
			cm.mu.Unlock()
			app.tabs = map[string]*tabState{"a": {cm: cm}}
			app.activeID = "a"
			done := make(chan string, 1)
			go func() { done <- fileActionForTest(app, action) }()
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("no request")
			}
			select {
			case result := <-done:
				t.Fatalf("returned before mutation result: %q", result)
			case <-time.After(30 * time.Millisecond):
			}
			unblock()
			select {
			case result := <-done:
				if !strings.Contains(result, "file mutation denied") {
					t.Fatalf("lost rejection: %q", result)
				}
			case <-time.After(time.Second):
				t.Fatal("no result")
			}
		})
	}
}

func TestRoleFileAcknowledgementValidation(t *testing.T) {
	for _, action := range []string{"delete", "rename", "move"} {
		for _, outcome := range []string{"done", "operation", "client", "channel", "folder", "name", "destination", "new folder", "new name", "malformed", "empty rejection"} {
			t.Run(action+"/"+outcome, func(t *testing.T) {
				entered, release := make(chan struct{}), make(chan struct{})
				var once sync.Once
				unblock := func() { once.Do(func() { close(release) }) }
				t.Cleanup(unblock)
				app, cm := newPipedApp(t, func(f *netproto.Frame) (netproto.MessageType, any, bool) {
					var msg netproto.FileRename
					if err := netproto.Decode(f, &msg); err != nil || !msg.AckRequested {
						t.Errorf("request: %+v / %v", msg, err)
					}
					kind := netproto.MsgFileRename
					result := netproto.FileMutationSaved{ClientID: "self", ChannelID: 7, Folder: "docs", Name: "report.txt"}
					if action == "delete" {
						kind = netproto.MsgFileDelete
					} else {
						result.NewFolder, result.NewName = "archive", "renamed.txt"
						if action == "move" {
							result.NewChannelID = 8
						}
					}
					result.Operation = kind
					if f.Type != uint16(kind) || msg.ChannelID != result.ChannelID || msg.Folder != result.Folder || msg.Name != result.Name || msg.NewChannelID != result.NewChannelID || msg.NewFolder != result.NewFolder || msg.NewName != result.NewName {
						t.Errorf("wrong file request: %+v", msg)
					}
					close(entered)
					<-release
					switch outcome {
					case "operation":
						result.Operation = netproto.MsgPing
					case "client":
						result.ClientID = "other"
					case "channel":
						result.ChannelID++
					case "folder":
						result.Folder = "other"
					case "name":
						result.Name = "other.txt"
					case "destination":
						result.NewChannelID++
					case "new folder":
						result.NewFolder = "other"
					case "new name":
						result.NewName = "other.txt"
					case "malformed":
						return netproto.MsgFileMutationSaved, []string{"invalid"}, true
					case "empty rejection":
						return netproto.MsgError, netproto.Error{Code: 4, OriginType: f.Type}, true
					}
					return netproto.MsgFileMutationSaved, result, true
				})
				cm.mu.Lock()
				cm.authorizationModel = netproto.AuthorizationModelRolesV1
				cm.clientID = "self"
				cm.mu.Unlock()
				app.tabs = map[string]*tabState{"a": {cm: cm}}
				app.activeID = "a"
				done := make(chan string, 1)
				go func() { done <- fileActionForTest(app, action) }()
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

func TestLegacyFileMutationsDoNotRequestAcknowledgement(t *testing.T) {
	for _, action := range []string{"delete", "rename", "move"} {
		t.Run(action, func(t *testing.T) {
			requests := make(chan *netproto.Frame, 1)
			app, cm := newPipedApp(t, func(f *netproto.Frame) (netproto.MessageType, any, bool) { requests <- f; return 0, nil, false })
			app.tabs = map[string]*tabState{"a": {cm: cm}}
			app.activeID = "a"
			if result := fileActionForTest(app, action); result != "" {
				t.Fatal(result)
			}
			kind := netproto.MsgFileRename
			if action == "delete" {
				kind = netproto.MsgFileDelete
			}
			var msg netproto.FileRename
			if err := netproto.Decode(nextFrame(t, requests, kind), &msg); err != nil || msg.AckRequested {
				t.Fatalf("legacy request: %+v / %v", msg, err)
			}
		})
	}
}

func TestFileAcknowledgementRequiresExplicitZeroFields(t *testing.T) {
	for _, field := range []string{"operation", "client_id", "channel_id", "folder", "name", "new_channel_id", "new_folder", "new_name"} {
		for _, outcome := range []string{"complete", "omitted", "null"} {
			t.Run(field+"/"+outcome, func(t *testing.T) {
				app, cm := newPipedApp(t, func(*netproto.Frame) (netproto.MessageType, any, bool) {
					result := map[string]any{"operation": netproto.MsgFileDelete, "client_id": "self", "channel_id": 0, "folder": "", "name": "report.txt", "new_channel_id": 0, "new_folder": "", "new_name": ""}
					if outcome == "omitted" {
						delete(result, field)
					}
					if outcome == "null" {
						result[field] = nil
					}
					return netproto.MsgFileMutationSaved, result, true
				})
				cm.mu.Lock()
				cm.authorizationModel = netproto.AuthorizationModelRolesV1
				cm.clientID = "self"
				cm.mu.Unlock()
				app.tabs = map[string]*tabState{"a": {cm: cm}}
				app.activeID = "a"
				if result := app.FileDeleteForTab("a", 0, "", "report.txt"); (result == "") != (outcome == "complete") {
					t.Fatalf("result: %q", result)
				}
			})
		}
	}
}
