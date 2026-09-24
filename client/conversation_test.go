package main

import (
	"crypto/rand"
	"encoding/json"
	"testing"

	"golang.org/x/crypto/nacl/box"
	"noxa/internal/netproto"
)

func TestConversationEnvelopeBindsGroupEpochReferenceAndIdentities(t *testing.T) {
	senderPub, senderPriv, err := box.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	recipientPub, recipientPriv, err := box.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := json.Marshal(conversationPlaintext{ConversationID: "group-a", Epoch: 3, Reference: "ref", From: "alice", To: "bob", Text: "private hello"})
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := sealDM(string(plain), *recipientPub, *senderPriv)
	if err != nil {
		t.Fatal(err)
	}
	message := netproto.ConversationMessage{ConversationID: "group-a", Epoch: 3, Reference: "ref", FromUniqueID: "alice", Body: sealed}
	text, err := openConversation(message, "bob", *senderPub, *recipientPriv)
	if err != nil || text != "private hello" {
		t.Fatalf("decrypt %q: %v", text, err)
	}
	for _, change := range []func(*netproto.ConversationMessage){
		func(m *netproto.ConversationMessage) { m.ConversationID = "group-b" },
		func(m *netproto.ConversationMessage) { m.Epoch++ },
		func(m *netproto.ConversationMessage) { m.Reference = "other" },
		func(m *netproto.ConversationMessage) { m.FromUniqueID = "mallory" },
	} {
		altered := message
		change(&altered)
		if _, err := openConversation(altered, "bob", *senderPub, *recipientPriv); err == nil {
			t.Fatal("accepted substituted routing metadata")
		}
	}
	if _, err := openConversation(message, "carol", *senderPub, *recipientPriv); err == nil {
		t.Fatal("accepted wrong recipient")
	}
	if _, err := openConversation(message, "bob", *recipientPub, *recipientPriv); err == nil {
		t.Fatal("accepted wrong sender key")
	}
}

func TestConversationOnlyReadsDrainLateResponses(t *testing.T) {
	for _, action := range []string{"list", "get", "history", "create", "invite", "accept", "leave", "send", "remove"} {
		want := action == "list" || action == "get" || action == "history"
		if isConversationRead(netproto.MsgConversationRequest, netproto.ConversationRequest{Action: action}) != want {
			t.Fatal(action)
		}
	}
	if isConversationRead(netproto.MsgChatSend, netproto.ConversationRequest{Action: "get"}) {
		t.Fatal("unrelated command drained")
	}
}
