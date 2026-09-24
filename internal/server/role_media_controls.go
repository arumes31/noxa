package server

import (
	"context"
	"errors"
	"time"

	"noxa/internal/authorization"
	"noxa/internal/netproto"
)

// roleChannelControl pins policy and source membership through a control
// change. Turning a feature off remains possible after its grant is revoked.
func (s *TCPServer) roleChannelControl(ctx context.Context, client *Client, capability authorization.Capability, active bool, effect func(context.Context, int64) error) error {
	err := s.withRoleChannelControl(ctx, client, capability, active, effect)
	if errors.Is(err, authorization.ErrRoleForbidden) || errors.Is(err, authorization.ErrAuthorizationUnavailable) {
		return s.roleError(ctx, client, err)
	}
	return err
}

func (s *TCPServer) withRoleChannelControl(ctx context.Context, client *Client, capability authorization.Capability, active bool, effect func(context.Context, int64) error) error {
	return s.withRolePolicy(ctx, func(ctx context.Context) error {
		s.roleMetadataMu.Lock()
		defer s.roleMetadataMu.Unlock()
		client.roleActionMu.Lock()
		defer client.roleActionMu.Unlock()
		if s.deps.State == nil {
			return authorization.ErrAuthorizationUnavailable
		}
		channelID, _, present := s.deps.State.ClientChannelState(client.ID)
		if !present {
			return authorization.ErrRoleForbidden
		}
		if active && (channelID <= 0 || client.rulesBlocked() || !s.roleAllowed(ctx, client, channelID, capability)) {
			return authorization.ErrRoleForbidden
		}
		return effect(ctx, channelID)
	})
}

// Signaling callbacks run after the request that created a peer has returned.
// They must acquire their own lease instead of retaining its authorization.
func (s *TCPServer) writeVoiceSignal(client *Client, kind netproto.MessageType, body any) error {
	return s.withRoleChannelControl(context.Background(), client, authorization.Connect, true, func(context.Context, int64) error {
		return s.writeMessage(client, kind, body)
	})
}

func (s *TCPServer) roleWhisperSet(ctx context.Context, client *Client, msg netproto.WhisperSet) error {
	return s.roleChannelControl(ctx, client, authorization.Whisper, msg.Active, func(ctx context.Context, source int64) error {
		s.refreshRolePublishers(ctx.Value(roleLeaseKey{}).(roleLease).evaluator)
		if !msg.Active {
			s.deps.Voice.SetWhisper(client.ID, nil, nil, false)
			return s.acknowledgeMediaControl(client, msg.AckRequested, netproto.MediaControlSaved{
				Operation: netproto.MsgWhisperSet, Active: msg.Active, UniqueIDs: msg.UniqueIDs, ChannelIDs: msg.ChannelIDs,
			})
		}
		clientIDs := make([]string, 0, len(msg.UniqueIDs))
		for _, uid := range msg.UniqueIDs {
			target, ok := s.clientByUniqueID(uid)
			if !ok || target.rulesBlocked() {
				return authorization.ErrRoleForbidden
			}
			destination, _, present := s.deps.State.ClientChannelState(target.ID)
			if !present || destination <= 0 || !s.roleAllowed(ctx, client, destination, authorization.Whisper) ||
				!s.roleAllowed(ctx, target, destination, authorization.Connect) || !s.roleAllowed(ctx, target, source, authorization.ViewChannel) {
				return authorization.ErrRoleForbidden
			}
			clientIDs = append(clientIDs, target.ID)
		}
		for _, destination := range msg.ChannelIDs {
			if destination <= 0 || !s.roleAllowed(ctx, client, destination, authorization.Whisper) {
				return authorization.ErrRoleForbidden
			}
			if _, ok := s.deps.State.GetChannel(destination); !ok {
				return authorization.ErrRoleForbidden
			}
		}
		s.deps.Voice.SetWhisper(client.ID, clientIDs, msg.ChannelIDs, true)
		return s.acknowledgeMediaControl(client, msg.AckRequested, netproto.MediaControlSaved{
			Operation: netproto.MsgWhisperSet, Active: msg.Active, UniqueIDs: msg.UniqueIDs, ChannelIDs: msg.ChannelIDs,
		})
	})
}

func (s *TCPServer) rolePrioritySpeaker(ctx context.Context, client *Client, msg netproto.PrioritySpeaker) error {
	return s.roleChannelControl(ctx, client, authorization.PrioritySpeaker, msg.Active, func(_ context.Context, channelID int64) error {
		s.deps.State.SetPrioritySpeaker(client.ID, msg.Active)
		s.broadcastEvent(eventPrioritySpeakerChanged, prioritySpeakerEvent{ClientID: client.ID, ChannelID: channelID, Active: msg.Active})
		return s.acknowledgeMediaControl(client, msg.AckRequested, netproto.MediaControlSaved{Operation: netproto.MsgPrioritySpeaker, Active: msg.Active})
	})
}

func (s *TCPServer) roleScreenShare(ctx context.Context, client *Client, msg netproto.ScreenShare) error {
	return s.roleChannelControl(ctx, client, authorization.ShareScreen, msg.Active, func(_ context.Context, channelID int64) error {
		s.deps.State.SetSharing(client.ID, msg.Active)
		payload, err := eventEnvelope(eventScreenshareChanged, struct {
			ClientID  string `json:"client_id"`
			ChannelID int64  `json:"channel_id"`
			Active    bool   `json:"active"`
			MaxHeight int    `json:"max_height,omitempty"`
		}{client.ID, channelID, msg.Active, msg.MaxHeight})
		if err != nil {
			return err
		}
		s.deps.Broadcast.BroadcastToChannel(channelID, payload)
		return s.acknowledgeMediaControl(client, msg.AckRequested, netproto.MediaControlSaved{Operation: netproto.MsgScreenShare, Active: msg.Active, MaxHeight: msg.MaxHeight})
	})
}

func (s *TCPServer) rolePositionUpdate(ctx context.Context, client *Client, msg netproto.PositionUpdate) error {
	if msg.ChannelID <= 0 || !msg.ValidPosition() {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "invalid position")
	}
	return s.roleChannelControl(ctx, client, authorization.Connect, true, func(_ context.Context, channelID int64) error {
		if msg.ChannelID != channelID {
			return nil
		}
		client.mu.Lock()
		if time.Since(client.lastPositionAt) < 150*time.Millisecond {
			client.mu.Unlock()
			return nil
		}
		client.lastPositionAt = time.Now()
		client.mu.Unlock()
		payload, err := eventEnvelope(eventPosition, positionEvent{ChannelID: channelID, ClientID: client.ID, Context: msg.Context, X: msg.X, Y: msg.Y, Z: msg.Z})
		if err != nil {
			return err
		}
		s.deps.Broadcast.BroadcastToChannel(channelID, payload)
		return nil
	})
}
