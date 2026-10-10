package server

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"noxa/internal/authorization"
	"noxa/internal/netproto"
)

type discussionStore interface {
	Discussion(context.Context, netproto.DiscussionRequest, string, string) (netproto.DiscussionResult, error)
	DiscussionFollowers(context.Context, int64, []string) (map[string]bool, error)
}

func (s *TCPServer) handleDiscussion(ctx context.Context, client *Client, frame *netproto.Frame) error {
	var r netproto.DiscussionRequest
	if err := netproto.Decode(frame, &r); err != nil || r.Validate() != nil || r.Text != "" {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "invalid discussion request")
	}
	if s.deps == nil || s.deps.Chat == nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "discussion storage unavailable")
	}
	backend, ok := s.deps.Chat.(discussionStore)
	if !ok {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "discussion storage unavailable")
	}
	return s.roleAction(ctx, client, r.ChannelID, authorization.ReadHistory, func(ctx context.Context) error {
		if !s.scopeReadable(ctx, client, r.ChannelID) {
			return s.sendErrorFor(client, requestOrigin(ctx), errCodePermissionDenied, "channel unavailable")
		}
		// Every thread response includes its encrypted message page. Check the
		// recipient before a membership/lifecycle mutation can commit.
		if r.Action != "list" && r.Action != "state" && r.Action != "configure" && s.publishedKey(client) == "" {
			return s.sendErrorFor(client, requestOrigin(ctx), errCodePermissionDenied, "publish an encryption key before opening a thread")
		}
		mutation := r.Action != "list" && r.Action != "get" && r.Action != "history" && r.Action != "state"
		if mutation {
			if client.userID() <= 0 {
				return s.sendErrorFor(client, requestOrigin(ctx), errCodePermissionDenied, "sign in to participate in discussions")
			}
			if s.chatRate != nil && !s.chatRate.allow(client.UniqueID, time.Now()) {
				return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "discussion rate limit exceeded — slow down")
			}
		}
		capability := authorization.ReadHistory
		switch r.Action {
		case "create", "send":
			capability = authorization.SendMessages
		case "configure":
			capability = authorization.ManageChannels
		case "pin", "delete", "delete_message":
			capability = authorization.ManageMessages
		case "archive", "reopen", "edit", "resolve":
			capability = authorization.ManageMessages
			if !s.roleAllowed(ctx, client, r.ChannelID, capability) {
				current, err := backend.Discussion(ctx, netproto.DiscussionRequest{Action: "state", ChannelID: r.ChannelID, ThreadID: r.ThreadID}, client.UniqueID, client.nickname())
				if err != nil || len(current.Threads) != 1 || current.Threads[0].Author != client.UniqueID {
					return s.sendErrorFor(client, requestOrigin(ctx), errCodePermissionDenied, "only the author or a moderator can archive this thread")
				}
				capability = authorization.SendMessages
			}
		}
		return s.roleAction(ctx, client, r.ChannelID, capability, func(ctx context.Context) error {
			if r.Action == "create" || r.Action == "send" {
				if err := s.validateDiscussionBody(ctx, client, r); err != nil {
					return err
				}
			}
			if r.Action == "configure" || r.Action == "edit" {
				if err := s.moderateBody(ctx, r.Title+"\n"+strings.Join(r.Tags, "\n")); err != nil {
					return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, err.Error())
				}
			}
			result, err := backend.Discussion(ctx, r, client.UniqueID, client.nickname())
			if err != nil {
				switch {
				case errors.Is(err, sql.ErrNoRows):
					return s.sendErrorFor(client, requestOrigin(ctx), errCodeNotFound, "thread not found")
				case errors.Is(err, netproto.ErrDiscussionInvalid), errors.Is(err, netproto.ErrDiscussionArchived), errors.Is(err, netproto.ErrDiscussionMembership):
					return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, err.Error())
				default:
					return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "discussion operation failed")
				}
			}
			if len(result.Messages) > 0 {
				pub := s.publishedKey(client)
				if pub == "" {
					return s.sendErrorFor(client, requestOrigin(ctx), errCodePermissionDenied, "publish an encryption key before reading discussions")
				}
				gens := map[uint32]bool{}
				for _, m := range result.Messages {
					if !m.Deleted {
						gens[m.KeyID] = true
					}
				}
				result.Keys, result.Refused, _ = s.scopeKeyBundle(ctx, r.ChannelID, pub, gens)
			}
			result.CanManage = s.roleAllowed(ctx, client, r.ChannelID, authorization.ManageChannels)
			result.CanModerate = s.roleAllowed(ctx, client, r.ChannelID, authorization.ManageMessages)
			if mutation && r.Action != "subscribe" && r.Action != "join" && r.Action != "leave" {
				event := map[string]any{"channel_id": r.ChannelID, "thread_id": result.ThreadID, "new_message": r.Action == "send" || r.Action == "create", "author": client.UniqueID}
				if (r.Action == "send" || r.Action == "create") && result.MessageID > 0 {
					event["message_id"] = result.MessageID
				}
				if r.Action == "delete" {
					event["deleted"] = true
				}
				if r.Action == "delete_message" {
					event["deleted_message_id"] = r.MessageID
				}
				deleted := r.Action == "delete" || r.Action == "delete_message"
				if !deleted {
					s.broadcastScope(ctx, r.ChannelID, "discussion_changed", event)
				}
				if result.ThreadID > 0 {
					s.notifyDiscussionReaders(ctx, backend, r.ChannelID, result.ThreadID, event, deleted)
				}
			}
			return s.writeCommittedReply(client, netproto.MsgDiscussionResult, result)
		})
	})
}

