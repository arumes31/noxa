package main

import (
	"testing"
	"time"

	"noxa/internal/netproto"
)

func TestAvatarReadRetainsCapturedTab(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	app, first := newPipedApp(t, func(f *netproto.Frame) (netproto.MessageType, any, bool) {
		if netproto.MessageType(f.Type) != netproto.MsgAvatarGet {
			return 0, nil, false
		}
		close(entered)
		<-release
		return netproto.MsgAvatarData, netproto.AvatarData{UniqueID: "peer", DataBase64: "cG5n"}, true
	})
	_, second := newPipedApp(t, func(_ *netproto.Frame) (netproto.MessageType, any, bool) {
		return netproto.MsgError, netproto.Error{Message: "wrong server"}, true
	})
	app.tabs = map[string]*tabState{"a": {cm: first}, "b": {cm: second}, "offline": {}}
	app.activeID = "a"
	type result struct {
		data netproto.AvatarData
		err  error
	}
	done := make(chan result, 1)
	go func() {
		data, err := app.GetAvatarForTab("a", "peer")
		done <- result{data, err}
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("avatar request did not reach first server")
	}
	app.tabsMu.Lock()
	_, _, _, ok := app.activateLocked("b")
	app.tabsMu.Unlock()
	if !ok {
		t.Fatal("activation failed")
	}
	for _, tab := range []string{"", "a", "missing", "offline"} {
		if _, err := app.GetAvatarForTab(tab, "peer"); err == nil {
			t.Fatalf("accepted stale avatar read for %q", tab)
		}
	}
	release <- struct{}{}
	select {
	case got := <-done:
		if got.err != nil || got.data.UniqueID != "peer" || got.data.DataBase64 != "cG5n" {
			t.Fatalf("captured avatar = %+v, %v", got.data, got.err)
		}
	case <-time.After(time.Second):
		t.Fatal("captured avatar did not complete")
	}
}
