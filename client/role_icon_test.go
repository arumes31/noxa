package main

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"noxa/internal/netproto"
)

func TestRoleIconRejectsMismatchedAcknowledgement(t *testing.T) {
	a, _ := newPipedApp(t, func(frame *netproto.Frame) (netproto.MessageType, any, bool) {
		if netproto.MessageType(frame.Type) == netproto.MsgRoleChannelIconSet {
			return netproto.MsgRoleChannelIconSaved, netproto.RoleChannelIconSaved{ChannelID: 18}, true
		}
		return 0, nil, false
	})
	a.tabs = map[string]*tabState{"a": {cm: a.cmLoad()}}
	a.activeID = "a"
	if result, err := a.SetRoleChannelIconForTab("a", 17, "image", 0); err == nil || result.ChannelID != 0 {
		t.Fatalf("mismatched result: %+v %v", result, err)
	}
}

func TestRoleChannelIconEntryWaitsForStorage(t *testing.T) {
	for _, source := range []int64{0, 8} {
		for _, outcome := range []string{"done", "mismatch", "denied"} {
			t.Run(fmt.Sprintf("%d/%s", source, outcome), func(t *testing.T) {
				entered, release := make(chan struct{}), make(chan struct{})
				var once sync.Once
				unblock := func() { once.Do(func() { close(release) }) }
				t.Cleanup(unblock)
				app, cm := newPipedApp(t, func(f *netproto.Frame) (netproto.MessageType, any, bool) {
					var request netproto.ChannelIconSet
					if err := netproto.Decode(f, &request); err != nil || request.ChannelID != 17 || request.CopyFromChannelID != source {
						t.Errorf("wrong request: %+v / %v", request, err)
					}
					if f.Type != uint16(netproto.MsgRoleChannelIconSet) {
						t.Error("role request used unacknowledged protocol")
					}
					close(entered)
					<-release
					if outcome == "denied" {
						return netproto.MsgError, netproto.Error{Code: 4, OriginType: f.Type, Message: "icon denied"}, true
					}
					id := int64(17)
					if outcome == "mismatch" {
						id++
					}
					return netproto.MsgRoleChannelIconSaved, netproto.RoleChannelIconSaved{ChannelID: id}, true
				})
				cm.mu.Lock()
				cm.authorizationModel = netproto.AuthorizationModelRolesV1
				cm.mu.Unlock()
				app.tabs = map[string]*tabState{"a": {cm: cm}}
				app.activeID = "a"
				data := "image"
				if source != 0 {
					data = ""
				}
				done := make(chan string, 1)
				go func() { done <- app.ChannelIconSetForTab("a", 17, data, source) }()
				select {
				case <-entered:
				case <-time.After(time.Second):
					t.Fatal("no request")
				}
				select {
				case result := <-done:
					t.Fatalf("premature result: %q", result)
				case <-time.After(30 * time.Millisecond):
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
