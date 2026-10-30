package main

import (
	"noxa/internal/netproto"
	"testing"
)

func TestLobbyPrivacyRequiresAcknowledgement(t *testing.T) {
	for _, acknowledged := range []bool{false, true} {
		t.Run(map[bool]string{true: "confirmed", false: "unsupported reply"}[acknowledged], func(t *testing.T) {
			app, cm := newPipedApp(t, func(f *netproto.Frame) (netproto.MessageType, any, bool) {
				var request netproto.SetStatus
				if err := netproto.Decode(f, &request); err != nil || !request.PrivacyOnly || request.HideLobbyPresence == nil || !*request.HideLobbyPresence {
					t.Errorf("bad privacy request: %+v %v", request, err)
				}
				response := netproto.StatusSaved{ClientID: "self", Status: "busy", Message: "unchanged"}
				if acknowledged {
					response.HideLobbyPresence = request.HideLobbyPresence
				}
				return netproto.MsgStatusSaved, response, true
			})
			app.settings.HideLobbyPresence = true
			app.tabs = map[string]*tabState{"a": {cm: cm}}
			cm.supportsLobbyPrivacy = true
			cm.clientID = "self"
			app.settingsGeneration = 1
			err := app.applyAllSettingsEffects(1)
			if app.hotkeyEffectGeneration != 1 {
				t.Fatal("privacy failure skipped local effects")
			}
			if (err == nil) != acknowledged || cm.lobbyPrivacyApplied != acknowledged {
				t.Fatalf("unconfirmed privacy success: %v / %v", err, cm.lobbyPrivacyApplied)
			}
		})
	}
}
