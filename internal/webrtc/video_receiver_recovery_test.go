package webrtc

import (
	"fmt"
	"testing"
	"time"
)

func TestReceiverRecoveryScopesAndBoundsOutputReplacement(t *testing.T) {
	for _, scenario := range []string{"allowed", "generation", "session", "revision", "paused", "channel", "guard", "cooldown", "budget", "replacement denied"} {
		t.Run(scenario, func(t *testing.T) {
			engine, err := New(testLogger(), nil, false)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = engine.Close() })
			r := NewRouter(nil)
			for _, id := range []string{"pub", "sub", "other"} {
				r.JoinChannel(1, id)
				peer, err := engine.NewPeerConnection(id)
				if err != nil {
					t.Fatal(err)
				}
				if err := r.AttachPeer(id, peer); err != nil {
					t.Fatal(err)
				}
			}
			r.SetTrackSlots("pub", map[string]string{"screen": SlotScreen})
			r.EnsurePublishers("sub")
			r.EnsurePublishers("other")
			generation := testVideoPublication(t, r, "pub", "sub", SlotScreen)
			session, revision := r.VideoWatchSession("sub"), uint64(1)
			peer := r.pubPeers["sub"]
			for i, tr := range peer.pc.GetTransceivers() {
				if err := tr.SetMid(fmt.Sprint(i)); err != nil {
					t.Fatal(err)
				}
			}
			old := r.pubTracks["sub"]["pub"].video[SlotScreen]
			audio := r.pubTracks["sub"]["pub"].audio[SlotMic]
			other := r.pubTracks["other"]["pub"].video[SlotScreen]
			renegotiations := 0
			r.SetRenegotiateHook(func(string) { renegotiations++ })
			switch scenario {
			case "generation":
				generation++
			case "session":
				session++
			case "revision":
				revision++
			case "paused":
				_, _ = r.WatchVideo("sub", "pub", SlotScreen, generation, 2, session, false)
			case "channel":
				r.JoinChannel(2, "sub")
			case "guard":
				r.SetPublisherGuard(func(PublisherAccess) bool { return false })
			case "cooldown":
				peer.videoRecoveryLast = time.Now()
			case "budget":
				peer.videoRecoveryCount = videoReceiverRecoveryLimit
			case "replacement denied":
				calls := 0
				// Exercise a denied new binding after the exact old binding was
				// authorized for removal, without weakening either guard.
				r.publisherGuard = func(PublisherAccess) bool { calls++; return calls == 1 }
			}
			err = r.ResetStreamReceiver("sub", "pub", SlotScreen, generation, session, revision)
			if (err == nil) != (scenario == "allowed") {
				t.Fatalf("scope %s: %v", scenario, err)
			}
			if scenario == "replacement denied" && (renegotiations != 1 || r.pubTracks["sub"]["pub"].video[SlotScreen] != nil || old.egress.active) {
				t.Fatal("failed replacement did not negotiate retired output")
			}
			if scenario != "allowed" {
				return
			}
			if r.pubPeers["sub"] != peer || r.pubTracks["sub"]["pub"].audio[SlotMic] != audio || r.pubTracks["other"]["pub"].video[SlotScreen] != other {
				t.Fatal("recovery changed unrelated peer or media outputs")
			}
			if r.pubTracks["sub"]["pub"].video[SlotScreen] == old || old.egress.active || peer.videoRecoveryCount != 1 {
				t.Fatal("recovery did not retire old guarded output")
			}
			if err := r.ResetStreamReceiver("sub", "pub", SlotScreen, generation, session, revision); err == nil {
				t.Fatal("immediate repeat bypassed recovery cooldown")
			}
		})
	}
}
