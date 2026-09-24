package main

import (
	"reflect"
	"sync"
	"testing"
	"time"

	"noxa/internal/netproto"
)

func TestVoiceMetadataForTabRejectsActivationAndCopiesICE(t *testing.T) {
	first := &connManager{iceServers: []netproto.ICEServer{{URLs: []string{"stun:first.example"}}}, mediaLimits: netproto.MediaLimits{VideoMaxBitrate: 1000000}}
	second := &connManager{iceServers: []netproto.ICEServer{{URLs: []string{"stun:second.example"}}}, mediaLimits: netproto.MediaLimits{VideoMaxBitrate: 2000000}}
	app := &App{activeID: "a", tabs: map[string]*tabState{"a": {cm: first}, "b": {cm: second}, "offline": {}}}
	app.cmStore(first)
	got, err := app.GetICEServersForTab("a")
	if err != nil || !reflect.DeepEqual(got, first.iceServers) {
		t.Fatalf("ICE = %+v, %v", got, err)
	}
	got[0].URLs[0] = "stun:changed.example"
	got[0].Username = "changed"
	again, err := app.GetICEServersForTab("a")
	if err != nil || again[0].URLs[0] != "stun:first.example" || again[0].Username != "" {
		t.Fatalf("cache was mutated: %+v, %v", again, err)
	}
	limits, err := app.GetMediaLimitsForTab("a")
	if err != nil || limits != first.mediaLimits {
		t.Fatalf("limits = %+v, %v", limits, err)
	}
	app.tabsMu.Lock()
	_, _, _, ok := app.activateLocked("b")
	app.tabsMu.Unlock()
	if !ok {
		t.Fatal("activation failed")
	}
	for _, tab := range []string{"", "a", "missing", "offline"} {
		if _, err := app.GetICEServersForTab(tab); err == nil {
			t.Fatalf("accepted stale ICE read for %q", tab)
		}
		if _, err := app.GetMediaLimitsForTab(tab); err == nil {
			t.Fatalf("accepted stale limits read for %q", tab)
		}
	}
	limits, err = app.GetMediaLimitsForTab("b")
	if err != nil || limits != second.mediaLimits {
		t.Fatalf("new limits = %+v, %v", limits, err)
	}
}

func TestVoiceOfferRetainsCapturedTab(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	app, cm := newPipedApp(t, func(f *netproto.Frame) (netproto.MessageType, any, bool) {
		var offer netproto.WebRTCOffer
		if err := netproto.Decode(f, &offer); err != nil || offer.SDP != "original offer" || len(offer.Tracks) != 1 || offer.Tracks[0].Slot != "mic" {
			t.Errorf("offer = %+v, %v", offer, err)
		}
		close(entered)
		<-release
		return netproto.MsgWebRTCAnswer, netproto.WebRTCAnswer{SDP: "original answer"}, true
	})
	app.activeID = "a"
	app.tabs = map[string]*tabState{"a": {cm: cm}}
	type result struct {
		sdp string
		err error
	}
	done := make(chan result, 1)
	go func() {
		sdp, err := app.WebRTCOfferForTab("a", "original offer", []netproto.TrackSlot{{TrackID: "audio", Slot: "mic"}})
		done <- result{sdp, err}
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("no offer")
	}
	app.tabsMu.Lock()
	app.activeID = "b"
	app.cmStore(&connManager{})
	app.tabsMu.Unlock()
	unblock()
	select {
	case got := <-done:
		if got.err != nil || got.sdp != "original answer" {
			t.Fatalf("answer = %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("no answer")
	}
}

func TestVoiceSignalsReportClosedConnection(t *testing.T) {
	app, cm := newPipedApp(t, func(_ *netproto.Frame) (netproto.MessageType, any, bool) { return 0, nil, false })
	app.activeID = "a"
	app.tabs = map[string]*tabState{"a": {cm: cm}}
	cm.disconnect()
	if err := app.WebRTCAnswerForTab("a", "answer"); err == nil {
		t.Fatal("answer swallowed write failure")
	}
	if err := app.SendICECandidateForTab("a", "candidate", "0", 0); err == nil {
		t.Fatal("candidate swallowed write failure")
	}
}