func (s *TCPServer) notifyDiscussionReaders(ctx context.Context, backend discussionStore, channelID, threadID int64, event any, allReaders bool) {
	if s.deps.State == nil || s.deps.Broadcast == nil {
		return
	}
	clients := s.deps.State.ListClients()
	var followers map[string]bool
	if !allReaders {
		online := make([]string, 0, len(clients))
		for _, client := range clients {
			online = append(online, client.UniqueID)
		}
		var err error
		followers, err = backend.DiscussionFollowers(ctx, threadID, online)
		if err != nil {
			return
		}
	}
	payload, err := eventEnvelope("discussion_changed", event)
	if err != nil {
		return
	}
	guarded, err := eventEnvelope(roleChannelDelivery, roleChannelEvent{ChannelID: channelID, Payload: payload})
	if err != nil {
		return
	}
	// Deletions invalidate every authorized open view, including readers who
	// have not followed or whose join raced with the deletion transaction.
	for _, sc := range clients {
		if !allReaders && (!followers[sc.UniqueID] || sc.ChannelID == channelID || s.deps.State.IsSubscribed(sc.ClientID, channelID)) {
			continue
		}
		if client, ok := s.clientByID(sc.ClientID); ok {
			_ = s.withRoleAccess(ctx, client, channelID, authorization.ReadHistory, func(context.Context) error { return s.deps.Broadcast.BroadcastToClient(client.ID, guarded) })
		}
	}
}

func (s *TCPServer) validateDiscussionBody(ctx context.Context, client *Client, r netproto.DiscussionRequest) error {
	if s.chatKeys == nil || r.BodyEnc == "" || r.KeyID == 0 {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "encrypted discussion body required")
	}
	current, _, err := s.chatKeys.current(ctx, r.ChannelID)
	if err != nil || current != r.KeyID {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "stale chat key — wait for re-key")
	}
	plain, err := s.chatKeys.open(ctx, r.ChannelID, r.KeyID, r.BodyEnc)
	if err != nil || strings.TrimSpace(plain) == "" || len(plain) > 12000 || (s.cfg != nil && s.cfg.ChatMaxLength > 0 && len(plain) > s.cfg.ChatMaxLength) {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "invalid discussion message")
	}
	moderated := r.Title + "\n" + strings.Join(r.Tags, "\n") + "\n" + stripAttachmentRefs(plain)
	if err = s.moderateBody(ctx, moderated); err != nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, err.Error())
	}
	if ch, ok := s.deps.State.GetChannel(r.ChannelID); ok && ch.SlowModeSeconds > 0 && !s.roleAllowed(ctx, client, r.ChannelID, authorization.BypassSlowmode) {
		if s.chatSlow.check(client.UniqueID, r.ChannelID, ch.SlowModeSeconds, time.Now()) > 0 {
			return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "slow mode — wait before sending")
		}
	}
	if s.chatSpam != nil && s.chatSpam.record(client.UniqueID, bodyDigest(moderated), time.Now()) {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "possible spam detected")
	}
	return nil
}
