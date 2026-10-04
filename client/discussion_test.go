package main

import (
	"testing"
	"time"

	"noxa/internal/netproto"
)

func TestDiscussionBridgeEncryptsAndStripsWireMaterial(t *testing.T) {
	key := randKey(t)
	app, cm := newPipedApp(t, func(frame *netproto.Frame) (netproto.MessageType, any, bool) {
		var request netproto.DiscussionRequest
		if err := netproto.Decode(frame, &request); err != nil {
			t.Error(err)
		}
		if request.Text != "" || request.KeyID != 3 || request.RequestID != "retry-1" {
			t.Errorf("wire metadata=%+v", request)
		}
		if plain, err := openScope(request.BodyEnc, key); err != nil || plain != "Private voice discussion" {
			t.Errorf("wire encryption failed: %q %v", plain, err)
		}
		return netproto.MsgDiscussionResult, netproto.DiscussionResult{Action: request.Action, ChannelID: 7, ThreadID: 11, Messages: []netproto.ChatHistoryEntry{{ID: 1, BodyEnc: request.BodyEnc, KeyID: 3}}}, true
	})
	app.tabs = map[string]*tabState{"a": {cm: cm}}
	app.activeID = "a"
	cm.scopeKeys.put(7, 3, key)
	out, err := app.DiscussionForTab("a", netproto.DiscussionRequest{Action: "send", ChannelID: 7, ThreadID: 11, Text: "Private voice discussion", RequestID: "retry-1"})
	if err != nil || len(out.Messages) != 1 || out.Messages[0].Body != "Private voice discussion" || !out.Messages[0].EncVerified || out.Messages[0].BodyEnc != "" || out.Messages[0].KeyID != 0 || out.Keys != nil {
		t.Fatalf("webview response=%+v error=%v", out, err)
	}
	if _, err = app.DiscussionForTab("stale", netproto.DiscussionRequest{Action: "list", ChannelID: 7}); err == nil {
		t.Fatal("stale tab was accepted")
	}
}

func TestDiscussionBridgeDeletionScopeAndTombstones(t *testing.T) {
	for _, action := range []string{"delete", "delete_message"} {
		t.Run(action, func(t *testing.T) {
			app, cm := newPipedApp(t, func(frame *netproto.Frame) (netproto.MessageType, any, bool) {
				var request netproto.DiscussionRequest
				if err := netproto.Decode(frame, &request); err != nil {
					t.Error(err)
				}
				if request.Action != action || request.ChannelID != 7 || request.ThreadID != 11 || (action == "delete_message" && request.MessageID != 42) {
					t.Errorf("unexpected deletion target: %+v", request)
				}
				out := netproto.DiscussionResult{Action: action, ChannelID: 7, ThreadID: 11}
				if action == "delete_message" {
					out.Messages = []netproto.ChatHistoryEntry{{ID: 42, Deleted: true, Body: "must not render", KeyID: 3}}
				}
				return netproto.MsgDiscussionResult, out, true
			})
			app.tabs = map[string]*tabState{"a": {cm: cm}}
			app.activeID = "a"
			r := netproto.DiscussionRequest{Action: action, ChannelID: 7, ThreadID: 11}
			if action == "delete_message" {
				r.MessageID = 42
			}
			out, err := app.DiscussionForTab("a", r)
			if err != nil {
				t.Fatal(err)
			}
			if action == "delete_message" && (!out.Messages[0].Deleted || out.Messages[0].Body != "" || out.Messages[0].KeyID != 0) {
				t.Fatalf("tombstone leaked content: %+v", out)
			}
		})
	}
}

func TestDiscussionBridgeRetainsOriginKeysAcrossTabChange(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	key := randKey(t)
	var origin *connManager
	app, cm := newPipedApp(t, func(*netproto.Frame) (netproto.MessageType, any, bool) {
		close(entered)
		<-release
		return netproto.MsgDiscussionResult, netproto.DiscussionResult{Action: "get", ChannelID: 7, ThreadID: 11, Messages: []netproto.ChatHistoryEntry{{ID: 1, BodyEnc: mustSeal(t, "Original server", key), KeyID: 3}}, Keys: []netproto.ChannelKey{sealKeyFor(t, origin, 7, 3, key)}}, true
	})
	origin = cm
	other, _ := newPipedApp(t, func(*netproto.Frame) (netproto.MessageType, any, bool) {
		t.Error("redirected to another server")
		return 0, nil, false
	})
	app.tabs = map[string]*tabState{"a": {cm: cm}, "b": {cm: other.cmLoad()}}
	app.activeID = "a"
	type response struct {
		result netproto.DiscussionResult
		err    error
	}
	done := make(chan response, 1)
	go func() {
		out, err := app.DiscussionForTab("a", netproto.DiscussionRequest{Action: "get", ChannelID: 7, ThreadID: 11})
		done <- response{out, err}
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("read did not start")
	}
	app.tabsMu.Lock()
	_, _, _, ok := app.activateLocked("b")
	app.tabsMu.Unlock()
	close(release)
	if !ok {
		t.Fatal("tab activation failed")
	}
	select {
	case got := <-done:
		if got.err != nil || len(got.result.Messages) != 1 || got.result.Messages[0].Body != "Original server" || got.result.Keys != nil {
			t.Fatalf("origin response=%+v error=%v", got.result, got.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("read did not complete")
	}
	if _, ok := other.cmLoad().scopeKeys.get(7, 3); ok {
		t.Fatal("key installed on replacement server")
	}
}
