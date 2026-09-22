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
	identities := make(map[string]roleMediaIdentity)
	for _, member := range s.deps.State.ListClients() {
		client, ok := s.clientByID(member.ClientID)
		if !ok || !client.isAuthed() {
			continue
		}
		identities[client.ID] = roleMediaIdentity{client.userID(), client.uniqueID(), member.Status == "invisible", client.rulesBlocked()}
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
