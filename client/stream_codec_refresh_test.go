package main

import (
	"noxa/internal/netproto"
	"testing"
)

func TestStreamCodecRefreshRequiresAuthenticatedCapability(t *testing.T) {
	app, cm, capture := voiceDiagnosticApp(t, false)
	cm.clientID, cm.authorizationModel = "self", netproto.AuthorizationModelRolesV1
	cm.supportsStreamCodec, cm.supportsStreamSourceQuality = true, true
	request := netproto.VideoStreamControl{Action: "publish", Slot: "screen", Generation: 7, Active: true, QualityMode: "source", Codecs: []string{"vp8", "h264"}}
	if app.SupportsStreamCodecRefreshForTab("a") {
		t.Fatal("legacy server accepted")
	}
	if _, err := app.VideoStreamControlForTab("a", request); err == nil || capture.data.Len() != 0 {
		t.Fatal("refresh written to legacy server")
	}
	cm.supportsStreamCodecRefresh = true
	if !app.SupportsStreamCodecRefreshForTab("a") || app.SupportsStreamCodecRefreshForTab("missing") {
		t.Fatal("refresh support is not tab-scoped")
	}
	for _, field := range []string{"codec", "source", "identity", "model", "connection"} {
		t.Run(field, func(t *testing.T) {
			switch field {
			case "codec":
				cm.supportsStreamCodec = false
				defer func() { cm.supportsStreamCodec = true }()
			case "source":
				cm.supportsStreamSourceQuality = false
				defer func() { cm.supportsStreamSourceQuality = true }()
			case "identity":
				cm.clientID = ""
				defer func() { cm.clientID = "self" }()
			case "model":
				cm.authorizationModel = ""
				defer func() { cm.authorizationModel = netproto.AuthorizationModelRolesV1 }()
			case "connection":
				conn := cm.conn
				cm.conn = nil
				defer func() { cm.conn = conn }()
			}
			if app.SupportsStreamCodecRefreshForTab("a") {
				t.Fatal("unauthenticated/incomplete capability accepted")
			}
			if _, err := app.VideoStreamControlForTab("a", request); err == nil || capture.data.Len() != 0 {
				t.Fatal("unscoped refresh transmitted")
			}
		})
	}
}

func TestStreamCodecRefreshAcknowledgementPinsGeneration(t *testing.T) {
	for _, replacement := range []bool{false, true} {
		t.Run(map[bool]string{false: "same source", true: "replacement"}[replacement], func(t *testing.T) {
			app, cm := newPipedApp(t, func(frame *netproto.Frame) (netproto.MessageType, any, bool) {
				var request netproto.VideoStreamControl
				if err := netproto.Decode(frame, &request); err != nil {
					t.Fatal(err)
				}
				generation := request.Generation
				if replacement {
					generation++
				}
				active := true
				return netproto.MsgVideoStreamResult, netproto.VideoStreamResult{Action: request.Action, PublisherID: request.PublisherID, Slot: request.Slot, Generation: generation, Active: true, Streams: []netproto.VideoStream{}, QualityMode: "source", UploadActive: &active, Codec: "h264"}, true
			})
			cm.mu.Lock()
			cm.clientID, cm.authorizationModel = "self", netproto.AuthorizationModelRolesV1
			cm.supportsStreamCodec, cm.supportsStreamSourceQuality, cm.supportsStreamCodecRefresh = true, true, true
			cm.mu.Unlock()
			app.tabs, app.activeID = map[string]*tabState{"a": {cm: cm}}, "a"
			_, err := app.VideoStreamControlForTab("a", netproto.VideoStreamControl{Action: "publish", Slot: "screen", Generation: 7, Active: true, QualityMode: "source", Codecs: []string{"vp8", "h264"}})
			if (err != nil) != replacement {
				t.Fatalf("replacement=%v: %v", replacement, err)
			}
		})
	}
}
