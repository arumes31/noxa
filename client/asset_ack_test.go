package main

import (
	"strings"
	"sync"
	"testing"
	"time"

	"noxa/internal/netproto"
)

var assetActions = []netproto.MessageType{
	netproto.MsgAvatarSet, netproto.MsgServerIconSet, netproto.MsgServerBannerSet,
	netproto.MsgEmojiUpload, netproto.MsgEmojiDelete, netproto.MsgEmojiRename,
}

func assetActionForTest(app *App, kind netproto.MessageType) string {
	switch kind {
	case netproto.MsgAvatarSet:
		return app.SetAvatarForTab("a", "image")
	case netproto.MsgServerIconSet:
		return app.ServerIconSetForTab("a", "image")
	case netproto.MsgServerBannerSet:
		return app.ServerBannerSetForTab("a", "image")
	case netproto.MsgEmojiUpload:
		return app.EmojiUploadForTab("a", "wave", "image")
	case netproto.MsgEmojiDelete:
		return app.EmojiDeleteForTab("a", "wave")
	case netproto.MsgEmojiRename:
		return app.EmojiRenameForTab("a", "wave", "hello")
	default:
		panic("unknown test action")
	}
}

func TestRoleAssetsWaitForStorageResult(t *testing.T) {
	for _, kind := range assetActions {
		t.Run(kind.String(), func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			t.Cleanup(unblock)
			app, cm := newPipedApp(t, func(f *netproto.Frame) (netproto.MessageType, any, bool) {
				close(entered)
				<-release
				return netproto.MsgError, netproto.Error{Code: 4, Message: "asset denied", OriginType: f.Type}, true
			})
			cm.mu.Lock()
			cm.authorizationModel = netproto.AuthorizationModelRolesV1
			cm.clientID = "self"
			cm.mu.Unlock()
			app.tabs = map[string]*tabState{"a": {cm: cm}}
			app.activeID = "a"
			done := make(chan string, 1)
			go func() { done <- assetActionForTest(app, kind) }()
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("no asset request")
			}
			select {
			case result := <-done:
				t.Fatalf("returned before storage result: %q", result)
			case <-time.After(30 * time.Millisecond):
			}
			unblock()
			select {
			case result := <-done:
				if !strings.Contains(result, "asset denied") {
					t.Fatalf("lost rejection: %q", result)
				}
			case <-time.After(time.Second):
				t.Fatal("no result")
			}
		})
	}
}

func TestRoleAssetAcknowledgementValidation(t *testing.T) {
	for _, kind := range assetActions {
		for _, outcome := range []string{"done", "wrong operation", "wrong client", "wrong name", "wrong new name", "malformed", "empty rejection"} {
			t.Run(kind.String()+"/"+outcome, func(t *testing.T) {
				entered, release := make(chan struct{}), make(chan struct{})
				var once sync.Once
				unblock := func() { once.Do(func() { close(release) }) }
				t.Cleanup(unblock)
				app, cm := newPipedApp(t, func(f *netproto.Frame) (netproto.MessageType, any, bool) {
					var request struct {
						AckRequested bool   `json:"ack_requested"`
						Name         string `json:"name"`
						NewName      string `json:"new_name"`
						DataBase64   string `json:"data_base64"`
					}
					if err := netproto.Decode(f, &request); err != nil || !request.AckRequested || f.Type != uint16(kind) {
						t.Errorf("request: %+v / %v", request, err)
					}
					result := netproto.AssetMutationSaved{Operation: kind, ClientID: "self"}
					if kind == netproto.MsgEmojiUpload || kind == netproto.MsgEmojiDelete || kind == netproto.MsgEmojiRename {
						result.Name = "wave"
					}
					if kind == netproto.MsgEmojiRename {
						result.NewName = "hello"
					}
					if request.Name != result.Name || request.NewName != result.NewName {
						t.Errorf("wrong names: %+v", request)
					}
					if kind != netproto.MsgEmojiDelete && kind != netproto.MsgEmojiRename && request.DataBase64 != "image" {
						t.Errorf("lost image: %+v", request)
					}
					close(entered)
					<-release
					switch outcome {
					case "wrong operation":
						result.Operation = netproto.MsgPing
					case "wrong client":
						result.ClientID = "other"
					case "wrong name":
						result.Name = "other"
					case "wrong new name":
						result.NewName = "other"
					case "malformed":
						return netproto.MsgAssetMutationSaved, []string{"invalid"}, true
					case "empty rejection":
						return netproto.MsgError, netproto.Error{Code: 4, OriginType: f.Type}, true
					}
					return netproto.MsgAssetMutationSaved, result, true
				})
				cm.mu.Lock()
				cm.authorizationModel = netproto.AuthorizationModelRolesV1
				cm.clientID = "self"
				cm.mu.Unlock()
				app.tabs = map[string]*tabState{"a": {cm: cm}}
				app.activeID = "a"
				done := make(chan string, 1)
				go func() { done <- assetActionForTest(app, kind) }()
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

func TestLegacyAssetsDoNotRequestAcknowledgement(t *testing.T) {
	for _, kind := range assetActions {
		t.Run(kind.String(), func(t *testing.T) {
			requests := make(chan *netproto.Frame, 1)
			app, cm := newPipedApp(t, func(f *netproto.Frame) (netproto.MessageType, any, bool) {
				requests <- f
				return 0, nil, false
			})
			app.tabs = map[string]*tabState{"a": {cm: cm}}
			app.activeID = "a"
			if result := assetActionForTest(app, kind); result != "" {
				t.Fatal(result)
			}
			var msg struct {
				AckRequested bool `json:"ack_requested"`
			}
			if err := netproto.Decode(nextFrame(t, requests, kind), &msg); err != nil || msg.AckRequested {
				t.Fatalf("legacy request: %+v / %v", msg, err)
			}
		})
	}
}
