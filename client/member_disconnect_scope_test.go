package main

import (
	"strconv"
	"testing"

	"noxa/internal/netproto"
)

func TestDisconnectMemberForTabValidatesSourceAndReply(t *testing.T) {
	for _, returnedChannel := range []int64{0, 4, 5} {
		t.Run(strconv.FormatInt(returnedChannel, 10), func(t *testing.T) {
			app, cm := newPipedApp(t, func(f *netproto.Frame) (netproto.MessageType, any, bool) {
				var msg netproto.KickClient
				if err := netproto.Decode(f, &msg); err != nil || !msg.AckRequested || msg.ExpectedChannelID != 4 || msg.ClientID != "target" || msg.Reason != "reason" || msg.FromServer || msg.Ban {
					t.Errorf("invalid disconnect: %+v / %v", msg, err)
				}
				return netproto.MsgClientRemoved, netproto.ClientRemoved{ClientID: "target", ChannelID: returnedChannel}, true
			})
			cm.mu.Lock()
			cm.authorizationModel = netproto.AuthorizationModelRolesV1
			cm.mu.Unlock()
			app.tabs = map[string]*tabState{"a": {cm: cm}}
			app.activeID = "a"
			if result := app.DisconnectMemberForTab("a", "target", 4, "reason"); (result == "") != (returnedChannel == 4) {
				t.Fatalf("reply accepted incorrectly: %q", result)
			}
		})
	}
}

func TestDisconnectMemberRejectsInvalidContextBeforeWrite(t *testing.T) {
	app, cm := newPipedApp(t, func(*netproto.Frame) (netproto.MessageType, any, bool) {
		t.Error("invalid disconnect reached server")
		return 0, nil, false
	})
	app.tabs = map[string]*tabState{"a": {cm: cm}}
	app.activeID = "a"
	if result := app.DisconnectMemberForTab("a", "target", 4, ""); result == "" {
		t.Fatal("scoped disconnect accepted legacy model")
	}
	cm.mu.Lock()
	cm.authorizationModel = netproto.AuthorizationModelRolesV1
	cm.mu.Unlock()
	for _, tab := range []string{"", "missing"} {
		if result := app.DisconnectMemberForTab(tab, "target", 4, ""); result == "" {
			t.Fatal("accepted invalid tab")
		}
	}
	for _, channel := range []int64{-1, 0} {
		if result := app.DisconnectMemberForTab("a", "target", channel, ""); result == "" {
			t.Fatal("accepted invalid source")
		}
	}
	if result := app.DisconnectMemberForTab("a", "", 4, ""); result == "" {
		t.Fatal("accepted empty target")
	}
	if result := app.KickClientForTab("a", "target", false, false, "", 0); result == "" {
		t.Fatal("old role API bypassed source requirement")
	}
	app.tabsMu.Lock()
	app.activeID = "b"
	app.tabsMu.Unlock()
	if result := app.DisconnectMemberForTab("a", "target", 4, ""); result == "" {
		t.Fatal("accepted old active tab")
	}
}
