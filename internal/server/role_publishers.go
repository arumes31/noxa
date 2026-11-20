package server

import (
	"sync"

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
	// Concurrent policy, membership and offer refreshes must be serialized:
	// an older snapshot that does not know the newest member could be
	// installed last and prune that member's pairs (one listener then never
	// heard one speaker).
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
	allowed := memoizedEvaluate(e)
	s.deps.Voice.SetPublisherGuard(func(pair webrtc.PublisherAccess) bool {
		sender, senderOK := identities[pair.PublisherID]
		receiver, receiverOK := identities[pair.SubscriberID]
		if !senderOK || !receiverOK || sender.blocked || receiver.blocked || pair.ChannelID <= 0 || pair.SubscriberChannelID <= 0 {
			return false
		}
		if !allowed(sender.userID, pair.ChannelID, authorization.Connect) ||
			!allowed(receiver.userID, pair.SubscriberChannelID, authorization.Connect) ||
			!allowed(receiver.userID, pair.ChannelID, authorization.ViewChannel) {
			return false
		}
		if sender.invisible && sender.uniqueID != receiver.uniqueID && !allowed(receiver.userID, 0, authorization.ViewConnectionInfo) {
			return false
		}
		return pair.ChannelID == pair.SubscriberChannelID ||
			(allowed(sender.userID, pair.ChannelID, authorization.Whisper) && allowed(sender.userID, pair.SubscriberChannelID, authorization.Whisper))
	})
}

type evaluationKey struct {
	userID, channelID int64
	capability        authorization.Capability
}

// memoizedEvaluate caches decisions of one immutable evaluator. The router
// checks every subscriber/publisher pair under its exclusive lock on each
// refresh; the distinct (user, channel, capability) questions grow with the
// members, not with the pairs. Safe for concurrent callers.
func memoizedEvaluate(e *authorization.RoleEvaluator) func(int64, int64, authorization.Capability) bool {
	var mu sync.RWMutex
	cache := make(map[evaluationKey]bool)
	return func(userID, channelID int64, capability authorization.Capability) bool {
		key := evaluationKey{userID, channelID, capability}
		mu.RLock()
		result, ok := cache[key]
		mu.RUnlock()
		if ok {
			return result
		}
		result = e.Evaluate(userID, channelID, capability).Allowed
		mu.Lock()
		cache[key] = result
		mu.Unlock()
		return result
	}
}
