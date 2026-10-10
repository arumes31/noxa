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

func TestStreamCodecRequiresAuthenticatedCapability(t *testing.T) {
	app, cm, capture := voiceDiagnosticApp(t, false)
	cm.authorizationModel, cm.clientID = netproto.AuthorizationModelRolesV1, "self"
	if app.SupportsStreamCodecForTab("a") {
		t.Fatal("missing capability accepted")
	}
	if _, err := app.VideoStreamControlForTab("a", netproto.VideoStreamControl{Action: "publish", Slot: "screen", Active: true, Codecs: []string{"h264", "vp8"}}); err == nil || capture.data.Len() != 0 {
		t.Fatal("codec request sent to legacy server")
	}
	cm.supportsStreamCodec = true
	if !app.SupportsStreamCodecForTab("a") || app.SupportsStreamCodecForTab("missing") {
		t.Fatal("codec capability is not tab-scoped")
	}
}

func TestStreamCodecValidatesSelectedCodec(t *testing.T) {
	for _, selected := range []string{"vp8", "h264", "h265", ""} {
		t.Run(selected, func(t *testing.T) {
			app, cm := newPipedApp(t, func(frame *netproto.Frame) (netproto.MessageType, any, bool) {
				var request netproto.VideoStreamControl
				if err := netproto.Decode(frame, &request); err != nil {
					t.Fatal(err)
				}
				return netproto.MsgVideoStreamResult, netproto.VideoStreamResult{Action: request.Action, PublisherID: request.PublisherID, Slot: request.Slot, Generation: 9, Active: true, Streams: []netproto.VideoStream{}, Codec: selected}, true
			})
			cm.mu.Lock()
			cm.clientID, cm.authorizationModel, cm.supportsStreamCodec = "self", netproto.AuthorizationModelRolesV1, true
			cm.mu.Unlock()
			app.tabs, app.activeID = map[string]*tabState{"a": {cm: cm}}, "a"
			_, err := app.VideoStreamControlForTab("a", netproto.VideoStreamControl{Action: "publish", Slot: "screen", Active: true, Codecs: []string{"h264", "vp8"}})
			if (err == nil) != (selected == "vp8" || selected == "h264") {
				t.Fatalf("invalid selected codec accepted: %q %v", selected, err)
			}
		})
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
