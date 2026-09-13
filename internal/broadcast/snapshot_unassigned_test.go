package broadcast

import (
	"encoding/json"
	"testing"

	"voicx/internal/state"
)

func TestSnapshotIncludesVisibleClientsBeforeTheyJoinAChannel(t *testing.T) {
	sm := newTestManager()
	sm.AddChannel(&state.Channel{ChannelID: 1, Name: "Echo Test"})
	sm.AddClient(&state.Client{ClientID: "alpha", UniqueID: "uid-alpha", Nickname: "ALPHA"})
	sm.AddClient(&state.Client{ClientID: "bravo", UniqueID: "uid-bravo", Nickname: "BRAVO"})
	sm.AddClient(&state.Client{ClientID: "hidden", UniqueID: "uid-hidden", Nickname: "Hidden", Status: "invisible"})

	for _, tc := range []struct {
		name   string
		admin  bool
		viewer string
		want   int
	}{
		{name: "ordinary viewer", viewer: "uid-alpha", want: 2},
		{name: "administrator", admin: true, want: 3},
		{name: "invisible self", viewer: "uid-hidden", want: 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload, err := json.Marshal(BuildSnapshot(sm, tc.admin, tc.viewer))
			if err != nil {
				t.Fatal(err)
			}
			var wire struct {
				Unassigned []*ClientInfo `json:"unassigned_clients"`
				Total      int           `json:"total_clients"`
			}
			if err := json.Unmarshal(payload, &wire); err != nil {
				t.Fatal(err)
			}
			if len(wire.Unassigned) != tc.want || wire.Total != tc.want {
				t.Fatalf("snapshot has %d unassigned records, %d total; want %d visible clients", len(wire.Unassigned), wire.Total, tc.want)
			}
			for _, client := range wire.Unassigned {
				if client.ChannelID != 0 || client.UniqueID == "" || client.Nickname == "" {
					t.Fatalf("incomplete unassigned identity: %+v", client)
				}
				if !tc.admin && tc.viewer != "uid-hidden" && client.ClientID == "hidden" {
					t.Fatal("invisible user exposed to an ordinary viewer")
				}
			}
		})
	}
	if err := sm.JoinChannel("bravo", 1); err != nil {
		t.Fatal(err)
	}
	snap := BuildSnapshot(sm, false, "uid-alpha")
	if len(snap.RootChannels[0].Clients) != 1 || snap.RootChannels[0].Clients[0].ClientID != "bravo" {
		t.Fatal("joining client not represented in its channel")
	}
	if len(snap.UnassignedClients) != 1 || snap.UnassignedClients[0].ClientID != "alpha" || snap.TotalClients != 2 {
		t.Fatal("joining client was duplicated or remaining unassigned client was lost")
	}
}
