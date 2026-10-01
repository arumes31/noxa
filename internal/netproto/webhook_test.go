package netproto

import (
	"strings"
	"testing"
)

func TestWebhookRequestValidation(t *testing.T) {
	for _, tc := range []struct {
		r     WebhookRequest
		valid bool
	}{
		{WebhookRequest{Action: "list", ChannelID: 1}, true},
		{WebhookRequest{Action: "create", ChannelID: 1, Name: "Builds"}, true},
		{WebhookRequest{Action: "revoke", ChannelID: 1, ID: 1}, true},
		{WebhookRequest{Action: "create", ChannelID: 0, Name: "Global"}, false},
		{WebhookRequest{Action: "create", ChannelID: 1, Name: "Spoof\nname"}, false},
		{WebhookRequest{Action: "create", ChannelID: 1, Name: "Hidden\u202Ename"}, false},
		{WebhookRequest{Action: "create", ChannelID: 1, Name: strings.Repeat("a", 61)}, false},
		{WebhookRequest{Action: "create", ChannelID: 1, Name: "   "}, false},
		{WebhookRequest{Action: "revoke", ChannelID: 1, ID: -1}, false},
	} {
		if got := tc.r.Valid(); got != tc.valid {
			t.Errorf("%+v valid=%t", tc.r, got)
		}
	}
}
