package netproto

import (
	"strings"
	"testing"
)

func TestChannelInviteValidation(t *testing.T) {
	token := strings.Repeat("A", 43)
	for _, r := range []ChannelInviteRequest{{Action: "get", ChannelID: 1}, {Action: "regenerate", ChannelID: 1, Access: "guest"}, {Action: "resolve", ChannelID: 1, Token: token}} {
		if !r.Valid() {
			t.Fatalf("valid request rejected: %+v", r)
		}
	}
	for _, r := range []ChannelInviteRequest{{Action: "get", ChannelID: 0}, {Action: "get", ChannelID: 1, Token: token}, {Action: "regenerate", ChannelID: 1, Access: "owner"}, {Action: "resolve", ChannelID: 1, Token: "short"}} {
		if r.Valid() {
			t.Fatal("invalid request accepted")
		}
	}
	if ValidInviteToken(strings.Repeat("A", 42) + "B") {
		t.Fatal("noncanonical token accepted")
	}
}
