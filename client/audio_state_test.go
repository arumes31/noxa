package main

import (
	"testing"

	"noxa/internal/netproto"
)

func TestAudioStateAcknowledgementMatchesCaller(t *testing.T) {
	for _, tc := range []struct {
		name, clientID          string
		muted, deafened, wantOK bool
	}{
		{"saved", "self", true, true, true}, {"wrong caller", "other", true, true, false},
		{"wrong mute", "self", false, true, false}, {"wrong deafen", "self", true, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app, cm := newPipedApp(t, func(frame *netproto.Frame) (netproto.MessageType, any, bool) {
				var request netproto.AudioStateSet
				if netproto.MessageType(frame.Type) != netproto.MsgAudioStateSet || netproto.Decode(frame, &request) != nil || !request.Muted || !request.Deafened {
					t.Errorf("bad request: %+v", request)
				}
				return netproto.MsgAudioStateSaved, netproto.AudioStateSaved{ClientID: tc.clientID, Muted: tc.muted, Deafened: tc.deafened}, true
			})
			cm.mu.Lock()
			cm.clientID, cm.supportsAudioState = "self", true
			cm.mu.Unlock()
			app.tabs["one"] = &tabState{cm: cm, info: TabInfo{ID: "one"}}
			app.activeID = "one"
			if err := app.SetAudioStateForTab("one", false, true); (err == "") != tc.wantOK {
				t.Fatalf("result=%q wantOK=%v", err, tc.wantOK)
			}
		})
	}
}

func TestAudioStateDoesNotWriteToOlderServer(t *testing.T) {
	app, cm := newPipedApp(t, func(_ *netproto.Frame) (netproto.MessageType, any, bool) {
		t.Error("sent unsupported audio state")
		return 0, nil, false
	})
	app.tabs["one"] = &tabState{cm: cm, info: TabInfo{ID: "one"}}
	app.activeID = "one"
	if err := app.SetAudioStateForTab("one", true, true); err != "" {
		t.Fatal(err)
	}
}
