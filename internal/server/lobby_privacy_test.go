package server

import (
	"noxa/internal/netproto"
	"slices"
	"testing"
)

func TestLobbyPrivacyAuthenticationAndStatusPreservation(t *testing.T) {
	env := startTestEnv(t, nil)
	defer env.stop()
	conn := dialRetry(t, env.addr)
	defer conn.Close()
	send(t, conn, netproto.MsgAuthenticate, netproto.Authenticate{Username: "user-uid", Password: "pw", HideLobbyPresence: true})
	var auth netproto.AuthResponse
	if err := netproto.Decode(readOfType(t, conn, netproto.MsgAuthResponse), &auth); err != nil || !auth.OK {
		t.Fatalf("auth: %+v %v", auth, err)
	}
	if !slices.Contains(auth.Capabilities, netproto.CapabilityLobbyPrivacy) {
		t.Fatal("privacy capability missing")
	}
	current, ok := env.state.GetClient(auth.ClientID)
	if !ok || !current.HideLobbyPresence {
		t.Fatal("privacy was not installed at authentication")
	}
	env.state.SetStatus(auth.ClientID, "busy", "do not disturb")
	for _, hidden := range []bool{false, true} {
		send(t, conn, netproto.MsgSetStatus, netproto.SetStatus{AckRequested: true, PrivacyOnly: true, HideLobbyPresence: &hidden})
		var saved netproto.StatusSaved
		if err := netproto.Decode(readOfType(t, conn, netproto.MsgStatusSaved), &saved); err != nil {
			t.Fatal(err)
		}
		if saved.HideLobbyPresence == nil || *saved.HideLobbyPresence != hidden || saved.Status != "busy" || saved.Message != "do not disturb" {
			t.Fatalf("wrong confirmation: %+v", saved)
		}
	}
	// Old clients' ordinary status updates must preserve the privacy preference.
	send(t, conn, netproto.MsgSetStatus, netproto.SetStatus{AckRequested: true, Status: "away"})
	readOfType(t, conn, netproto.MsgStatusSaved)
	current, _ = env.state.GetClient(auth.ClientID)
	if !current.HideLobbyPresence || current.Status != "away" {
		t.Fatal("status update changed privacy")
	}
}
