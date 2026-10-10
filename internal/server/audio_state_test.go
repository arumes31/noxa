package server

import (
	"testing"

	"noxa/internal/broadcast"
	"noxa/internal/netproto"
)

func TestSelfAudioStateIsPublicSessionMetadata(t *testing.T) {
	env := startTestEnv(t, nil)
	defer env.stop()
	alice, aliceID := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = alice.Close() }()
	bob, bobID := dialAuthed(t, env.addr, "admin-uid")
	defer func() { _ = bob.Close() }()
	for _, controls := range []netproto.AudioStateSet{{Deafened: true}, {Muted: true}, {}} {
		env.state.SetAudioState(aliceID, false, false)
		env.state.SetSpeaking(aliceID, true)
		send(t, alice, netproto.MsgAudioStateSet, controls)
		var saved netproto.AudioStateSaved
		if err := netproto.Decode(readOfType(t, alice, netproto.MsgAudioStateSaved), &saved); err != nil {
			t.Fatal(err)
		}
		if saved.ClientID != aliceID || saved.Muted != (controls.Muted || controls.Deafened) || saved.Deafened != controls.Deafened {
			t.Fatalf("bad acknowledgement: %+v", saved)
		}
		for {
			frame := readOfType(t, bob, netproto.MsgSnapshot)
			var snapshot broadcast.TreeSnapshot
			if err := netproto.Decode(frame, &snapshot); err != nil {
				t.Fatal(err)
			}
			matched := false
			for _, member := range snapshot.UnassignedClients {
				if member.ClientID == aliceID && member.SelfMuted == saved.Muted && member.SelfDeafened == saved.Deafened {
					if saved.Muted && member.IsSpeaking {
						t.Fatal("muted member still advertised as speaking")
					}
					matched = true
				}
			}
			if matched {
				break
			}
		}
		other, _ := env.state.GetClient(bobID)
		if other.SelfMuted || other.SelfDeafened || other.ServerMuted || other.ServerDeafened {
			t.Fatalf("changed another member: %+v", other)
		}
	}
}
