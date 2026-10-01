//go:build integration

package store

import (
	"errors"
	"testing"

	"noxa/internal/netproto"
)

func TestConversationReadPositionPersistsAndRespectsMembership(t *testing.T) {
	s, c := conversationTestStore(t)
	c = conversationTestChange(t, s, c, "owner", "invite", "member")
	c = conversationTestChange(t, s, c, "member", "accept", "")
	send := func(actor string, sequence int) int64 {
		t.Helper()
		id, _, err := s.SendConversation(t.Context(), actor, conversationTestSend(c, sequence))
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	read := func(uid string) netproto.Conversation {
		t.Helper()
		return conversationTestRead(t, s, uid, netproto.ConversationRequest{Action: "get", ID: c.ID}).Conversations[0]
	}
	first := send("owner", 1)
	second := send("owner", 2)
	send("member", 3)
	if got := read("member"); got.UnreadCount != 2 || got.ReadMessageID != 0 {
		t.Fatalf("initial: %+v", got)
	}
	if err := s.MarkConversationRead(t.Context(), "member", c.ID, first); err != nil {
		t.Fatal(err)
	}
	if got := read("member"); got.UnreadCount != 1 || got.ReadMessageID != first {
		t.Fatalf("partial read: %+v", got)
	}
	other := testAdditionalStore(t, s)
	if got := conversationTestRead(t, other, "member", netproto.ConversationRequest{Action: "get", ID: c.ID}).Conversations[0]; got.ReadMessageID != first || got.UnreadCount != 1 {
		t.Fatalf("independent connection lost persisted state: %+v", got)
	}
	foreign, err := s.CreateConversation(t.Context(), "owner", "Other group")
	if err != nil {
		t.Fatal(err)
	}
	foreignID, _, err := s.SendConversation(t.Context(), "owner", conversationTestSend(foreign, 20))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.MarkConversationRead(t.Context(), "member", c.ID, foreignID); !errors.Is(err, netproto.ErrConversationInvalid) {
		t.Fatalf("foreign message accepted: %v", err)
	}
	c = conversationTestChange(t, s, c, "owner", "invite", "third")
	if got := read("member"); got.ReadMessageID != first {
		t.Fatal("membership edit reset read pointer")
	}
	if err := s.MarkConversationRead(t.Context(), "member", c.ID, second); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkConversationRead(t.Context(), "member", c.ID, first); err != nil {
		t.Fatal(err)
	}
	if got := read("member"); got.UnreadCount != 0 || got.ReadMessageID != second {
		t.Fatalf("regressed pointer: %+v", got)
	}
	results := make(chan error, 2)
	go func() { results <- s.MarkConversationRead(t.Context(), "member", c.ID, first) }()
	go func() { results <- other.MarkConversationRead(t.Context(), "member", c.ID, second) }()
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if got := read("member"); got.ReadMessageID != second {
		t.Fatal("concurrent update regressed read cursor")
	}
	for _, uid := range []string{"third", "outsider"} {
		if err := s.MarkConversationRead(t.Context(), uid, c.ID, second); !errors.Is(err, netproto.ErrConversationDenied) {
			t.Fatalf("%s marked read: %v", uid, err)
		}
	}
	if err := s.MarkConversationRead(t.Context(), "member", c.ID, second+1000); !errors.Is(err, netproto.ErrConversationInvalid) {
		t.Fatalf("future message: %v", err)
	}
	c = conversationTestChange(t, s, c, "owner", "remove", "member")
	if err := s.MarkConversationRead(t.Context(), "member", c.ID, second); !errors.Is(err, netproto.ErrConversationDenied) {
		t.Fatalf("removed member: %v", err)
	}
	c = conversationTestChange(t, s, c, "owner", "invite", "member")
	c = conversationTestChange(t, s, c, "member", "accept", "")
	if got := read("member"); got.UnreadCount != 0 || got.ReadMessageID != 0 {
		t.Fatalf("rejoin exposed old state: %+v", got)
	}
	if err := s.MarkConversationRead(t.Context(), "member", c.ID, second); !errors.Is(err, netproto.ErrConversationInvalid) {
		t.Fatalf("pre-join marker accepted: %v", err)
	}
	send("owner", 4)
	if got := read("member"); got.UnreadCount != 1 {
		t.Fatalf("new unread after rejoin: %+v", got)
	}
}
