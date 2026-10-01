package main

import (
	"sync/atomic"
	"testing"

	"noxa/internal/netproto"
)

func TestServerConfigRejectsTabSwitchBeforeFrontendReset(t *testing.T) {
	want := netproto.ServerConfig{MaxClients: 100, ClientTimeoutSeconds: 90, OpusBitrate: 64000}
	var writesA, writesB atomic.Int32
	backend := func(count *atomic.Int32) func(*netproto.Frame) (netproto.MessageType, any, bool) {
		return func(frame *netproto.Frame) (netproto.MessageType, any, bool) {
			count.Add(1)
			return netproto.MsgServerConfigResponse, want, true
		}
	}
	app, _ := newPipedApp(t, backend(&writesA))
	other, _ := newPipedApp(t, backend(&writesB))
	app.tabs = map[string]*tabState{"a": {cm: app.cmLoad()}, "b": {cm: other.cmLoad()}}
	app.activeID = "a"
	if got, err := app.GetServerConfigForTab("a"); err != nil || got != want {
		t.Fatalf("current tab read: %+v, %v", got, err)
	}
	if got, err := app.SetServerConfigForTab("a", want); err != nil || got != want {
		t.Fatalf("current tab write: %+v, %v", got, err)
	}
	// Commit native activation without publishing tab_reset to JavaScript.
	app.tabsMu.Lock()
	_, _, _, ok := app.activateLocked("b")
	app.tabsMu.Unlock()
	if !ok {
		t.Fatal("native activation failed")
	}
	for _, tabID := range []string{"", "a", "missing"} {
		if _, err := app.GetServerConfigForTab(tabID); err == nil {
			t.Fatalf("accepted stale query for %q", tabID)
		}
		if _, err := app.SetServerConfigForTab(tabID, want); err == nil {
			t.Fatalf("accepted stale save for %q", tabID)
		}
	}
	if writesA.Load() != 2 || writesB.Load() != 0 {
		t.Fatalf("stale requests reached transports: a=%d b=%d", writesA.Load(), writesB.Load())
	}
	if got, err := app.GetServerConfigForTab("b"); err != nil || got != want {
		t.Fatalf("new tab read: %+v, %v", got, err)
	}
}

func TestServerConfigBindings(t *testing.T) {
	if _, err := (&App{}).GetServerConfig(); err == nil || err.Error() != "not connected" {
		t.Fatalf("offline GetServerConfig error = %v", err)
	}
	if _, err := (&App{}).SetServerConfig(netproto.ServerConfig{}); err == nil || err.Error() != "not connected" {
		t.Fatalf("offline SetServerConfig error = %v", err)
	}

	frames := make(chan *netproto.Frame, 2)
	want := netproto.ServerConfig{
		MaxClients: 100, ClientTimeoutSeconds: 30, OpusBitrate: 64_000,
		OpusFEC: true, OpusDTX: true, OpusStereo: true,
	}
	app, _ := newPipedApp(t, func(frame *netproto.Frame) (netproto.MessageType, any, bool) {
		frames <- frame
		switch netproto.MessageType(frame.Type) {
		case netproto.MsgServerConfigQuery, netproto.MsgServerConfigSet:
			return netproto.MsgServerConfigResponse, want, true
		default:
			return 0, nil, false
		}
	})

	got, err := app.GetServerConfig()
	if err != nil || got != want {
		t.Fatalf("GetServerConfig = %+v, %v", got, err)
	}
	nextFrame(t, frames, netproto.MsgServerConfigQuery)

	got, err = app.SetServerConfig(want)
	if err != nil || got != want {
		t.Fatalf("SetServerConfig = %+v, %v", got, err)
	}
	var sent netproto.ServerConfig
	if err := netproto.Decode(nextFrame(t, frames, netproto.MsgServerConfigSet), &sent); err != nil || sent != want {
		t.Fatalf("SetServerConfig payload = %+v, %v", sent, err)
	}
}
