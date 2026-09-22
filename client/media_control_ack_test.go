package main

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"noxa/internal/netproto"
)

func TestMediaControlReplyValidation(t *testing.T) {
	want := netproto.MediaControlSaved{Operation: netproto.MsgWhisperSet, ClientID: "self", Active: false, UniqueIDs: []string{"peer"}, ChannelIDs: []int64{3}}
	body, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var valid map[string]any
	if err := json.Unmarshal(body, &valid); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"operation", "client_id", "active", "max_height", "quality", "unique_ids", "channel_ids"} {
		for _, mutation := range []string{"missing", "null", "wrong"} {
			t.Run(field+"/"+mutation, func(t *testing.T) {
				var reply map[string]any
				if err := json.Unmarshal(body, &reply); err != nil {
					t.Fatal(err)
				}
				switch mutation {
				case "missing":
					delete(reply, field)
				case "null":
					reply[field] = nil
				case "wrong":
					reply[field] = map[string]any{"operation": 1, "client_id": "other", "active": true, "max_height": 1080, "quality": "high", "unique_ids": []string{"other"}, "channel_ids": []int64{4}}[field]
				}
				payload, err := json.Marshal(reply)
				if err != nil {
					t.Fatal(err)
				}
				if err := validateMediaControlReply(&netproto.Frame{Payload: payload}, want); err == nil {
					t.Fatal("accepted invalid reply")
				}
			})
		}
	}
	if err := validateMediaControlReply(&netproto.Frame{Payload: body}, want); err != nil {
		t.Fatal(err)
	}
}

func TestMediaControlReplyRetainsOriginalTab(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	app, cm := newPipedApp(t, func(f *netproto.Frame) (netproto.MessageType, any, bool) {
		var msg netproto.ScreenShare
		if err := netproto.Decode(f, &msg); err != nil || !msg.AckRequested {
			t.Errorf("request: %+v / %v", msg, err)
		}
		close(entered)
		<-release
		return netproto.MsgMediaControlSaved, netproto.MediaControlSaved{Operation: netproto.MsgScreenShare, ClientID: "self", Active: true, MaxHeight: 720, UniqueIDs: []string{}, ChannelIDs: []int64{}}, true
	})
	cm.mu.Lock()
	cm.authorizationModel, cm.clientID = netproto.AuthorizationModelRolesV1, "self"
	cm.mu.Unlock()
	app.activeID, app.tabs = "a", map[string]*tabState{"a": {cm: cm}}
	done := make(chan string, 1)
	go func() { done <- app.SetScreenShareQualityForTab("a", true, 720) }()
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
		if got != "" {
			t.Fatal(got)
		}
	case <-time.After(time.Second):
		t.Fatal("no reply")
	}
}

func TestRoleMediaControlsWaitForConfirmation(t *testing.T) {
	for _, tc := range []struct {
		name string
		kind netproto.MessageType
		call func(*App) string
	}{
		{"priority", netproto.MsgPrioritySpeaker, func(a *App) string { return a.SetPrioritySpeaker(true) }},
		{"whisper", netproto.MsgWhisperSet, func(a *App) string { return a.WhisperSet([]string{"peer"}, []int64{1}, true) }},
		{"screen", netproto.MsgScreenShare, func(a *App) string { return a.SetScreenShareQuality(true, 720) }},
		{"quality", netproto.MsgVideoQuality, func(a *App) string { return a.SetVideoQuality("mid") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			defer unblock()
			app, cm := newPipedApp(t, func(f *netproto.Frame) (netproto.MessageType, any, bool) {
				close(entered)
				<-release
				return netproto.MsgError, netproto.Error{Message: "control denied", OriginType: uint16(tc.kind)}, true
			})
			cm.mu.Lock()
			cm.authorizationModel, cm.clientID = netproto.AuthorizationModelRolesV1, "self"
			cm.mu.Unlock()
			done := make(chan string, 1)
			go func() { done <- tc.call(app) }()
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("no request")
			}
			select {
			case got := <-done:
				t.Fatalf("returned before confirmation: %q", got)
			case <-time.After(25 * time.Millisecond):
			}
			unblock()
			select {
			case got := <-done:
				if got != "control denied" {
					t.Fatalf("result: %q", got)
				}
			case <-time.After(time.Second):
				t.Fatal("no result")
			}
		})
	}
}
