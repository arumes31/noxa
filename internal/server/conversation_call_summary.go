package server

import "noxa/internal/netproto"

// Snapshot before taking a database membership lease: call start takes the
// call mutex before its database lock, so the reverse order would deadlock.
func (s *TCPServer) conversationCallSnapshot() []netproto.CallSession {
	s.privateCallsMu.Lock()
	defer s.privateCallsMu.Unlock()
	calls := make([]netproto.CallSession, 0, len(s.privateCalls))
	for _, call := range s.privateCalls {
		if call.ConversationID == "" || call.EndedAt != 0 {
			continue
		}
		call.Participants = append([]netproto.CallParticipant(nil), call.Participants...)
		calls = append(calls, call)
	}
	return calls
}

func applyConversationCalls(c *netproto.Conversation, uid string, calls []netproto.CallSession) {
	viewer, found := c.Member(uid)
	if !found || viewer.Pending {
		return
	}
	for _, call := range calls {
		if call.ConversationID != c.ID {
			continue
		}
		count := 0
		for _, participant := range call.Participants {
			member, found := c.Member(participant.UniqueID)
			if found && !member.Pending && participant.State == "accepted" {
				count++
			}
		}
		if count > 0 {
			c.ActiveCallCount++
			c.CallParticipantCount += count
		}
	}
}
