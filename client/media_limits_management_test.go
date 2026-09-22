package main

import (
	"encoding/json"
	"sync/atomic"
	"testing"

	"noxa/internal/netproto"
)

func TestMediaLimitsManagementBindingsAndTabScope(t *testing.T) {
	want := netproto.MediaLimits{VideoMaxBitrate: 800000, VideoMaxWidth: 640, VideoMaxHeight: 360}
	response := netproto.MediaLimitsSaved{Revision: 7, MediaLimits: want}
	var writes atomic.Int32
	frames := make(chan *netproto.Frame, 1)
	app, _ := newPipedApp(t, func(frame *netproto.Frame) (netproto.MessageType, any, bool) {
		writes.Add(1)
		frames <- frame
		return netproto.MsgMediaLimitsSaved, response, true
	})
	app.tabs = map[string]*tabState{"a": {cm: app.cmLoad()}}
	app.activeID = "a"
	got, err := app.SetMediaLimitsForTab("a", want)
	if err != nil || got != response {
		t.Fatalf("save = %+v, %v", got, err)
	}
	var sent netproto.MediaLimits
	if err := netproto.Decode(nextFrame(t, frames, netproto.MsgMediaLimitsSet), &sent); err != nil || sent != want {
		t.Fatalf("payload = %+v, %v", sent, err)
	}
	for _, tabID := range []string{"", "missing"} {
		if _, err := app.SetMediaLimitsForTab(tabID, want); err == nil {
			t.Fatalf("accepted stale tab %q", tabID)
		}
	}
	if _, err := app.SetMediaLimitsForTab("a", netproto.MediaLimits{VideoMaxWidth: 640}); err == nil {
		t.Fatal("accepted invalid limits")
	}
	if writes.Load() != 1 {
		t.Fatalf("invalid/stale calls reached transport: %d", writes.Load())
	}
}

func TestMediaLimitsManagementRejectsMalformedSavedResponse(t *testing.T) {
	for _, payload := range []string{
		`{"revision":"1","video_max_bitrate":800000,"video_max_width":640}`,
		`{"revision":"1","video_max_bitrate":800000,"video_max_width":640,"video_max_height":0}`,
	} {
		t.Run(payload, func(t *testing.T) {
			_, cm := newPipedApp(t, func(*netproto.Frame) (netproto.MessageType, any, bool) {
				return netproto.MsgMediaLimitsSaved, json.RawMessage(payload), true
			})
			if _, err := cm.setMediaLimits(netproto.MediaLimits{}); err == nil {
				t.Fatal("accepted malformed acknowledgement")
			}
		})
	}
}
