package main

import (
	"noxa/internal/netproto"
	"strings"
	"testing"
)

func TestChannelInviteBridgeRejectsStaleScopeAndSecretDisclosure(t *testing.T) {
	app, cm := newPipedApp(t, func(frame *netproto.Frame) (netproto.MessageType, any, bool) {
		var r netproto.ChannelInviteRequest
		if err := netproto.Decode(frame, &r); err != nil {
			t.Fatal(err)
		}
		return netproto.MsgChannelInviteResult, netproto.ChannelInviteResult{Action: r.Action, ChannelID: r.ChannelID, Token: strings.Repeat("A", 43)}, true
	})
	app.tabs = map[string]*tabState{"a": {cm: cm}}
	app.activeID = "a"
	if _, err := app.ChannelInviteForTab("a", netproto.ChannelInviteRequest{Action: "get", ChannelID: 1}); err == nil {
		t.Fatal("secret disclosed by read")
	}
	if _, err := app.ChannelInviteForTab("stale", netproto.ChannelInviteRequest{Action: "get", ChannelID: 1}); err == nil {
		t.Fatal("stale tab accepted")
	}
}
