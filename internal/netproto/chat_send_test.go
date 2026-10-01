package netproto

import "testing"

func TestChatAcceptedMatches(t *testing.T) {
	for _, scope := range []string{"global", "channel", "unique", "client"} {
		msg := ChatSend{ClientMsgID: "reference"}
		switch scope {
		case "channel":
			msg.ChannelID = "7"
		case "unique":
			msg.ToUniqueID = "peer"
		case "client":
			msg.ToClientID = "connection"
		}
		for _, disposition := range []string{ChatStored, ChatRelayed, ChatQueued, "unknown"} {
			for _, id := range []int64{-1, 0, 42} {
				a := ChatAccepted{ClientMsgID: msg.ClientMsgID, ChannelID: msg.ChannelID, ToUniqueID: msg.ToUniqueID, ToClientID: msg.ToClientID, Disposition: disposition, MessageID: id}
				want := (scope == "global" || scope == "channel") && disposition == ChatStored && id > 0 ||
					(scope == "unique" || scope == "client") && disposition == ChatRelayed && id == 0 ||
					scope == "unique" && disposition == ChatQueued && id == 0
				if a.Matches(msg) != want {
					t.Fatalf("%s/%s/%d: match should be %v", scope, disposition, id, want)
				}
				if !want {
					continue
				}
				for _, field := range []string{"reference", "channel", "unique", "client", "empty reference"} {
					wrong := a
					switch field {
					case "reference":
						wrong.ClientMsgID += "other"
					case "channel":
						wrong.ChannelID += "other"
					case "unique":
						wrong.ToUniqueID += "other"
					case "client":
						wrong.ToClientID += "other"
					case "empty reference":
						wrong.ClientMsgID = ""
					}
					if wrong.Matches(msg) {
						t.Fatalf("%s accepted wrong %s", scope, field)
					}
				}
			}
		}
	}
	if (ChatAccepted{Disposition: ChatStored, MessageID: 42}).Matches(ChatSend{}) {
		t.Fatal("accepted empty reference")
	}
}
