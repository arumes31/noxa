package server

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"
	"noxa/internal/netproto"
)

const eventConversationChanged = "conversation_changed"

type conversationStore interface {
	CreateConversation(context.Context, string, string) (netproto.Conversation, error)
	ChangeConversation(context.Context, string, netproto.ConversationRequest) ([]string, error)
	SendConversation(context.Context, string, netproto.ConversationRequest) (int64, []string, error)
	MarkConversationRead(context.Context, string, string, int64) error
	WithConversations(context.Context, string, netproto.ConversationRequest, func(netproto.ConversationResult) error) error
}

func (s *TCPServer) handleConversation(ctx context.Context, client *Client, frame *netproto.Frame) error {
	var request netproto.ConversationRequest
	if err := netproto.Decode(frame, &request); err != nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "invalid conversation request")
	}
	read := request.Action == "list" || request.Action == "get" || request.Action == "history"
	switch request.Action {
	case "list", "create", "get", "history", "invite", "accept", "decline", "leave", "remove", "transfer", "rename", "send", "mark_read":
	default:
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "invalid conversation action")
	}
	if request.Action != "list" && request.Action != "create" {
		id, err := uuid.Parse(request.ID)
		if err != nil || id.String() != request.ID {
			return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "invalid conversation id")
		}
	}
	if request.BeforeID < 0 || request.ReadMessageID < 0 || (request.Action == "mark_read" && request.ReadMessageID == 0) || (request.Action == "send") != (request.Message != nil) {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "invalid conversation request")
	}
	return s.rolePolicyRead(ctx, client, func(ctx context.Context) error {
		if !client.isAuthed() || client.userID() <= 0 || client.rulesBlocked() {
			return s.sendErrorFor(client, requestOrigin(ctx), errCodePermissionDenied, "sign in and accept the rules to use private groups")
		}
		backend, ok := s.deps.Chat.(conversationStore)
		if !ok {
			return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "private groups unavailable")
		}
		if request.Action == "mark_read" && s.conversationReadRate != nil && !s.conversationReadRate.allow(client.UniqueID, time.Now()) {
			return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "group read rate limit exceeded")
		}
		if !read && request.Action != "mark_read" && s.chatRate != nil && !s.chatRate.allowLimit(client.UniqueID, time.Now(), s.chatActionLimit(ctx, client)) {
			return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "group rate limit exceeded")
		}
		calls := s.conversationCallSnapshot()
		deliveryStarted := false
		deliver := func(result netproto.ConversationResult) error {
			for i := range result.Conversations {
				applyConversationCalls(&result.Conversations[i], client.UniqueID, calls)
			}
			deliveryStarted = true
			return s.writeCommittedReply(client, netproto.MsgConversationResult, result)
		}
		if read {
			err := backend.WithConversations(ctx, client.UniqueID, request, deliver)
			if deliveryStarted {
				return err
			}
			return s.conversationError(ctx, client, err)
		}
		result := netproto.ConversationResult{Action: request.Action, Conversations: []netproto.Conversation{}, Messages: []netproto.ConversationMessage{}}
		var recipients []string
		var err error
		switch request.Action {
		case "mark_read":
			err = backend.MarkConversationRead(ctx, client.UniqueID, request.ID, request.ReadMessageID)
		case "create":
			var c netproto.Conversation
			c, err = backend.CreateConversation(ctx, client.UniqueID, request.Name)
			if err == nil {
				result.Conversations = append(result.Conversations, c)
			}
		case "send":
			result.MessageID, recipients, err = backend.SendConversation(ctx, client.UniqueID, request)
		default:
			recipients, err = backend.ChangeConversation(ctx, client.UniqueID, request)
		}
		if err != nil {
			return s.conversationError(ctx, client, err)
		}
		if request.Action == "leave" {
			s.removeConversationCaller(request.ID, client.UniqueID)
		}
		if request.Action == "remove" {
			s.removeConversationCaller(request.ID, request.Target)
		}
		// Queue only an opaque invalidation. Clients fetch content through a fresh
		// membership lease; queued events never contain messages or new members.
		payload, err := eventEnvelope(eventConversationChanged, map[string]any{"id": request.ID, "message_id": result.MessageID})
		if err != nil {
			return err
		}
		if s.deps.Broadcast != nil {
			for _, uid := range recipients {
				if request.Action == "send" && uid == client.UniqueID {
					continue
				}
				if target, found := s.clientByUniqueID(uid); found {
					addressed := payload
					if request.Action == "invite" && uid == request.Target {
						addressed, err = eventEnvelope(eventConversationChanged, map[string]any{"id": request.ID, "invitation": true})
						if err != nil {
							return err
						}
					}
					_ = s.deps.Broadcast.BroadcastToClient(target.ID, addressed)
				}
			}
		}
		return deliver(result)
	})
}

func (s *TCPServer) conversationError(ctx context.Context, client *Client, err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, netproto.ErrConversationDenied), errors.Is(err, sql.ErrNoRows):
		return s.sendErrorFor(client, requestOrigin(ctx), errCodePermissionDenied, "private group unavailable or access denied")
	case errors.Is(err, netproto.ErrConversationConflict), errors.Is(err, netproto.ErrConversationInvalid):
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, err.Error())
	default:
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "private group request failed")
	}
}
