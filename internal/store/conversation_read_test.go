//go:build integration

package store

import (
	"errors"
	"slices"
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

func TestConversationSharedReadReceipts(t *testing.T) {
	s, c := conversationTestStore(t)
	c = conversationTestChange(t, s, c, "owner", "invite", "member")
	c = conversationTestChange(t, s, c, "member", "accept", "")
	first, _, err := s.SendConversation(t.Context(), "owner", conversationTestSend(c, 1))
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := s.SendConversation(t.Context(), "owner", conversationTestSend(c, 2))
	if err != nil {
		t.Fatal(err)
	}
	seen := func(viewer, member string) int64 {
		t.Helper()
		got := conversationTestRead(t, s, viewer, netproto.ConversationRequest{Action: "get", ID: c.ID}).Conversations[0]
		m, _ := got.Member(member)
		return m.SeenMessageID
	}
	// A private read never becomes a receipt.
	if err := s.MarkConversationRead(t.Context(), "member", c.ID, first); err != nil {
		t.Fatal(err)
	}
	if got := seen("owner", "member"); got != 0 {
		t.Fatalf("private read shared: %d", got)
	}
	audience, position, advanced, err := s.ShareConversationRead(t.Context(), "member", c.ID, second)
	if err != nil || position != second || !advanced || !slices.Equal(audience, []string{"owner"}) {
		t.Fatalf("share: audience=%v position=%d advanced=%v err=%v", audience, position, advanced, err)
	}
	if got := seen("owner", "member"); got != second {
		t.Fatalf("owner sees member at %d, want %d", got, second)
	}
	if got := conversationTestRead(t, s, "member", netproto.ConversationRequest{Action: "get", ID: c.ID}).Conversations[0]; got.ReadMessageID != second || got.UnreadCount != 0 {
		t.Fatalf("shared read did not advance the private cursor: %+v", got)
	}
	// Receipts never move backwards, and a repeat or older mark_read reports
	// no advance, so the server has nothing to relay.
	if _, position, advanced, err := s.ShareConversationRead(t.Context(), "member", c.ID, first); err != nil || position != second || advanced {
		t.Fatalf("receipt regressed to %d advanced=%v (%v)", position, advanced, err)
	}
	if _, position, advanced, err := s.ShareConversationRead(t.Context(), "member", c.ID, second); err != nil || position != second || advanced {
		t.Fatalf("repeated receipt: position=%d advanced=%v (%v)", position, advanced, err)
	}
	// A pending invitee neither shares nor sees receipts.
	c = conversationTestChange(t, s, c, "owner", "invite", "third")
	if got := seen("third", "member"); got != 0 {
		t.Fatalf("invitee saw a receipt: %d", got)
	}
	if _, _, _, err := s.ShareConversationRead(t.Context(), "third", c.ID, second); !errors.Is(err, netproto.ErrConversationDenied) {
		t.Fatalf("invitee shared a receipt: %v", err)
	}
	// Removal drops the member's receipt with the membership row.
	c = conversationTestChange(t, s, c, "owner", "remove", "member")
	if _, _, _, err := s.ShareConversationRead(t.Context(), "member", c.ID, second); !errors.Is(err, netproto.ErrConversationDenied) {
		t.Fatalf("removed member shared a receipt: %v", err)
	}
	c = conversationTestChange(t, s, c, "owner", "invite", "member")
	conversationTestChange(t, s, c, "member", "accept", "")
	if got := seen("owner", "member"); got != 0 {
		t.Fatalf("rejoined member kept an old receipt: %d", got)
	}
}
