package server

import (
	"noxa/internal/authorization"
	"noxa/internal/webrtc"
)

type roleMediaIdentity struct {
	userID    int64
	uniqueID  string
	invisible bool
	blocked   bool
}

// refreshRolePublishers is called with policy and metadata changes excluded.
// The router receives copied identities and an immutable evaluator; invoking
// this predicate under its lock cannot re-enter Authority or server state.
func (s *TCPServer) refreshRolePublishers(e *authorization.RoleEvaluator) {
	if s.deps.Voice == nil || s.deps.State == nil {
		return
	}
	// Every renegotiation offer refreshes the guard, so a join triggers N-1
	// concurrent refreshes. Without serialization an older snapshot that does
	// not know the newest member could be installed last and prune that
	// member's pairs (one listener then never heard one speaker).
	s.publisherGuardMu.Lock()
	defer s.publisherGuardMu.Unlock()
	identities := make(map[string]roleMediaIdentity)
	for _, member := range s.deps.State.ListClients() {
		client, ok := s.clientByID(member.ClientID)
		if !ok || !client.isAuthed() {
			continue
		}
		identities[client.ID] = roleMediaIdentity{client.userID(), client.uniqueID(), s.deps.State.PresenceHidden(member), client.rulesBlocked()}
	}
	s.deps.Voice.SetPublisherGuard(func(pair webrtc.PublisherAccess) bool {
		sender, senderOK := identities[pair.PublisherID]
		receiver, receiverOK := identities[pair.SubscriberID]
		if !senderOK || !receiverOK || sender.blocked || receiver.blocked || pair.ChannelID <= 0 || pair.SubscriberChannelID <= 0 {
			return false
		}
		if !e.Evaluate(sender.userID, pair.ChannelID, authorization.Connect).Allowed ||
			!e.Evaluate(receiver.userID, pair.SubscriberChannelID, authorization.Connect).Allowed ||
			!e.Evaluate(receiver.userID, pair.ChannelID, authorization.ViewChannel).Allowed {
			return false
		}
		if sender.invisible && sender.uniqueID != receiver.uniqueID && !e.Evaluate(receiver.userID, 0, authorization.ViewConnectionInfo).Allowed {
			return false
		}
		return pair.ChannelID == pair.SubscriberChannelID ||
			(e.Evaluate(sender.userID, pair.ChannelID, authorization.Whisper).Allowed && e.Evaluate(sender.userID, pair.SubscriberChannelID, authorization.Whisper).Allowed)
	})
}
