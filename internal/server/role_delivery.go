package server

import (
	"context"
	"encoding/json"
	"errors"

	"noxa/internal/authorization"
	"noxa/internal/netproto"
)

// This envelope is internal to the outbound queue. The writer removes it only
// after checking the current policy, holding that revision through the write.
// Enqueue-time checks alone cannot revoke data waiting behind a slow socket.
const roleChannelDelivery = "_role_channel_delivery"

type roleChannelEvent struct {
	ChannelID int64           `json:"channel_id"`
	Payload   json.RawMessage `json:"payload"`
}

func (s *TCPServer) writeRoleBroadcast(client *Client, payload []byte) error {
	return s.writeRoleBroadcastInContext(context.Background(), client, payload)
}

func (s *TCPServer) writeRoleBroadcastInContext(ctx context.Context, client *Client, payload []byte) error {
	err := s.withRoleSession(ctx, client, func(ctx context.Context) error {
		var event struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(payload, &event); err != nil {
			return err
		}
		if event.Type == eventPrivateCallSignal {
			s.privateCallsMu.Lock()
			defer s.privateCallsMu.Unlock()
		}
		if event.Type == eventSpeakingChanged || event.Type == eventWhisper || event.Type == eventAvatarChanged || event.Type == eventPoke || event.Type == eventPrioritySpeakerChanged || event.Type == eventScreenshareChanged || event.Type == eventStreamWatchStarted || event.Type == eventPosition {
			// A queued activity write must finish before moderation can
			// acknowledge a mute/deafen, or recheck after that change. Avatar
			// notifications likewise must not outlive the member's visibility.
			s.roleMetadataMu.Lock()
			defer s.roleMetadataMu.Unlock()
		}
		e := ctx.Value(roleLeaseKey{}).(roleLease).evaluator
		frame, err := s.roleBroadcastFrame(client, payload, e)
		if err != nil || frame == nil {
			return err
		}
		return s.writeFrameInContext(ctx, client, frame)
	})
	// An unavailable policy closes protected delivery, not the control socket.
	// The mutation handler still owes its saved-but-pending acknowledgement.
	if errors.Is(err, authorization.ErrAuthorizationUnavailable) || errors.Is(err, context.DeadlineExceeded) {
		return nil
	}
	return err
}

