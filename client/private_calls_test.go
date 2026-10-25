package main

import (
	"crypto/rand"
	"encoding/json"
	"strings"
	"testing"

	"golang.org/x/crypto/nacl/box"
	"noxa/internal/netproto"
)

func TestPrivateCallDescriptionAuthenticatesDTLSAndRouting(t *testing.T) {
	senderPub, senderPriv, err := box.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	recipientPub, recipientPriv, err := box.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := json.Marshal(PrivateCallDescription{CallID: "call", From: "alice", To: "bob", Type: "offer", SDP: "v=0\r\na=fingerprint:sha-256 01:02\r\n"})
	if err != nil {
		t.Fatal(err)
	}
	body, err := sealDM(string(plain), *recipientPub, *senderPriv)
	if err != nil {
		t.Fatal(err)
	}
	signal := netproto.CallSignal{CallID: "call", From: "alice", To: "bob", Body: body}
	description, err := openPrivateCallDescription(signal, "bob", *senderPub, *recipientPriv)
	if err != nil || description.Type != "offer" {
		t.Fatalf("open: %+v %v", description, err)
	}
	for _, change := range []func(*netproto.CallSignal){func(s *netproto.CallSignal) { s.CallID = "other" }, func(s *netproto.CallSignal) { s.From = "mallory" }, func(s *netproto.CallSignal) { s.To = "carol" }, func(s *netproto.CallSignal) { s.Body = body[:len(body)-4] + "AAAA" }} {
		changed := signal
		change(&changed)
		if _, err := openPrivateCallDescription(changed, "bob", *senderPub, *recipientPriv); err == nil {
			t.Fatal("accepted substituted call signaling")
		}
	}
}

func TestPrivateCallCandidatesAreAuthenticatedAndBounded(t *testing.T) {
	senderPub, senderPriv, err := box.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	recipientPub, recipientPriv, err := box.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	valid := `[{"candidate":"candidate:1 1 udp 2122260223 127.0.0.1 54321 typ host","sdpMid":"0","sdpMLineIndex":0,"usernameFragment":"abcd"}]`
	for _, tc := range []struct {
		name, data string
		valid      bool
	}{
		{"host", valid, true},
		{"empty", `[]`, false},
		{"null entry", `[null]`, false},
		{"missing candidate", `[{"sdpMid":"0"}]`, false},
		{"unbounded candidate", `[{"candidate":"` + strings.Repeat("a", 2049) + `","sdpMid":"0"}]`, false},
		{"missing media", `[{"candidate":"candidate:host"}]`, false},
		{"negative media", `[{"candidate":"candidate:host","sdpMLineIndex":-1}]`, false},
		{"too many", `[` + strings.Repeat(valid[1:len(valid)-1]+",", 16) + valid[1:len(valid)-1] + `]`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plain, err := json.Marshal(PrivateCallDescription{CallID: "call", From: "alice", To: "bob", Type: "candidates", SDP: tc.data})
			if err != nil {
				t.Fatal(err)
			}
			sealed, err := sealDM(string(plain), *recipientPub, *senderPriv)
			if err != nil {
				t.Fatal(err)
			}
			signal := netproto.CallSignal{CallID: "call", From: "alice", To: "bob", Body: sealed}
			_, err = openPrivateCallDescription(signal, "bob", *senderPub, *recipientPriv)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v error=%v", tc.valid, err)
			}
			if tc.valid {
				signal.CallID = "other"
				if _, err := openPrivateCallDescription(signal, "bob", *senderPub, *recipientPriv); err == nil {
					t.Fatal("accepted candidates from another call")
				}
			}
		})
	}
}

// (441) A callee ringing on several devices is unbound until one answers;
// afterwards the server projects the account's other sessions as
// "elsewhere". Both must stay readable so the desktop can stop ringing, while
// a participant bound to another session in any other state stays rejected.
func TestCallParticipantVisibleAcrossOwnSessions(t *testing.T) {
	cases := []struct {
		name string
		p    netproto.CallParticipant
		want bool
	}{
		{"own bound", netproto.CallParticipant{ClientID: "mine", State: "accepted"}, true},
		{"own ringing", netproto.CallParticipant{ClientID: "mine", State: "ringing"}, true},
		{"unbound ringing", netproto.CallParticipant{State: "ringing"}, true},
		{"unbound accepted", netproto.CallParticipant{State: "accepted"}, false},
		{"answered elsewhere", netproto.CallParticipant{ClientID: "phone", State: netproto.CallStateElsewhere}, true},
		{"other session accepted", netproto.CallParticipant{ClientID: "phone", State: "accepted"}, false},
		{"other session ringing", netproto.CallParticipant{ClientID: "phone", State: "ringing"}, false},
		{"other session left", netproto.CallParticipant{ClientID: "phone", State: "left"}, false},
	}
	for _, tc := range cases {
		if got := callParticipantVisible(tc.p, "mine"); got != tc.want {
			t.Errorf("%s: visible=%v want %v", tc.name, got, tc.want)
		}
	}
}
