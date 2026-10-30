package main

import (
	"noxa/internal/netproto"
	"path/filepath"
	"testing"
	"time"
)

func TestLobbyPrivacyRequiresAcknowledgement(t *testing.T) {
	for _, hidden := range []bool{false, true} {
		for _, acknowledged := range []bool{false, true} {
			t.Run(map[bool]string{true: "confirmed", false: "unsupported reply"}[acknowledged], func(t *testing.T) {
				app, cm := newPipedApp(t, func(f *netproto.Frame) (netproto.MessageType, any, bool) {
					var request netproto.SetStatus
					if err := netproto.Decode(f, &request); err != nil || !request.PrivacyOnly || request.HideLobbyPresence == nil || *request.HideLobbyPresence != hidden {
						t.Errorf("bad privacy request: %+v %v", request, err)
					}
					response := netproto.StatusSaved{ClientID: "self", Status: "busy", Message: "unchanged"}
					if acknowledged {
						response.HideLobbyPresence = request.HideLobbyPresence
					}
					return netproto.MsgStatusSaved, response, true
				})
				app.settings.HideLobbyPresence = hidden
				app.tabs = map[string]*tabState{"a": {cm: cm}}
				cm.supportsLobbyPrivacy = true
				cm.clientID = "self"
				cm.lobbyPrivacyApplied = !hidden
				app.settingsGeneration = 1
				err := app.applyLobbyPrivacy()
				if localErr := app.applyLocalSettingsEffects(1); localErr != nil {
					t.Fatal(localErr)
				}
				if app.hotkeyEffectGeneration != 1 {
					t.Fatal("privacy failure skipped local effects")
				}
				if (err == nil) != acknowledged || cm.lobbyPrivacyApplied != (hidden == acknowledged) {
					t.Fatalf("unconfirmed privacy success: %v / %v", err, cm.lobbyPrivacyApplied)
				}
			})
		}
	}
}

func TestLobbyPrivacyIndependentServersApplyConcurrently(t *testing.T) {
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	handler := func(f *netproto.Frame) (netproto.MessageType, any, bool) {
		started <- struct{}{}
		<-release
		return netproto.MsgStatusSaved, netproto.StatusSaved{}, true
	}
	app, first := newPipedApp(t, handler)
	_, second := newPipedApp(t, handler)
	first.supportsLobbyPrivacy = true
	second.supportsLobbyPrivacy = true
	app.settings.HideLobbyPresence = true
	app.tabs = map[string]*tabState{"first": {cm: first}, "second": {cm: second}}
	done := make(chan struct{})
	go func() { _ = app.applyLobbyPrivacy(); close(done) }()
	defer func() { close(release); <-done }()
	for range 2 {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("one server's acknowledgement blocked the other request")
		}
	}
}

func TestSavedSettingsReportLobbyPrivacyWarningSeparately(t *testing.T) {
	app, cm := newPipedApp(t, func(*netproto.Frame) (netproto.MessageType, any, bool) {
		return netproto.MsgStatusSaved, netproto.StatusSaved{}, true
	})
	app.settings = DefaultSettings()
	app.settingsPath = filepath.Join(t.TempDir(), "settings.json")
	app.tabs = map[string]*tabState{"a": {cm: cm}}
	cm.supportsLobbyPrivacy = true
	warnings := 0
	app.eventEmit = func(name string, _ any) {
		if name == "settings_lobby_privacy_warning" {
			warnings++
		}
	}
	draft := app.GetSettings()
	draft.HideLobbyPresence = true
	draft.Volume = 75
	if err := app.SaveSettings(draft); err != "" {
		t.Fatal("persisted edits reported as failed: " + err)
	}
	saved := loadSettingsAt(app.settingsPath)
	if !saved.HideLobbyPresence || saved.Volume != 75 || warnings != 1 || cm.lobbyPrivacyApplied {
		t.Fatalf("missing persisted edit or separate warning: hidden=%v volume=%v warnings=%v applied=%v", saved.HideLobbyPresence, saved.Volume, warnings, cm.lobbyPrivacyApplied)
	}
}
