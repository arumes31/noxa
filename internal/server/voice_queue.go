package server

import (
	"context"

	"noxa/internal/authorization"
	"noxa/internal/netproto"
	"noxa/internal/state"
)

const eventVoiceQueueChanged = "voice_queue_changed"

func (s *TCPServer) handleVoiceQueue(ctx context.Context, client *Client, frame *netproto.Frame) error {
	var request netproto.VoiceQueueRequest
	if err := netproto.Decode(frame, &request); err != nil || request.ChannelID <= 0 ||
		(request.Action != "raise" && request.Action != "lower" && request.Action != "next" && request.Action != "clear") {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "invalid voice queue action")
	}
	if s.deps == nil || s.deps.State == nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "state unavailable")
	}
	return s.rolePolicyRead(ctx, client, func(ctx context.Context) error {
		e := ctx.Value(roleLeaseKey{}).(roleLease).evaluator
		s.roleMetadataMu.Lock()
		defer s.roleMetadataMu.Unlock()
		self, exists := s.deps.State.GetClient(client.ID)
		if !exists || self.ChannelID != request.ChannelID || !e.Evaluate(client.userID(), request.ChannelID, authorization.Connect).Allowed {
			return s.sendErrorFor(client, requestOrigin(ctx), errCodePermissionDenied, "join this voice channel first")
		}
		target := request.ClientID
		if target == "" {
			target = client.ID
		}
		moderating := request.Action == "next" || request.Action == "clear" || target != client.ID
		if (request.Action == "raise" && target != client.ID) || (moderating && !e.Evaluate(client.userID(), request.ChannelID, authorization.MuteMembers).Allowed) {
			return s.sendErrorFor(client, requestOrigin(ctx), errCodePermissionDenied, "queue moderation requires Mute members")
		}
		if target != client.ID && request.Action == "lower" {
			member, ok := s.deps.State.GetClient(target)
			if !ok || member.ChannelID != request.ChannelID || (member.Status == "invisible" && member.UniqueID != client.uniqueID() && !e.Evaluate(client.userID(), 0, authorization.ViewConnectionInfo).Allowed) {
				return s.sendErrorFor(client, requestOrigin(ctx), errCodeNotFound, "member not found")
			}
		}
		// The metadata barrier serializes queue and membership changes. Check
		// every member whose state will change before applying any effect.
		members := s.deps.State.ListClients()
		var firstID string
		var firstOrder uint64
		if request.Action == "next" {
			for _, member := range members {
				if member.ChannelID == request.ChannelID && member.HandRaisedOrder > 0 && (firstOrder == 0 || member.HandRaisedOrder < firstOrder) {
					firstID, firstOrder = member.ClientID, member.HandRaisedOrder
				}
			}
		}
		for _, member := range members {
			if member.ChannelID != request.ChannelID || member.ClientID == client.ID {
				continue
			}
			affected := request.Action == "lower" && member.ClientID == target
			affected = affected || request.Action == "clear" && (member.HandRaisedOrder > 0 || member.HandCalled)
			affected = affected || request.Action == "next" && firstID != "" && (member.ClientID == firstID || member.HandCalled)
			if affected && (!e.CanManageMember(client.userID(), member.UserID) || (member.Status == "invisible" && member.UniqueID != client.uniqueID() && !e.Evaluate(client.userID(), 0, authorization.ViewConnectionInfo).Allowed)) {
				return s.sendErrorFor(client, requestOrigin(ctx), errCodePermissionDenied, "queue moderation denied")
			}
		}
		chosen, err := s.deps.State.UpdateVoiceQueue(request.ChannelID, target, request.Action)
		if err != nil {
			return s.sendErrorFor(client, requestOrigin(ctx), errCodeNotFound, "voice queue member unavailable")
		}
		// A repeated raise or lower (or an empty clear) changes nothing; it
		// must not cost every connected client a fresh snapshot.
		if !sameVoiceQueue(members, s.deps.State.ListClients(), request.ChannelID) {
			s.broadcastEvent(eventVoiceQueueChanged, struct{}{})
		}
		if moderating {
			s.auditInChannels(ctx, client.uniqueID(), "voice_queue_moderated", target, "action="+request.Action, request.ChannelID)
		}
		// A moderation acknowledgement must not disclose an invisible next speaker.
		if request.Action == "next" || request.Action == "clear" {
			chosen = ""
		}
		return s.writeCommittedReply(client, netproto.MsgVoiceQueueResult, netproto.VoiceQueueResult{ChannelID: request.ChannelID, Action: request.Action, ClientID: chosen})
	})
}

// voiceQueueEntry is the queue-visible part of one channel member.
type voiceQueueEntry struct {
	order  uint64
	called bool
}

// sameVoiceQueue reports whether the queue state of channelID's members is
// identical in two member lists.
func sameVoiceQueue(before, after []*state.Client, channelID int64) bool {
	queue := func(members []*state.Client) map[string]voiceQueueEntry {
		out := make(map[string]voiceQueueEntry)
		for _, member := range members {
			if member.ChannelID == channelID {
				out[member.ClientID] = voiceQueueEntry{member.HandRaisedOrder, member.HandCalled}
			}
		}
		return out
	}
	a, b := queue(before), queue(after)
	if len(a) != len(b) {
		return false
	}
	for id, entry := range a {
		if other, ok := b[id]; !ok || other != entry {
			return false
		}
	}
	return true
}
