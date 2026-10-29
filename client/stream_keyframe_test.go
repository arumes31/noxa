package main

import (
	"testing"

	"noxa/internal/netproto"
)

// Task 2b: the desktop binding forwards an encoded-streams receiver's
// keyframe request unchanged and accepts the server's acknowledgement.
func TestVideoStreamKeyframeRequestIsForwarded(t *testing.T) {
	var got netproto.VideoStreamControl
	app, cm := newPipedApp(t, func(frame *netproto.Frame) (netproto.MessageType, any, bool) {
		if err := netproto.Decode(frame, &got); err != nil {
			t.Fatal(err)
		}
		return netproto.MsgVideoStreamResult, netproto.VideoStreamResult{Action: got.Action, PublisherID: got.PublisherID, Slot: got.Slot, Active: true, Streams: []netproto.VideoStream{}}, true
	})
	cm.mu.Lock()
	cm.clientID, cm.authorizationModel = "self", netproto.AuthorizationModelRolesV1
	cm.mu.Unlock()
	app.tabs, app.activeID = map[string]*tabState{"a": {cm: cm}}, "a"
	result, err := app.VideoStreamControlForTab("a", netproto.VideoStreamControl{Action: "keyframe", PublisherID: "pub", Slot: "cam", Active: true})
	if err != nil || result.Action != "keyframe" {
		t.Fatalf("keyframe request: %+v %v", result, err)
	}
	if got.Action != "keyframe" || got.PublisherID != "pub" || got.Slot != "cam" || !got.Active || got.Generation != 0 || got.Session != 0 {
		t.Fatalf("forwarded request: %+v", got)
	}
}