// Structural events are replaced with a fresh filtered snapshot. In particular
// a move/leave cannot reveal a formerly hidden source, parent or member name.
func (s *TCPServer) roleBroadcastFrame(client *Client, payload []byte, e *authorization.RoleEvaluator) (*netproto.Frame, error) {
	var envelope struct {
		Type string          `json:"type"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return nil, err
	}
	switch envelope.Type {
	case eventPrivateCall:
		// Direct opaque invalidation; the state request checks participant/session.
	case eventPrivateCallSignal:
		var signal netproto.CallSignal
		if err := json.Unmarshal(envelope.Data, &signal); err != nil {
			return nil, err
		}
		call, found := s.privateCalls[signal.CallID]
		sender, senderFound := call.Participant(signal.From)
		recipient, recipientFound := call.Participant(client.UniqueID)
		if !found || call.EndedAt != 0 || !senderFound || !recipientFound || sender.State != "accepted" || recipient.State != "accepted" || recipient.ClientID != client.ID || signal.To != client.UniqueID {
			return nil, nil
		}
		liveSender, online := s.clientByID(sender.ClientID)
		if !online || !liveSender.isAuthed() || liveSender.sessionRevoked() || liveSender.UniqueID != signal.From {
			return nil, nil
		}
	case eventStreamWatchStarted:
		var event streamWatchStartedEvent
		if err := json.Unmarshal(envelope.Data, &event); err != nil {
			return nil, err
		}
		if event.PublisherID != client.ID || s.deps.Voice == nil {
			return nil, nil
		}
		current := false
		for _, stream := range s.deps.Voice.VideoPublications(client.ID) {
			if stream.PublisherID == client.ID && stream.Slot == event.Slot && stream.Generation == event.Generation {
				current = true
				break
			}
		}
		if !current {
			return nil, nil
		}
	case roleChannelDelivery:
		var event roleChannelEvent
		if err := json.Unmarshal(envelope.Data, &event); err != nil {
			return nil, err
		}
		if !e.Evaluate(client.userID(), event.ChannelID, authorization.ViewChannel).Allowed {
			return nil, nil
		}
		payload = event.Payload
		var scoped struct {
			Type string          `json:"type"`
			Data json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(payload, &scoped); err != nil {
			return nil, err
		}
		if scoped.Type == eventChat {
			var chat netproto.ChatBroadcast
			if err := json.Unmarshal(scoped.Data, &chat); err != nil {
				return nil, err
			}
			var err error
			payload, err = recipientChatPayload(chat, client.uniqueID())
			if err != nil {
				return nil, err
			}
		}
	case eventUserJoined, eventUserLeft, eventUserMoved, eventChannelCreated, eventChannelDeleted, eventChannelUpdated, eventStatusChanged, eventMemberVoiceChanged:
		return netproto.Encode(netproto.MsgSnapshot, buildRoleSnapshot(s.deps.State, e, client.userID(), client.uniqueID()))
	case eventAvatarChanged, eventSpeakingChanged, eventPrioritySpeakerChanged, eventPosition, eventScreenshareChanged:
		var event struct {
			ClientID  string `json:"client_id"`
			ChannelID *int64 `json:"channel_id"`
			Speaking  bool   `json:"speaking"`
			Active    bool   `json:"active"`
		}
		if err := json.Unmarshal(envelope.Data, &event); err != nil {
			return nil, err
		}
		member, ok := s.deps.State.GetClient(event.ClientID)
		if !ok || !e.Evaluate(client.userID(), member.ChannelID, authorization.ViewChannel).Allowed {
			return nil, nil
		}
		if envelope.Type == eventSpeakingChanged && event.Speaking && (member.ServerMuted || !member.IsSpeaking) {
			return nil, nil
		}
		if envelope.Type == eventPosition {
			recipient, present := s.deps.State.GetClient(client.ID)
			if !present || recipient.ChannelID <= 0 || recipient.ChannelID != member.ChannelID ||
				!e.Evaluate(recipient.UserID, recipient.ChannelID, authorization.Connect).Allowed {
				return nil, nil
			}
		}
		if (envelope.Type == eventPrioritySpeakerChanged && event.Active != member.PrioritySpeaker) ||
			(envelope.Type == eventScreenshareChanged && event.Active != member.Sharing) {
			return nil, nil
		}
		if event.Active && ((envelope.Type == eventPrioritySpeakerChanged && (member.ChannelID <= 0 || !e.Evaluate(member.UserID, member.ChannelID, authorization.PrioritySpeaker).Allowed)) ||
			(envelope.Type == eventScreenshareChanged && (member.ChannelID <= 0 || !e.Evaluate(member.UserID, member.ChannelID, authorization.ShareScreen).Allowed))) {
			return nil, nil
		}
		if envelope.Type != eventAvatarChanged && (event.ChannelID == nil || *event.ChannelID != member.ChannelID) {
			return nil, nil
		}
		if member.Status == "invisible" && member.UniqueID != client.uniqueID() && !e.Evaluate(client.userID(), 0, authorization.ViewConnectionInfo).Allowed {
			return nil, nil
		}
	case eventChannelIconChanged:
		var event channelEvent
		if err := json.Unmarshal(envelope.Data, &event); err != nil {
			return nil, err
		}
		if !e.Evaluate(client.userID(), event.ChannelID, authorization.ViewChannel).Allowed {
			return nil, nil
		}
	case eventKicked:
		var event kickEvent
		if err := json.Unmarshal(envelope.Data, &event); err != nil {
			return nil, err
		}
		// Other members learn the new visible tree, without private reasons or
		// the identities of moderators acting in channels they cannot see.
		if event.ClientID != client.ID {
			return netproto.Encode(netproto.MsgSnapshot, buildRoleSnapshot(s.deps.State, e, client.userID(), client.uniqueID()))
		}
	case eventAnnouncement:
		if !e.Evaluate(client.userID(), 0, authorization.ViewChannel).Allowed {
			return nil, nil
		}
	case eventChat:
		// Channel/global messages must use the guarded queue envelope. Only
		// direct messages, separately addressed by the server, arrive unwrapped.
		var event netproto.ChatBroadcast
		if err := json.Unmarshal(envelope.Data, &event); err != nil {
			return nil, err
		}
		if !event.Direct {
			return nil, nil
		}
		var err error
		payload, err = recipientChatPayload(event, client.uniqueID())
		if err != nil {
			return nil, err
		}
	case eventWhisper:
		var event whisperEvent
		if err := json.Unmarshal(envelope.Data, &event); err != nil {
			return nil, err
		}
		sender, ok := s.clientByID(event.FromClientID)
		if !ok || event.ChannelID <= 0 {
			return nil, nil
		}
		source, ok := s.deps.State.GetClient(event.FromClientID)
		if !ok || source.ChannelID != event.ChannelID {
			return nil, nil
		}
		target, ok := s.deps.State.GetClient(client.ID)
		if !ok || target.ChannelID <= 0 || !e.Evaluate(client.userID(), target.ChannelID, authorization.Connect).Allowed ||
			!e.Evaluate(client.userID(), source.ChannelID, authorization.ViewChannel).Allowed ||
			!e.Evaluate(sender.userID(), source.ChannelID, authorization.Whisper).Allowed ||
			!e.Evaluate(sender.userID(), target.ChannelID, authorization.Whisper).Allowed {
			return nil, nil
		}
		if event.Speaking && (source.ServerMuted || target.ServerDeafened || !source.IsSpeaking) {
			return nil, nil
		}
		if source.Status == "invisible" && source.UniqueID != client.uniqueID() && !e.Evaluate(client.userID(), 0, authorization.ViewConnectionInfo).Allowed {
			return nil, nil
		}
	case eventPoke:
		var event pokeEvent
		if err := json.Unmarshal(envelope.Data, &event); err != nil {
			return nil, err
		}
		sender, ok := s.clientByID(event.FromClientID)
		if !ok || !sender.isAuthed() || !e.Evaluate(sender.userID(), 0, authorization.PokeMembers).Allowed || !s.rolePokeTargetVisible(e, sender, client.ID) {
			return nil, nil
		}
	case eventConversationChanged:
		// Addressed invalidation only. No content/membership crosses this queue;
		// fetching either requires a current database membership lease.
	case eventTyping, eventDMDelivered, eventDMRead,
		eventEmojiAdded, eventEmojiRemoved, eventEmojiRenamed, eventServerBannerChanged:
		// Direct or server-wide events. Channel typing uses the guarded envelope.
	default:
		// Retired permission/group events and new event types need an explicit
		// delivery policy before they can enter the role-mode wire protocol.
		return nil, nil
	}
	return &netproto.Frame{Type: uint16(netproto.MsgEvent), Payload: payload}, nil
}

// Mentions are notification metadata. Sending the complete resolved list would
// disclose hidden/invisible members even when the author can see them. The
// desktop only needs to know whether this recipient was mentioned.
func recipientChatPayload(chat netproto.ChatBroadcast, uniqueID string) ([]byte, error) {
	roleMentions := chat.RoleMentions
	chat.RoleMentions = nil
	for _, mentioned := range roleMentions {
		if uniqueID != "" && mentioned == uniqueID {
			chat.RoleMentions = []string{uniqueID}
			break
		}
	}
	mentions := chat.Mentions
	chat.Mentions = nil
	for _, mentioned := range mentions {
		if uniqueID != "" && mentioned == uniqueID {
			chat.Mentions = []string{uniqueID}
			break
		}
	}
	return eventEnvelope(eventChat, chat)
}
