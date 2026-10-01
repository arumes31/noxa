package netproto

import "testing"

func TestDiscussionRequestValidation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		r     DiscussionRequest
		valid bool
	}{
		{"list", DiscussionRequest{Action: "list", ChannelID: 1}, true},
		{"history", DiscussionRequest{Action: "history", ChannelID: 1, ThreadID: 1}, true},
		{"edit", DiscussionRequest{Action: "edit", ChannelID: 1, ThreadID: 1, Title: "Fixed"}, true},
		{"blank edit", DiscussionRequest{Action: "edit", ChannelID: 1, ThreadID: 1}, false},
		{"pin", DiscussionRequest{Action: "pin", ChannelID: 1, ThreadID: 1}, true},
		{"resolve", DiscussionRequest{Action: "resolve", ChannelID: 1, ThreadID: 1}, true},
		{"create", DiscussionRequest{Action: "create", ChannelID: 1, Title: "Maps", RequestID: "retry-1", Tags: []string{"Question"}}, true},
		{"negative channel", DiscussionRequest{Action: "list", ChannelID: -1}, false},
		{"missing thread", DiscussionRequest{Action: "send", ChannelID: 1, RequestID: "x"}, false},
		{"duplicate tags", DiscussionRequest{Action: "list", ChannelID: 1, Tags: []string{"a", "a"}}, false},
		{"blank title", DiscussionRequest{Action: "create", ChannelID: 1, Title: " ", RequestID: "x"}, false},
		{"unknown operation", DiscussionRequest{Action: "destroy", ChannelID: 1}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.r.Validate() == nil; got != tc.valid {
				t.Fatalf("valid=%t, want %t", got, tc.valid)
			}
		})
	}
}
