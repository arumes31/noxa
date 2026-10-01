package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"golang.org/x/crypto/nacl/box"
	"noxa/internal/netproto"
)

func TestConversationSendResolvesColdGroupThroughKeyRateLimit(t *testing.T) {
	group := netproto.Conversation{ID: "group", Name: "Group", Owner: "sender", Revision: 1, Epoch: 1,
		Members: []netproto.ConversationMember{{UniqueID: "sender", JoinedEpoch: 1}}}
	for i := 1; i < netproto.MaxConversationMembers; i++ {
		group.Members = append(group.Members, netproto.ConversationMember{UniqueID: fmt.Sprintf("peer-%d", i), JoinedEpoch: 1})
	}
	peer := mustTempIdentity(t)
	pub, _, err := peer.x25519()
	if err != nil {
		t.Fatal(err)
	}
	// Exercise the default five-key allowance with a shorter window. The
	// server advertises its remaining delay; the client must honor that delay.
	const window = 100 * time.Millisecond
	var reset time.Time
	used, limited, sent := 0, 0, 0
	app, cm := newPipedApp(t, func(frame *netproto.Frame) (netproto.MessageType, any, bool) {
		if netproto.MessageType(frame.Type) == netproto.MsgKeyRequest {
			var request netproto.KeyRequest
			_ = netproto.Decode(frame, &request)
			if !time.Now().Before(reset) {
				used, reset = 0, time.Now().Add(window)
			}
			if used == 5 {
				limited++
				return netproto.MsgError, map[string]any{"code": 2, "message": "key lookup rate limit exceeded — slow down", "origin_type": uint16(netproto.MsgKeyRequest), "retry_after_ms": time.Until(reset).Milliseconds() + 1}, true
			}
			used++
			return netproto.MsgKeyResponse, netproto.KeyResponse{UniqueID: request.UniqueID, PublicKey: base64.StdEncoding.EncodeToString(pub[:])}, true
		}
		var request netproto.ConversationRequest
		_ = netproto.Decode(frame, &request)
		result := netproto.ConversationResult{Action: request.Action}
		switch request.Action {
		case "get":
			result.Conversations = []netproto.Conversation{group}
		case "send":
			sent++
			if request.Message == nil || request.Message.Validate(group, "sender") != nil || len(request.Message.Envelopes) != len(group.Members) {
				t.Error("message did not include valid envelopes for every member")
			}
			result.MessageID = 42
		}
		return netproto.MsgConversationResult, result, true
	})
	cm.mu.Lock()
	cm.uniqueID = "sender"
	cm.mu.Unlock()
	app.tabs = map[string]*tabState{"a": {cm: cm}}
	app.activeID = "a"
	messageID, err := app.SendConversationForTab("a", group.ID, "hello group", "reference")
	if err != nil || messageID != 42 {
		t.Fatalf("send = %d, %v", messageID, err)
	}
	if limited != 2 || sent != 1 {
		t.Fatalf("rate limits = %d, sends = %d; want 2, 1", limited, sent)
	}
}

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
