package netproto

import (
	"encoding/base64"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestConversationEnvelopesRequireCanonicalBoundedCiphertext(t *testing.T) {
	c := Conversation{Epoch: 1, Members: []ConversationMember{{UniqueID: "alice", JoinedEpoch: 1}}}
	body := base64.StdEncoding.EncodeToString(make([]byte, 40))
	message := ConversationSend{Epoch: 1, Reference: "ref", Envelopes: map[string]string{"alice": body}}
	if err := message.Validate(c, "alice"); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{body + "\n", body + "\r\n", body + strings.Repeat("\n", 23000), "plaintext", base64.StdEncoding.EncodeToString(make([]byte, 18001))} {
		message.Envelopes["alice"] = invalid
		if err := message.Validate(c, "alice"); !errors.Is(err, ErrConversationInvalid) {
			t.Fatalf("invalid envelope accepted: %v", err)
		}
	}
}

func TestConversationRequiresInvitationAndChangesEpochOnMembership(t *testing.T) {
	c := Conversation{ID: "g", Name: "Friends", Owner: "alice", Revision: 1, Epoch: 1, Members: []ConversationMember{{UniqueID: "alice", JoinedEpoch: 1}}}
	change := func(actor, action, target string) {
		t.Helper()
		var err error
		c, err = ChangeConversation(c, actor, ConversationRequest{ID: c.ID, Revision: c.Revision, Action: action, Target: target})
		if err != nil {
			t.Fatal(err)
		}
	}
	change("alice", "invite", "bob")
	if c.Epoch != 1 || !c.Members[1].Pending {
		t.Fatal("invitation activated membership")
	}
	change("bob", "accept", "")
	if c.Epoch != 2 || c.Members[1].Pending || c.Members[1].JoinedEpoch != 2 {
		t.Fatal("acceptance did not advance membership epoch")
	}
	change("alice", "remove", "bob")
	if c.Epoch != 3 || len(c.Members) != 1 {
		t.Fatal("removal did not revoke member")
	}
	change("alice", "invite", "bob")
	change("bob", "accept", "")
	if c.Members[1].JoinedEpoch != 4 {
		t.Fatal("rejoining member retained prior history entitlement")
	}
}

func TestConversationRejectsUnauthorizedAndStaleChangesWithoutMutation(t *testing.T) {
	c := Conversation{ID: "g", Name: "Friends", Owner: "alice", Revision: 3, Epoch: 2, Members: []ConversationMember{{UniqueID: "alice", JoinedEpoch: 1}, {UniqueID: "bob", JoinedEpoch: 2}, {UniqueID: "carol", Pending: true}}}
	for _, tc := range []struct {
		actor, action, target string
		revision              int64
		want                  error
	}{
		{"mallory", "accept", "", 3, ErrConversationDenied},
		{"carol", "invite", "mallory", 3, ErrConversationDenied},
		{"bob", "remove", "alice", 3, ErrConversationDenied},
		{"bob", "rename", "", 3, ErrConversationDenied},
		{"alice", "transfer", "carol", 3, ErrConversationDenied},
		{"alice", "leave", "", 3, ErrConversationDenied},
		{"alice", "remove", "bob", 2, ErrConversationConflict},
		{"alice", "invite", "bob", 3, ErrConversationInvalid},
		{"bob", "accept", "", 3, ErrConversationInvalid},
	} {
		t.Run(tc.actor+"/"+tc.action+"/"+tc.target, func(t *testing.T) {
			before := c
			before.Members = append([]ConversationMember(nil), c.Members...)
			_, err := ChangeConversation(c, tc.actor, ConversationRequest{ID: "g", Revision: tc.revision, Action: tc.action, Target: tc.target, Name: "Changed"})
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
			if !reflect.DeepEqual(c, before) {
				t.Fatal("failed change mutated source")
			}
		})
	}
	transferred, err := ChangeConversation(c, "alice", ConversationRequest{ID: "g", Revision: 3, Action: "transfer", Target: "bob"})
	if err != nil || transferred.Owner != "bob" || transferred.Epoch != c.Epoch {
		t.Fatalf("transfer: %+v %v", transferred, err)
	}
	left, err := ChangeConversation(transferred, "alice", ConversationRequest{ID: "g", Revision: 4, Action: "leave"})
	if err != nil || len(left.Members) != 2 || left.Epoch != 3 {
		t.Fatalf("leave: %+v %v", left, err)
	}
	if len(c.Members) != 3 || c.Members[0].UniqueID != "alice" {
		t.Fatal("successful change aliased source")
	}
}
