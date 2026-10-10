package main

import (
	"testing"

	"noxa/internal/netproto"
)

func TestStreamVideoQualityRequiresCapabilityAndValidScope(t *testing.T) {
	app, cm, capture := voiceDiagnosticApp(t, false)
	cm.authorizationModel = netproto.AuthorizationModelRolesV1
	cm.clientID = "self"
	if app.SupportsStreamVideoQualityForTab("a") {
		t.Fatal("unsupported capability advertised")
	}
	if got := app.SetStreamVideoQualityForTab("a", "pub", "screen", "1", "2", "high"); got == "" {
		t.Fatal("unsupported request accepted")
	}
	cm.supportsStreamVideoQuality = true
	if !app.SupportsStreamVideoQualityForTab("a") {
		t.Fatal("capability unavailable")
	}
	for _, args := range [][5]string{{"", "screen", "1", "2", "high"}, {"pub", "mic", "1", "2", "high"}, {"pub", "screen", "0", "2", "high"}, {"pub", "screen", "1", "bad", "high"}, {"pub", "screen", "1", "2", "ultra"}} {
		if got := app.SetStreamVideoQualityForTab("a", args[0], args[1], args[2], args[3], args[4]); got == "" {
			t.Fatalf("invalid request accepted: %v", args)
		}
	}
	if capture.data.Len() != 0 {
		t.Fatal("unsupported/invalid request changed global quality")
	}
}

func TestStreamVideoQualityAcknowledgementAndTabScope(t *testing.T) {
	for _, wrong := range []string{"", "publisher", "slot", "generation", "session"} {
		t.Run("wrong="+wrong, func(t *testing.T) {
			app, cm := newPipedApp(t, func(frame *netproto.Frame) (netproto.MessageType, any, bool) {
				var got netproto.VideoQuality
				if err := netproto.Decode(frame, &got); err != nil || got.PublisherID != "pub" || got.Slot != "screen" || got.Generation != 9007199254740993 || got.Session != 2 || !got.AckRequested {
					t.Errorf("wrong request: %+v / %v", got, err)
				}
				saved := netproto.MediaControlSaved{Operation: netproto.MsgVideoQuality, ClientID: "self", Quality: got.Quality, PublisherID: got.PublisherID, Slot: got.Slot, Generation: got.Generation, Session: got.Session, UniqueIDs: []string{}, ChannelIDs: []int64{}}
				switch wrong {
				case "publisher":
					saved.PublisherID = "other"
				case "slot":
					saved.Slot = "cam"
				case "generation":
					saved.Generation++
				case "session":
					saved.Session++
				}
				return netproto.MsgMediaControlSaved, saved, true
			})
			cm.mu.Lock()
			cm.authorizationModel, cm.clientID, cm.supportsStreamVideoQuality = netproto.AuthorizationModelRolesV1, "self", true
			cm.mu.Unlock()
			app.tabs, app.activeID = map[string]*tabState{"a": {cm: cm}}, "a"
			if got := app.SetStreamVideoQualityForTab("missing", "pub", "screen", "9007199254740993", "2", "high"); got == "" {
				t.Fatal("missing tab accepted")
			}
			got := app.SetStreamVideoQualityForTab("a", "pub", "screen", "9007199254740993", "2", "high")
			if (got == "") != (wrong == "") {
				t.Fatalf("ack result = %q", got)
			}
		})
	}
}
