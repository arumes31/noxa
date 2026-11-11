package main

import (
	"errors"
	"testing"

	"noxa/internal/netproto"
)

func callMediaTestCall() netproto.CallSession {
	return netproto.CallSession{ID: "8f0a2c1e-4b8d-4d6e-9a51-3c2b1a0f9e77", Participants: []netproto.CallParticipant{
		{UniqueID: "alice", ClientID: "c-alice", State: "accepted", DeviceID: "dev-alice"},
		{UniqueID: "bob", ClientID: "c-bob", State: "accepted", DeviceID: "dev-bob"},
		{UniqueID: "carol", ClientID: "", State: "ringing"},
		{UniqueID: "dave", ClientID: "c-dave", State: "left", DeviceID: "dev-dave"},
	}}
}

func TestCallMediaTargetAllowed(t *testing.T) {
	call := callMediaTestCall()
	if err := callMediaTargetAllowed(call, "alice", "c-alice", "dev-alice", "bob", "dev-bob"); err != nil {
		t.Fatalf("accepted peer device refused: %v", err)
	}
	cases := []struct {
		name                     string
		mutate                   func(*netproto.CallSession)
		uid, client, dev, to, td string
		want                     error
	}{
		{"other device of the peer", nil, "alice", "c-alice", "dev-alice", "bob", "dev-bob-2", errCallMediaTarget},
		{"ringing participant", nil, "alice", "c-alice", "dev-alice", "carol", "dev-carol", errCallMediaTarget},
		{"departed participant", nil, "alice", "c-alice", "dev-alice", "dave", "dev-dave", errCallMediaTarget},
		{"not a participant", nil, "alice", "c-alice", "dev-alice", "mallory", "dev-m", errCallMediaTarget},
		{"self", nil, "alice", "c-alice", "dev-alice", "alice", "dev-alice-2", errCallMediaTarget},
		{"another session of my account", nil, "alice", "c-alice-2", "dev-alice", "bob", "dev-bob", errCallMediaScope},
		{"I have not accepted", func(c *netproto.CallSession) { c.Participants[0].State = "ringing" }, "alice", "c-alice", "dev-alice", "bob", "dev-bob", errCallMediaScope},
		{"ended call", func(c *netproto.CallSession) { c.EndedAt = 1 }, "alice", "c-alice", "dev-alice", "bob", "dev-bob", errCallMediaScope},
		{"I answered on another device", nil, "alice", "c-alice", "dev-alice-2", "bob", "dev-bob", errCallMediaScope},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := callMediaTestCall()
			if tc.mutate != nil {
				tc.mutate(&c)
			}
			if err := callMediaTargetAllowed(c, tc.uid, tc.client, tc.dev, tc.to, tc.td); !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
}

func TestCheckCallMediaKeyTargetRejectsNonCanonicalScope(t *testing.T) {
	m := &connManager{uniqueID: "alice", clientID: "c-alice", deviceID: "dev-alice"}
	for _, id := range []string{"", "call-1", "8F0A2C1E-4B8D-4D6E-9A51-3C2B1A0F9E77", "{8f0a2c1e-4b8d-4d6e-9a51-3c2b1a0f9e77}"} {
		req := MediaKeyRequest{Kind: mediaKeyControlKind, Principal: "bob", Device: "dev-bob"}
		req.Scope.Type, req.Scope.ID = callMediaScopeType, id
		if err := m.checkMediaKeyTarget(req); !errors.Is(err, errCallMediaScope) {
			t.Fatalf("scope %q: got %v", id, err)
		}
	}
}
