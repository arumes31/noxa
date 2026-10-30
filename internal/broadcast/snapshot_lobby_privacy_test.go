package broadcast

import (
	"noxa/internal/state"
	"testing"
)

func TestLobbyPrivacyTracksMembershipAndViewer(t *testing.T) {
	sm := newTestManager()
	sm.AddChannel(&state.Channel{ChannelID: 1, Name: "renamed lobby", SystemKind: "home"})
	sm.AddChannel(&state.Channel{ChannelID: 2, Name: "Lobby"}) // Name alone must not hide real voice.
	sm.AddClient(&state.Client{ClientID: "hidden", UniqueID: "hidden-uid", HideLobbyPresence: true})
	sm.AddClient(&state.Client{ClientID: "legacy-home", UniqueID: "legacy-uid", ChannelID: 1, HideLobbyPresence: true})
	if snap := BuildSnapshot(sm, false, "other"); snap.TotalClients != 0 {
		t.Fatal("legacy system-home membership leaked")
	}
	sm.RemoveClient("legacy-home")
	for _, tc := range []struct {
		admin bool
		uid   string
		count int
	}{
		{false, "other", 0}, {false, "hidden-uid", 1}, {true, "administrator", 1},
	} {
		snap := BuildSnapshot(sm, tc.admin, tc.uid)
		if snap.TotalClients != tc.count || len(snap.UnassignedClients) != tc.count {
			t.Fatalf("lobby presence leaked or self/admin missing: %+v", snap)
		}
	}
	if err := sm.JoinChannel("hidden", 2); err != nil {
		t.Fatal(err)
	}
	if snap := BuildSnapshot(sm, false, "other"); snap.TotalClients != 1 {
		t.Fatal("ordinary voice membership hidden")
	}
	if err := sm.LeaveChannel("hidden"); err != nil {
		t.Fatal(err)
	}
	sm.SetLobbyPrivacy("hidden", false)
	if snap := BuildSnapshot(sm, false, "other"); snap.TotalClients != 1 {
		t.Fatal("opt-out not reflected")
	}
}
