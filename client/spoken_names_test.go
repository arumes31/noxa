package main

import (
	"encoding/base64"
	"strings"
	"testing"

	"noxa/internal/netproto"
)

func TestSpokenNamesUseCapableServerAndValidateClips(t *testing.T) {
	calls := 0
	reply := netproto.SpokenNameData{ClientID: "c7", Language: "de", Key: strings.Repeat("a", 64),
		DataBase64: base64.StdEncoding.EncodeToString([]byte("RIFF....WAVE"))}
	app, cm := newPipedApp(t, func(frame *netproto.Frame) (netproto.MessageType, any, bool) {
		calls++
		var req netproto.SpokenNameGet
		if frame.Type != uint16(netproto.MsgSpokenNameGet) || netproto.Decode(frame, &req) != nil || req != (netproto.SpokenNameGet{ClientID: "c7", Language: "de"}) {
			t.Errorf("request %d %+v", frame.Type, req)
		}
		return netproto.MsgSpokenNameData, reply, true
	})
	app.tabs = map[string]*tabState{"a": {cm: cm}}
	app.activeID = "a"
	if _, err := app.GetSpokenNameForTab("a", "c7", "de"); err == nil || calls != 0 {
		t.Fatal("server without the capability was asked")
	}
	cm.mu.Lock()
	cm.supportsSpokenNames = true
	cm.mu.Unlock()
	if _, err := app.GetSpokenNameForTab("a", "c7", "fr"); err == nil || calls != 0 {
		t.Fatal("unsupported language requested")
	}
	if got, err := app.GetSpokenNameForTab("a", "c7", "de"); err != nil || got != reply || calls != 1 {
		t.Fatalf("got %+v, %v, calls %d", got, err, calls)
	}
	reply.DataBase64 = base64.StdEncoding.EncodeToString([]byte("<html>"))
	if _, err := app.GetSpokenNameForTab("a", "c7", "de"); err == nil {
		t.Fatal("non-WAV payload accepted")
	}
}

func TestSpokenNameReadsMayOutliveTheirCaller(t *testing.T) {
	if !canDrainRequest(netproto.MsgSpokenNameGet) {
		t.Fatal("a slow synthesis would disconnect the client")
	}
}
