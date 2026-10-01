package main

import (
	"noxa/internal/netproto"
	"testing"
)

func TestWebhookBridgeUsesOriginAndRejectsSecretInList(t *testing.T) {
	app, cm := newPipedApp(t, func(frame *netproto.Frame) (netproto.MessageType, any, bool) {
		var request netproto.WebhookRequest
		if err := netproto.Decode(frame, &request); err != nil {
			t.Fatal(err)
		}
		return netproto.MsgWebhookResult, netproto.WebhookResult{Action: request.Action, ChannelID: request.ChannelID, Token: "should-not-leak"}, true
	})
	app.tabs = map[string]*tabState{"a": {cm: cm}}
	app.activeID = "a"
	if _, err := app.WebhookForTab("a", netproto.WebhookRequest{Action: "list", ChannelID: 7}); err == nil {
		t.Fatal("list disclosed token")
	}
	if _, err := app.WebhookForTab("stale", netproto.WebhookRequest{Action: "list", ChannelID: 7}); err == nil {
		t.Fatal("stale tab accepted")
	}
}
