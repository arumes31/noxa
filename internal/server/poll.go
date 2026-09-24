package server

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"noxa/internal/authorization"
	"noxa/internal/netproto"
)

const eventPollChanged = "poll_changed"

type pollStore interface {
	StoreChatPoll(context.Context, int64, string, string, string, uint32, string, netproto.PollDefinition) (int64, bool, error)
	ReadPoll(context.Context, int64, string) (netproto.PollState, error)
	ChangePoll(context.Context, int64, string, []int, bool) (netproto.PollState, error)
}

func (s *TCPServer) handlePoll(ctx context.Context, client *Client, frame *netproto.Frame) error {
	var msg netproto.PollRequest
	if err := netproto.Decode(frame, &msg); err != nil || msg.MessageID <= 0 || len(msg.Choices) > 10 || (msg.Action != "get" && msg.Action != "vote" && msg.Action != "close") || (msg.Action != "vote" && len(msg.Choices) != 0) {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "invalid poll request")
	}
	if s.deps == nil || s.deps.Chat == nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "poll storage unavailable")
	}
	if msg.Action != "get" && s.chatRate != nil && !s.chatRate.allow(client.UniqueID, time.Now()) {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "poll rate limit exceeded — slow down")
	}
	backend, ok := s.deps.Chat.(pollStore)
	if !ok {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "poll storage unavailable")
	}
	stored, err := s.deps.Chat.GetChatMessage(ctx, msg.MessageID)
	if err != nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeNotFound, "poll not found")
	}
	capability := authorization.ReadHistory
	if msg.Action == "vote" || msg.Action == "close" {
		capability = authorization.SendMessages
	}
	if msg.Action == "close" && stored != nil && stored.FromUniqueID != client.UniqueID {
		capability = authorization.ManageMessages
	}
	return s.messageRoleAction(ctx, client, stored, capability, func(ctx context.Context) error {
		var result netproto.PollState
		if msg.Action == "get" {
			result, err = backend.ReadPoll(ctx, msg.MessageID, client.UniqueID)
		} else {
			if client.userID() <= 0 {
				return s.sendErrorFor(client, requestOrigin(ctx), errCodePermissionDenied, "sign in to vote or close polls")
			}
			result, err = backend.ChangePoll(ctx, msg.MessageID, client.UniqueID, msg.Choices, msg.Action == "close")
		}
		if err != nil {
			switch {
			case errors.Is(err, sql.ErrNoRows):
				return s.sendErrorFor(client, requestOrigin(ctx), errCodeNotFound, "poll not found")
			case errors.Is(err, netproto.ErrPollInvalid), errors.Is(err, netproto.ErrPollClosed):
				return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, err.Error())
			default:
				return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "poll request failed")
			}
		}
		if msg.Action != "get" {
			// Never broadcast individual ballots, even to the poll author.
			s.broadcastScope(ctx, stored.ChannelID, eventPollChanged, map[string]any{"message_id": msg.MessageID, "channel_id": stored.ChannelID, "version": result.Version})
		}
		result.Action = msg.Action
		return s.writeCommittedReply(client, netproto.MsgPollState, result)
	})
}
