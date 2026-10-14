package main

import (
	"testing"

	"noxa/internal/netproto"
)

func TestStreamHEVCRequiresAuthenticatedCapability(t *testing.T) {
	app, cm, capture := voiceDiagnosticApp(t, false)
	cm.clientID, cm.authorizationModel = "self", netproto.AuthorizationModelRolesV1
	cm.supportsStreamCodec, cm.supportsStreamSourceQuality = true, true
	request := netproto.VideoStreamControl{Action: "publish", Slot: "screen", Active: true, QualityMode: "source", Codecs: []string{"h265", "h264", "vp8"}, HEVCLevelID: 153, CodecRevision: 1}
	if app.SupportsStreamHEVCForTab("a") {
		t.Fatal("legacy server advertised HEVC")
	}
	if _, err := app.VideoStreamControlForTab("a", request); err == nil || capture.data.Len() != 0 {
		t.Fatal("HEVC written to old server")
	}
	cm.supportsStreamHEVC = true
	if !app.SupportsStreamHEVCForTab("a") || app.SupportsStreamHEVCForTab("missing") {
		t.Fatal("capability not tab scoped")
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
			if app.SupportsStreamHEVCForTab("a") {
				t.Fatal("unscoped HEVC accepted")
			}
			if _, err := app.VideoStreamControlForTab("a", request); err == nil || capture.data.Len() != 0 {
				t.Fatal("unscoped HEVC transmitted")
			}
		})
	}
}

func TestStreamHEVCFormatValidation(t *testing.T) {
	for _, tc := range []struct {
		codecs          []string
		level           uint8
		supported, want bool
	}{
		{[]string{"h265", "h264", "vp8"}, 153, true, true},
		{[]string{"h265", "vp8"}, 120, true, true},
		{[]string{"h264", "vp8"}, 0, false, true},
		{[]string{"h265", "vp8"}, 0, true, false},
		{[]string{"h265", "vp8"}, 255, true, false},
		{[]string{"h265", "vp8"}, 153, false, false},
		{[]string{"h265"}, 153, true, false},
		{[]string{"vp8"}, 153, true, false},
		{[]string{"h265", "h265", "vp8"}, 153, true, false},
		{[]string{"av1", "vp8"}, 0, true, false},
	} {
		if got := validClientVideoCodecs(tc.codecs, tc.level, tc.supported); got != tc.want {
			t.Errorf("%v/%d/%v: %v", tc.codecs, tc.level, tc.supported, got)
		}
	}
}

func TestStreamHEVCAcknowledgementPinsExactLevel(t *testing.T) {
	for _, level := range []uint8{0, 120, 153, 186} {
		t.Run(string(rune('a'+level%26)), func(t *testing.T) {
			app, cm := newPipedApp(t, func(frame *netproto.Frame) (netproto.MessageType, any, bool) {
				var request netproto.VideoStreamControl
				if err := netproto.Decode(frame, &request); err != nil {
					t.Fatal(err)
				}
				active := true
				return netproto.MsgVideoStreamResult, netproto.VideoStreamResult{Action: request.Action, PublisherID: request.PublisherID, Slot: request.Slot,
					Generation: 42, Active: true, Streams: []netproto.VideoStream{}, QualityMode: "source", UploadActive: &active, Codec: "h265", HEVCLevelID: level, CodecRevision: request.CodecRevision}, true
			})
			cm.mu.Lock()
			cm.clientID, cm.authorizationModel = "self", netproto.AuthorizationModelRolesV1
			cm.supportsStreamCodec, cm.supportsStreamSourceQuality, cm.supportsStreamHEVC = true, true, true
			cm.mu.Unlock()
			app.tabs, app.activeID = map[string]*tabState{"a": {cm: cm}}, "a"
			_, err := app.VideoStreamControlForTab("a", netproto.VideoStreamControl{Action: "publish", Slot: "screen", Active: true, QualityMode: "source", Codecs: []string{"h265", "vp8"}, HEVCLevelID: 153, CodecRevision: 1})
			if (err == nil) != (level == 153) {
				t.Fatalf("level=%d error=%v", level, err)
			}
		})
	}
}

func TestStreamHEVCCodecRevisionScopeAndAcknowledgement(t *testing.T) {
	app, cm, capture := voiceDiagnosticApp(t, false)
	cm.clientID, cm.authorizationModel = "self", netproto.AuthorizationModelRolesV1
	cm.supportsStreamCodec, cm.supportsStreamSourceQuality, cm.supportsStreamHEVC = true, true, true
	for _, msg := range []netproto.VideoStreamControl{
		{Action: "watch", Active: true, Codecs: []string{"h265", "vp8"}, HEVCLevelID: 153, CodecRevision: 1},
		{Action: "publish", Active: false, Codecs: []string{"vp8"}, CodecRevision: 1},
		{Action: "publish", Active: true, QualityMode: "source", Codecs: []string{"h265", "vp8"}, HEVCLevelID: 153},
		{Action: "list", CodecRevision: 1},
	} {
		if _, err := app.VideoStreamControlForTab("a", msg); err == nil || capture.data.Len() != 0 {
			t.Fatalf("invalid revision scope transmitted: %+v", msg)
		}
	}
	for _, acknowledged := range []uint64{0, 1, 2} {
		app, cm := newPipedApp(t, func(frame *netproto.Frame) (netproto.MessageType, any, bool) {
			var request netproto.VideoStreamControl
			if err := netproto.Decode(frame, &request); err != nil {
				t.Fatal(err)
			}
			active := true
			return netproto.MsgVideoStreamResult, netproto.VideoStreamResult{Action: request.Action, PublisherID: request.PublisherID,
				Slot: request.Slot, Generation: 42, Active: true, Streams: []netproto.VideoStream{}, QualityMode: "source",
				UploadActive: &active, Codec: "vp8", CodecRevision: acknowledged}, true
		})
		cm.mu.Lock()
		cm.clientID, cm.authorizationModel = "self", netproto.AuthorizationModelRolesV1
		cm.supportsStreamCodec, cm.supportsStreamSourceQuality, cm.supportsStreamHEVC = true, true, true
		cm.mu.Unlock()
		app.tabs, app.activeID = map[string]*tabState{"a": {cm: cm}}, "a"
		_, err := app.VideoStreamControlForTab("a", netproto.VideoStreamControl{Action: "publish", Slot: "screen", Active: true,
			QualityMode: "source", Codecs: []string{"vp8"}, CodecRevision: 1})
		if (err == nil) != (acknowledged == 1) {
			t.Fatalf("revision=%d err=%v", acknowledged, err)
		}
	}
}
