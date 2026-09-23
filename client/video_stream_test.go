package main

import (
	"encoding/json"
	"noxa/internal/netproto"
	"testing"
)

func TestVideoStreamAcknowledgementAndScope(t *testing.T) {
	want := netproto.VideoStreamControl{Action: "watch", PublisherID: "publisher", Slot: "screen", Generation: 8, Session: 3, Revision: 1, Active: true}
	for _, variant := range []string{"valid", "stale publication", "stale session", "missing active"} {
		t.Run(variant, func(t *testing.T) {
			app, _ := newPipedApp(t, func(frame *netproto.Frame) (netproto.MessageType, any, bool) {
				var got netproto.VideoStreamControl
				if err := netproto.Decode(frame, &got); err != nil || got.Generation != want.Generation || got.Session != want.Session {
					t.Errorf("wrong request: %+v %v", got, err)
				}
				result := netproto.VideoStreamResult{Action: want.Action, PublisherID: want.PublisherID, Slot: want.Slot, Generation: want.Generation, Session: want.Session, Revision: want.Revision, Active: true, Streams: []netproto.VideoStream{}}
				if variant == "stale publication" {
					result.Generation++
				}
				if variant == "stale session" {
					result.Session++
				}
				if variant == "missing active" {
					data, _ := json.Marshal(result)
					var fields map[string]any
					_ = json.Unmarshal(data, &fields)
					delete(fields, "active")
					return netproto.MsgVideoStreamResult, fields, true
				}
				return netproto.MsgVideoStreamResult, result, true
			})
			app.tabs = map[string]*tabState{"a": {cm: app.cmLoad()}}
			app.activeID = "a"
			if _, err := app.VideoStreamControlForTab("missing", want); err == nil {
				t.Fatal("stale tab accepted")
			}
			_, err := app.VideoStreamControlForTab("a", want)
			if (err == nil) != (variant == "valid") {
				t.Fatalf("acknowledgement %s: %v", variant, err)
			}
		})
	}
}
