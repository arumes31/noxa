package netproto

import "testing"

func TestClientRemovedMatches(t *testing.T) {
	for _, action := range []string{"disconnect", "kick", "ban", "server ban"} {
		msg := KickClient{ClientID: "target", FromServer: action == "kick" || action == "server ban", Ban: action == "ban" || action == "server ban"}
		if action == "disconnect" {
			msg.ExpectedChannelID = 4
		}
		for _, persistence := range []BanPersistence{"", BanSaved, BanUnconfirmed, "unknown"} {
			for _, pending := range []bool{false, true} {
				for _, channel := range []int64{-1, 0, 4} {
					result := ClientRemoved{ClientID: msg.ClientID, FromServer: msg.FromServer, Ban: msg.Ban, ChannelID: channel, Persistence: persistence, CleanupPending: pending}
					want := msg.Ban && channel == 0 && (persistence == BanSaved || persistence == BanUnconfirmed) ||
						!msg.Ban && msg.FromServer && channel == 0 && persistence == "" ||
						!msg.Ban && !msg.FromServer && channel > 0 && persistence == "" && !pending
					if result.Matches(msg) != want {
						t.Fatalf("%s: %+v match should be %v", action, result, want)
					}
					if !want {
						continue
					}
					for _, field := range []string{"target", "from server", "ban"} {
						wrong := result
						switch field {
						case "target":
							wrong.ClientID = "other"
						case "from server":
							wrong.FromServer = !wrong.FromServer
						case "ban":
							wrong.Ban = !wrong.Ban
						}
						if wrong.Matches(msg) {
							t.Fatalf("accepted wrong %s", field)
						}
					}
				}
			}
		}
	}
	if (ClientRemoved{ChannelID: 4}).Matches(KickClient{}) {
		t.Fatal("accepted empty target")
	}
}
