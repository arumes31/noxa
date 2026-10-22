package server

import (
	"context"
	"noxa/internal/authorization"
	"noxa/internal/netproto"
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
			if affected && !e.CanManageMember(client.userID(), member.UserID) {
				return s.sendErrorFor(client, requestOrigin(ctx), errCodePermissionDenied, "queue moderation denied")
			}
		}
		chosen, err := s.deps.State.UpdateVoiceQueue(request.ChannelID, target, request.Action)
		if err != nil {
			return s.sendErrorFor(client, requestOrigin(ctx), errCodeNotFound, "voice queue member unavailable")
		}
		s.broadcastEvent(eventVoiceQueueChanged, struct{}{})
		// A moderation acknowledgement must not disclose an invisible next speaker.
		if request.Action == "next" || request.Action == "clear" {
			chosen = ""
		}
		return s.writeCommittedReply(client, netproto.MsgVoiceQueueResult, netproto.VoiceQueueResult{ChannelID: request.ChannelID, Action: request.Action, ClientID: chosen})
	})
}
