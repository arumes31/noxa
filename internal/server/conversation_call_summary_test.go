package server

import (
	"noxa/internal/netproto"
	"testing"
)

func TestConversationCallSummaryRespectsCurrentMembership(t *testing.T) {
	s := &TCPServer{privateCalls: map[string]netproto.CallSession{
		"active": {ConversationID: "group", Participants: []netproto.CallParticipant{{UniqueID: "a", State: "accepted"}, {UniqueID: "removed", State: "accepted"}, {UniqueID: "b", State: "ringing"}}},
		"ended":  {ConversationID: "group", EndedAt: 1, Participants: []netproto.CallParticipant{{UniqueID: "a", State: "accepted"}}},
		"direct": {Participants: []netproto.CallParticipant{{UniqueID: "a", State: "accepted"}}},
	}}
	calls := s.conversationCallSnapshot()
	c := netproto.Conversation{ID: "group", Members: []netproto.ConversationMember{{UniqueID: "a"}, {UniqueID: "b"}, {UniqueID: "pending", Pending: true}}}
	applyConversationCalls(&c, "a", calls)
	if c.ActiveCallCount != 1 || c.CallParticipantCount != 1 {
		t.Fatalf("summary includes ended/removed participants: %+v", c)
	}
	c.ActiveCallCount, c.CallParticipantCount = 0, 0
	applyConversationCalls(&c, "pending", calls)
	if c.ActiveCallCount != 0 || c.CallParticipantCount != 0 {
		t.Fatal("pending invitation exposed call activity")
	}
}
