package main

import (
	"testing"

	"voicx/internal/netproto"
)

func TestChaosOutageRequiresBackendUnavailableError(t *testing.T) {
	for _, tc := range []struct {
		name      string
		kind      netproto.MessageType
		message   any
		wantError bool
	}{
		{"backend_unavailable", netproto.MsgError, netproto.Error{Code: 5, Message: "history query failed"}, false},
		{"successful_history", netproto.MsgChatHistoryResponse, netproto.ChatHistoryResponse{}, true},
		{"permission_denied", netproto.MsgError, netproto.Error{Code: 4, Message: "permission denied"}, true},
		{"malformed_error", netproto.MsgError, "not an error object", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, err := netproto.Encode(tc.kind, tc.message)
			if err != nil {
				t.Fatal(err)
			}
			err = checkChaosOutageReply(f)
			if (err != nil) != tc.wantError {
				t.Fatalf("outage result error = %v, want error %v", err, tc.wantError)
			}
		})
	}
}
