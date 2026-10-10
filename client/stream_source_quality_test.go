package main

import (
	"testing"

	"noxa/internal/netproto"
)

func TestStreamSourceQualityRequiresCapability(t *testing.T) {
	app, cm, capture := voiceDiagnosticApp(t, false)
	cm.authorizationModel, cm.clientID = netproto.AuthorizationModelRolesV1, "self"
	request := netproto.VideoStreamControl{Action: "publish", Slot: "screen", Active: true, QualityMode: "source"}
	if app.SupportsStreamSourceQualityForTab("a") {
		t.Fatal("unsupported capability advertised")
	}
	if _, err := app.VideoStreamControlForTab("a", request); err == nil || capture.data.Len() != 0 {
		t.Fatal("source publication written to unsupported server")
	}
	cm.supportsStreamSourceQuality = true
	if !app.SupportsStreamSourceQualityForTab("a") || app.SupportsStreamSourceQualityForTab("missing") {
		t.Fatal("source capability not pinned to authenticated tab")
	}
}

func TestStreamSourceQualityInitialReplyAndOwnCatalog(t *testing.T) {
	for _, test := range []string{"valid", "missing feedback", "wrong mode", "other publisher feedback", "legacy unknown"} {
		t.Run(test, func(t *testing.T) {
			app, cm := newPipedApp(t, func(frame *netproto.Frame) (netproto.MessageType, any, bool) {
				var request netproto.VideoStreamControl
				if err := netproto.Decode(frame, &request); err != nil {
					t.Fatal(err)
				}
				active := false
				reply := netproto.VideoStreamResult{Action: request.Action, PublisherID: request.PublisherID, Slot: request.Slot, Generation: 9,
					Active: request.Active, Streams: []netproto.VideoStream{}, QualityMode: "source", UploadActive: &active}
				switch test {
				case "missing feedback":
					reply.UploadActive = nil
				case "wrong mode":
					reply.QualityMode = ""
				case "other publisher feedback":
					reply.Streams = []netproto.VideoStream{{PublisherID: "other", Slot: "screen", Generation: 3, QualityMode: "source", UploadActive: &active}}
				case "legacy unknown":
					reply.QualityMode, reply.UploadActive = "", nil
				}
				return netproto.MsgVideoStreamResult, reply, true
			})
			cm.mu.Lock()
			cm.clientID, cm.authorizationModel, cm.supportsStreamSourceQuality = "self", netproto.AuthorizationModelRolesV1, true
			cm.mu.Unlock()
			app.tabs, app.activeID = map[string]*tabState{"a": {cm: cm}}, "a"
			mode := "source"
			if test == "legacy unknown" {
				mode = ""
			}
			_, err := app.VideoStreamControlForTab("a", netproto.VideoStreamControl{Action: "publish", Slot: "screen", Active: true, QualityMode: mode})
			if (err == nil) != (test == "valid" || test == "legacy unknown") {
				t.Fatalf("reply accepted incorrectly: %v", err)
			}
		})
	}
}
