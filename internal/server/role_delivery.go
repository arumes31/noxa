package server

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"noxa/internal/authorization"
	"noxa/internal/netproto"
)

// This envelope is internal to the outbound queue. The writer removes it only
// after checking the current policy, holding that revision through the write.
// Enqueue-time checks alone cannot revoke data waiting behind a slow socket.
const roleChannelDelivery = "_role_channel_delivery"

// roleResyncDelivery is internal to the outbound queue too: the writer turns
// it into a fresh filtered snapshot for a client that missed events.
const (
	roleResyncDelivery = "_role_resync"
	roleResyncPayload  = `{"type":"` + roleResyncDelivery + `"}`
)

// metadataWriteCeiling bounds a delivery made while holding roleMetadataMu.
// Writes to a healthy peer land in its socket buffer at once; only a peer that
// stopped reading blocks, and it must not stall delivery to everyone else.
const metadataWriteCeiling = 1500 * time.Millisecond

type roleChannelEvent struct {
	ChannelID int64           `json:"channel_id"`
	Payload   json.RawMessage `json:"payload"`
}

func (s *TCPServer) writeRoleBroadcast(client *Client, payload []byte) error {
	return s.writeRoleBroadcastInContext(context.Background(), client, payload)
}

func (s *TCPServer) writeRoleBroadcastInContext(ctx context.Context, client *Client, payload []byte) error {
	if string(payload) == roleResyncPayload {
		// Cleared before the snapshot is built: a change after this point
		// queues its own event or another resync.
		client.resyncPending.Store(false)
		// Wait for a busy writer outside every shared lock, so the snapshot
		// rarely waits under roleMetadataMu and a stuck writer cannot keep
		// that lock busy with retries.
		waitCtx, cancel := context.WithTimeout(ctx, roleWriteCeiling)
		err := client.wmu.LockContext(waitCtx)
		cancel()
		if err != nil {
			s.requestResync(client.ID)
			return nil
		}
		client.wmu.Unlock()
	}
	err := s.withRoleSession(ctx, client, func(ctx context.Context) error {
		var event struct {
			Type string          `json:"type"`
			Data json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(payload, &event); err != nil {
			return err
		}
		presenceType := event.Type
		if event.Type == roleChannelDelivery {
			var wrapper roleChannelEvent
			if err := json.Unmarshal(event.Data, &wrapper); err != nil {
				return err
			}
			var inner struct {
				Type string `json:"type"`
			}
			if err := json.Unmarshal(wrapper.Payload, &inner); err != nil {
				return err
			}
			presenceType = inner.Type
		}
		if event.Type == eventPrivateCallSignal {
			s.privateCallsMu.Lock()
			defer s.privateCallsMu.Unlock()
		}
		metadata := false
		if isPresenceSignal(presenceType) || roleSnapshotEvent(event.Type) || event.Type == eventSpeakingChanged || event.Type == eventWhisper || event.Type == eventAvatarChanged || event.Type == eventPoke || event.Type == eventPrioritySpeakerChanged || event.Type == eventScreenshareChanged || event.Type == eventStreamWatchStarted || event.Type == eventStreamUploadChanged || event.Type == eventPosition {
			// A queued activity write must finish before moderation can
			// acknowledge a mute/deafen, or recheck after that change. Avatar
			// notifications likewise must not outlive the member's visibility.
			s.roleMetadataMu.Lock()
			defer s.roleMetadataMu.Unlock()
			metadata = true
		}
		e := ctx.Value(roleLeaseKey{}).(roleLease).evaluator
		frame, err := s.roleBroadcastFrame(client, payload, e)
		if err != nil || frame == nil {
			return err
		}
		if metadata {
			if err := s.writeMetadataFrame(ctx, client, frame); err != nil {
				return err
			}
		} else if err := s.writeFrameInContext(ctx, client, frame); err != nil {
			return err
		}
		if frame.Type == uint16(netproto.MsgSnapshot) {
			client.commitStagedSnapshot()
		}
		return nil
	})
	// An unavailable policy closes protected delivery, not the control socket.
	// The mutation handler still owes its saved-but-pending acknowledgement.
	if errors.Is(err, authorization.ErrAuthorizationUnavailable) {
		return nil
	}
	// A delivery skipped behind a busy writer leaves the client stale; a
	// resync queued behind it is built once it is written, after this change.
	if errors.Is(err, context.DeadlineExceeded) {
		s.requestResync(client.ID)
		return nil
	}
	return err
}

// writeMetadataFrame writes while holding roleMetadataMu, which every other
// recipient waits for, so waiting for the writer and the write share the
// short ceiling. A wait that runs out skips this delivery without closing the
// client (it is resynced instead): the write it waits behind closes a peer
// that stopped reading at its own 5 s deadline.
func (s *TCPServer) writeMetadataFrame(ctx context.Context, client *Client, frame *netproto.Frame) error {
	ctx, cancel := context.WithTimeout(ctx, metadataWriteCeiling)
	defer cancel()
	if err := client.wmu.LockContext(ctx); err != nil {
		return err
	}
	defer client.wmu.Unlock()
	return s.writeFrameLocked(ctx, client, frame)
}

// requestResync queues one fresh filtered snapshot for a client that missed
// events; further requests coalesce until the writer takes it. A full queue
// drops it, and the broadcaster asks again once that queue drains.
func (s *TCPServer) requestResync(clientID string) {
	client, ok := s.clientByID(clientID)
	if !ok || s.deps == nil || s.deps.Broadcast == nil || !client.resyncPending.CompareAndSwap(false, true) {
		return
	}
	if err := s.deps.Broadcast.BroadcastToClient(clientID, []byte(roleResyncPayload)); err != nil {
		client.resyncPending.Store(false)
	}
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
	case eventStreamWatchStarted, eventStreamUploadChanged:
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
				current = envelope.Type != eventStreamUploadChanged || stream.QualityMode == "source"
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
		if scoped.Type == eventTyping && !s.presenceSignalVisible(client, e, scoped.Data) {
			return nil, nil
		}
		if scoped.Type == "discussion_changed" && !e.Evaluate(client.userID(), event.ChannelID, authorization.ReadHistory).Allowed {
			return nil, nil
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
	case eventStatusChanged:
		// Status changes of members the recipient cannot see (an invisible
		// user) must not be revealed by the arrival of an identical snapshot.
		return s.roleSnapshotFrame(client, e)
	case eventUserJoined, eventUserLeft, eventUserMoved, eventMediaDeviceChanged, eventChannelCreated, eventChannelDeleted, eventChannelUpdated, eventNicknameChanged, eventMemberVoiceChanged, eventAudioStateChanged, eventVoiceQueueChanged, roleResyncDelivery:
		return s.roleSnapshotFullFrame(client, e)
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
		if s.deps.State.PresenceHidden(member) && member.UniqueID != client.uniqueID() && !e.Evaluate(client.userID(), 0, authorization.ViewConnectionInfo).Allowed {
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
			return s.roleSnapshotFullFrame(client, e)
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
		if s.deps.State.PresenceHidden(source) && source.UniqueID != client.uniqueID() && !e.Evaluate(client.userID(), 0, authorization.ViewConnectionInfo).Allowed {
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
	case eventConversationTyping, eventConversationRead:
		deliverable, err := s.conversationActivityDeliverable(client.uniqueID(), envelope.Data)
		if err != nil || !deliverable || !s.presenceSignalVisible(client, e, envelope.Data) {
			return nil, err
		}
	case eventTyping, eventDMDelivered, eventDMRead:
		if !s.presenceSignalVisible(client, e, envelope.Data) {
			return nil, nil
		}
	case eventEmojiAdded, eventEmojiRemoved, eventEmojiRenamed, eventServerBannerChanged, eventOlmPending, eventShutdownScheduled:
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

func isPresenceSignal(kind string) bool {
	switch kind {
	case eventTyping, eventDMDelivered, eventDMRead, eventConversationTyping, eventConversationRead:
		return true
	}
	return false
}
func (s *TCPServer) presenceSignalVisible(viewer *Client, e *authorization.RoleEvaluator, data []byte) bool {
	var signal struct {
		ClientID     string `json:"client_id"`
		FromUniqueID string `json:"from_unique_id"`
		UniqueID     string `json:"unique_id"`
	}
	if json.Unmarshal(data, &signal) != nil {
		return false
	}
	id := signal.ClientID
	if id == "" {
		uid := signal.FromUniqueID
		if uid == "" {
			uid = signal.UniqueID
		}
		if sender, ok := s.clientByUniqueID(uid); ok {
			id = sender.ID
		}
	}
	return id != "" && s.rolePokeTargetVisible(e, viewer, id)
}
