package main

import (
	"testing"

	"noxa/internal/netproto"
)

func TestChatEditForTabUsesOriginalKeyAndRejectsStaleTabs(t *testing.T) {
	frames := make(chan *netproto.Frame, 4)
	app, cm := newPipedApp(t, func(f *netproto.Frame) (netproto.MessageType, any, bool) {
		frames <- f
		return 0, nil, false
	})
	other, _ := newPipedApp(t, func(*netproto.Frame) (netproto.MessageType, any, bool) {
		t.Error("edit reached replacement server")
		return 0, nil, false
	})
	app.tabs = map[string]*tabState{"a": {cm: cm}, "b": {cm: other.cmLoad()}}
	app.activeID = "a"
	key := randKey(t)
	cm.scopeKeys.put(7, 9, key)
	other.cmLoad().scopeKeys.put(7, 9, randKey(t))
	if err := app.ChatEditMessageForTab("a", 7, 11, "revised message", 4); err != "" {
		t.Fatal(err)
	}
	var edit netproto.ChatEdit
	if err := netproto.Decode(nextFrame(t, frames, netproto.MsgChatEdit), &edit); err != nil {
		t.Fatal(err)
	}
	if edit.MessageID != 11 || edit.ExpectedVersion != 4 || edit.KeyID != 9 || !edit.Enc {
		t.Fatalf("wrong edit metadata: %+v", edit)
	}
	if plain, err := openScope(edit.NewText, key); err != nil || plain != "revised message" {
		t.Fatalf("wrong encryption: %q / %v", plain, err)
	}
	if err := app.ChatEditMessageForTab("a", 8, 11, "no key", 4); err == "" {
		t.Fatal("accepted edit without channel key")
	}
	app.tabsMu.Lock()
	_, _, _, ok := app.activateLocked("b")
	app.tabsMu.Unlock()
	if !ok {
		t.Fatal("activation failed")
	}
	for _, tab := range []string{"", "a", "missing"} {
		if err := app.ChatEditMessageForTab(tab, 7, 11, "stale message", 4); err == "" {
			t.Fatalf("accepted stale tab %q", tab)
		}
	}
	select {
	case frame := <-frames:
		t.Fatalf("unexpected write: %+v", frame)
	default:
	}
}
