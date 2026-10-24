package main

import (
	"noxa/internal/netproto"
	"testing"
)

func TestLinkPreviewsUseConnectedServerAndRejectRemoteImages(t *testing.T) {
	calls := 0
	app, cm := newPipedApp(t, func(frame *netproto.Frame) (netproto.MessageType, any, bool) {
		calls++
		if frame.Type != uint16(netproto.MsgLinkPreviewRequest) {
			t.Errorf("unexpected type %d", frame.Type)
		}
		var req netproto.LinkPreviewRequest
		if err := netproto.Decode(frame, &req); err != nil {
			t.Error(err)
		}
		if req.Provider != "youtube" || req.Resource != "Fvr-SJPAh7o" {
			t.Errorf("request %+v", req)
		}
		return netproto.MsgLinkPreviewResult, netproto.LinkPreviewResult{Title: "Server result", Thumbnail: "https://i.ytimg.com/private.jpg"}, true
	})
	app.tabs = map[string]*tabState{"a": {cm: cm}}
	app.activeID = "a"
	cm.mu.Lock()
	cm.supportsLinkPreview = true
	cm.mu.Unlock()
	got, err := app.YouTubePreviewForTab("a", "Fvr-SJPAh7o")
	if err != nil || got.Title != "Server result" || got.Thumbnail != "" || calls != 1 {
		t.Fatalf("result %+v, %v calls=%d", got, err, calls)
	}
	cm.mu.Lock()
	cm.supportsLinkPreview = false
	cm.mu.Unlock()
	if _, err := app.YouTubePreviewForTab("a", "Fvr-SJPAh7o"); err == nil {
		t.Fatal("legacy server silently used direct fetching")
	}
	if calls != 1 {
		t.Fatal("unsupported request sent")
	}
	if _, err := app.YouTubePreviewForTab("different", "Fvr-SJPAh7o"); err == nil {
		t.Fatal("wrong server accepted")
	}
	cm.mu.Lock()
	cm.supportsLinkPreview = true
	cm.mu.Unlock()
	disabled := false
	app.settings.LinkPreviews = &disabled
	if _, err := app.YouTubePreviewForTab("a", "Fvr-SJPAh7o"); err == nil {
		t.Fatal("disabled previews fetched")
	}
	if calls != 1 {
		t.Fatal("disabled request sent")
	}
}
