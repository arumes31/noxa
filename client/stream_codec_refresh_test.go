package main

import (
	"noxa/internal/netproto"
	"testing"
)

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
