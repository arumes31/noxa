package main

import (
	"noxa/internal/netproto"
	"testing"
)

func TestOnlyPollReadsCanDrainAfterTimeout(t *testing.T) {
	for _, action := range []string{"get", "vote", "close", ""} {
		body := netproto.PollRequest{Action: action}
		if got := isPollRead(netproto.MsgPollRequest, body); got != (action == "get") {
			t.Fatalf("action %q drain=%t", action, got)
		}
		if isPollRead(netproto.MsgChatSend, body) {
			t.Fatal("unrelated request could drain")
		}
	}
	if isPollRead(netproto.MsgPollRequest, map[string]string{"action": "get"}) {
		t.Fatal("untyped request could drain")
	}
}
