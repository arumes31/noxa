package main

import (
	"sync"
	"testing"
	"time"

	"noxa/internal/netproto"
)

func TestRoleChatMutationsWaitForAcknowledgement(t *testing.T) {
	for _, kind := range []netproto.MessageType{netproto.MsgChatEdit, netproto.MsgChatDelete, netproto.MsgChatPin, netproto.MsgChatReact} {
		t.Run(kind.String(), func(t *testing.T) {
			entered := make(chan struct{})
			release := make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			t.Cleanup(unblock)
			app, cm := newPipedApp(t, func(f *netproto.Frame) (netproto.MessageType, any, bool) {
				if netproto.MessageType(f.Type) != kind {
					t.Errorf("request type: %d", f.Type)
				}
				var request struct {
					AckRequested bool `json:"ack_requested"`
				}
				if err := netproto.Decode(f, &request); err != nil || !request.AckRequested {
					t.Errorf("acknowledgement not requested: %+v %v", request, err)
				}
				close(entered)
				<-release
				return netproto.MsgChatMutationSaved, netproto.ChatMutationSaved{Operation: kind, MessageID: 11}, true
			})
			cm.mu.Lock()
			cm.authorizationModel = netproto.AuthorizationModelRolesV1
			cm.mu.Unlock()
			cm.scopeKeys.put(7, 9, randKey(t))
			app.tabs = map[string]*tabState{"a": {cm: cm}}
			app.activeID = "a"
			done := make(chan string, 1)
			go func() { done <- callChatMutationForTest(app, kind) }()
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("no request")
			}
			select {
			case err := <-done:
				t.Fatalf("returned before acknowledgement: %q", err)
			case <-time.After(30 * time.Millisecond):
			}
			// Switching the active native connection must not retarget this wait.
			app.tabsMu.Lock()
			app.activeID = "b"
			app.cmStore(&connManager{})
			app.tabsMu.Unlock()
			unblock()
			select {
			case err := <-done:
				if err != "" {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("acknowledgement did not complete the operation")
			}
		})
	}
}

func TestRoleChatMutationsRejectInvalidAcknowledgements(t *testing.T) {
	for _, kind := range []netproto.MessageType{netproto.MsgChatEdit, netproto.MsgChatDelete, netproto.MsgChatPin, netproto.MsgChatReact} {
		for _, result := range []string{"denied", "wrong operation", "wrong message", "malformed"} {
			t.Run(kind.String()+"/"+result, func(t *testing.T) {
				app, cm := newPipedApp(t, func(*netproto.Frame) (netproto.MessageType, any, bool) {
					saved := netproto.ChatMutationSaved{Operation: kind, MessageID: 11}
					switch result {
					case "denied":
						return netproto.MsgError, netproto.Error{Code: 4, Message: "permission denied", OriginType: uint16(kind)}, true
					case "wrong operation":
						saved.Operation = netproto.MsgPing
					case "wrong message":
						saved.MessageID++
					case "malformed":
						return netproto.MsgChatMutationSaved, []string{"invalid"}, true
					}
					return netproto.MsgChatMutationSaved, saved, true
				})
				cm.mu.Lock()
				cm.authorizationModel = netproto.AuthorizationModelRolesV1
				cm.mu.Unlock()
				cm.scopeKeys.put(7, 9, randKey(t))
				app.tabs = map[string]*tabState{"a": {cm: cm}}
				app.activeID = "a"
				if err := callChatMutationForTest(app, kind); err == "" {
					t.Fatal("invalid reply reported success")
				}
			})
		}
	}
}

func TestLegacyChatMutationsDoNotRequestAcknowledgements(t *testing.T) {
	for _, kind := range []netproto.MessageType{netproto.MsgChatEdit, netproto.MsgChatDelete, netproto.MsgChatPin, netproto.MsgChatReact} {
		t.Run(kind.String(), func(t *testing.T) {
			frames := make(chan *netproto.Frame, 1)
			app, cm := newPipedApp(t, func(f *netproto.Frame) (netproto.MessageType, any, bool) {
				frames <- f
				return 0, nil, false
			})
			cm.scopeKeys.put(7, 9, randKey(t))
			app.tabs = map[string]*tabState{"a": {cm: cm}}
			app.activeID = "a"
			if err := callChatMutationForTest(app, kind); err != "" {
				t.Fatal(err)
			}
			var request struct {
				AckRequested bool `json:"ack_requested"`
			}
			if err := netproto.Decode(nextFrame(t, frames, kind), &request); err != nil || request.AckRequested {
				t.Fatalf("legacy request: %+v %v", request, err)
			}
		})
	}
}

func callChatMutationForTest(app *App, kind netproto.MessageType) string {
	switch kind {
	case netproto.MsgChatEdit:
		return app.ChatEditMessageForTab("a", 7, 11, "edit", 1)
	case netproto.MsgChatDelete:
		return app.ChatDeleteMessageForTab("a", 11)
	case netproto.MsgChatPin:
		return app.ChatPinMessageForTab("a", 7, 11, true)
	default:
		return app.ChatReactForTab("a", 11, "👍")
	}
}
