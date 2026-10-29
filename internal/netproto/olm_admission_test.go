package netproto

import (
	"encoding/json"
	"testing"
)

func TestOlmControlSnapshotWireScope(t *testing.T) {
	base := OlmAdmissionRequest{RequestID: "snapshot", AccountEpoch: 1, Action: "snapshot", Scope: "direct", Peer: "bob", TargetDevice: "phone"}
	for _, test := range []struct {
		name  string
		edit  func(*OlmAdmissionRequest)
		valid bool
	}{
		{"targeted direct", func(*OlmAdmissionRequest) {}, true},
		{"full direct", func(r *OlmAdmissionRequest) { r.TargetDevice = "" }, true},
		{"full group", func(r *OlmAdmissionRequest) {
			r.Scope, r.Peer, r.Conversation, r.TargetDevice = "group", "", "group-id", ""
		}, true},
		{"targeted group", func(r *OlmAdmissionRequest) { r.Scope, r.Conversation = "group", "group-id" }, false},
		{"conversation", func(r *OlmAdmissionRequest) { r.Conversation = "group-id" }, false},
		{"missing peer", func(r *OlmAdmissionRequest) { r.Peer = "" }, false},
		{"invalid target", func(r *OlmAdmissionRequest) { r.TargetDevice = "phone\n" }, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := base
			test.edit(&r)
			payload, err := json.Marshal(r)
			if err != nil {
				t.Fatal(err)
			}
			_, err = DecodeOlmAdmissionRequest(&Frame{Payload: payload})
			if (err == nil) != test.valid {
				t.Fatalf("valid=%v: %v", test.valid, err)
			}
		})
	}
}
