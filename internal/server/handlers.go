// handlers.go implements the shared TCP control event types and delivery helpers.
// Domain handlers live in the handlers_*.go files. All handlers run on the
// client's connection goroutine and reply via s.writeMessage; asynchronous
// traffic (snapshots excluded) is delivered by the broadcaster through the
// client's broadcast writer goroutine as MsgEvent frames.
package server

import (
	"context"
	"encoding/json"

	"go.uber.org/zap"

	"noxa/internal/authorization"
	"noxa/internal/netproto"
)

// Broadcast event types sent in MsgEvent envelopes.
const (
	eventUserJoined     = "user_joined"
	eventUserLeft       = "user_left"
	eventUserMoved      = "user_moved"
	eventChannelCreated = "channel_created"
	eventChannelDeleted = "channel_deleted"
	eventKicked         = "kicked"
	eventChat           = "chat"
	eventChannelUpdated = "channel_updated"
)

// userEvent is the payload of user_joined / user_left / user_moved events.
type userEvent struct {
	ClientID      string `json:"client_id"`
	UniqueID      string `json:"unique_id,omitempty"`
	Nickname      string `json:"nickname,omitempty"`
	ChannelID     int64  `json:"channel_id,omitempty"`
	FromChannelID int64  `json:"from_channel_id,omitempty"`
	ByClientID    string `json:"by_client_id,omitempty"`
}

// channelEvent is the payload of channel_created / channel_deleted events.
type channelEvent struct {
	ChannelID  int64   `json:"channel_id"`
	ChannelIDs []int64 `json:"channel_ids,omitempty"`
	Name       string  `json:"name,omitempty"`
	ParentID   int64   `json:"parent_id,omitempty"`
	Reason     string  `json:"reason,omitempty"`
}

// channelUpdatedEvent is the payload of channel_updated events, carrying the
// editable channel fields after a ChannelEdit.
type channelUpdatedEvent struct {
	ChannelID       int64  `json:"channel_id"`
	Topic           string `json:"topic"`
	MaxClients      int    `json:"max_clients"`
	OpusBitrate     int    `json:"opus_bitrate"`
	OpusFEC         bool   `json:"opus_fec"`
	OpusDTX         bool   `json:"opus_dtx"`
	OpusStereo      bool   `json:"opus_stereo"`
	SlowModeSeconds int    `json:"slow_mode_seconds"`
	Description     string `json:"description"`
	OrderIndex      int    `json:"order_index"`
	ParentID        int64  `json:"parent_id"`
}

// kickEvent is the payload of kicked events.
type kickEvent struct {
	ClientID   string `json:"client_id"`
	ChannelID  int64  `json:"channel_id,omitempty"`
	ByClientID string `json:"by_client_id"`
	Reason     string `json:"reason,omitempty"`
	FromServer bool   `json:"from_server"`
	Ban        bool   `json:"ban"`
	ExpiresAt  int64  `json:"expires_at,omitempty"`
}

// handlePing replies with a Pong.
func (s *TCPServer) handlePing(ctx context.Context, client *Client, _ *netproto.Frame) error {
	s.logger.Debug("ping received", zap.String("client_id", client.ID))
	return s.writeMessage(client, netproto.MsgPong, netproto.Pong{})
}

// sendSnapshot builds the current channel-tree snapshot and sends it to the
// client as a MsgSnapshot frame.
func (s *TCPServer) sendSnapshot(client *Client) error {
	if s.deps == nil || s.deps.State == nil {
		return nil
	}
	if s.deps.Authority == nil {
		return authorization.ErrAuthorizationUnavailable
	}
	return s.deps.Authority.WithPolicy(context.Background(), func(e *authorization.RoleEvaluator) error {
		if client.sessionRevoked() {
			return authorization.ErrRoleForbidden
		}
		return s.writeMessage(client, netproto.MsgSnapshot, buildRoleSnapshot(s.deps.State, e, client.userID(), client.uniqueID()))
	})
}

// broadcastEvent marshals payload and broadcasts it to all registered clients
// as an event envelope. It is a no-op when no broadcaster is wired.
func (s *TCPServer) broadcastEvent(eventType string, payload any) {
	if s.deps == nil || s.deps.Broadcast == nil {
		return
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		s.logger.Error("failed to marshal event payload",
			zap.String("event_type", eventType),
			zap.Error(err),
		)
		return
	}
	s.deps.Broadcast.BroadcastEvent(eventType, raw)
}

// broadcastToAdmins sends an event to every online server admin (used by
// the invisible-status semantics, 381).
func (s *TCPServer) broadcastToAdmins(eventType string, payload any) {
	if s.deps == nil || s.deps.Broadcast == nil {
		return
	}
	s.broadcastEvent(eventType, payload)
}

// eventEnvelope wraps payload in the {"type": ..., "data": ...} envelope used
// for targeted broadcasts (BroadcastToChannel / BroadcastToClient), matching
// the shape Broadcaster.BroadcastEvent produces for server-wide events.
func eventEnvelope(eventType string, payload any) ([]byte, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	envelope := struct {
		Type string          `json:"type"`
		Data json.RawMessage `json:"data"`
	}{Type: eventType, Data: raw}
	return json.Marshal(envelope)
}

// broadcastWriter pumps outbound broadcast payloads to the client connection
// as MsgEvent frames until the broadcaster closes the channel (on Unregister)
// or a write fails.
func (s *TCPServer) broadcastWriter(client *Client, out <-chan []byte) {
	for payload := range out {
		if string(payload) == mediaLimitsNotification {
			if err := s.writeCurrentMediaLimits(context.Background(), client); err != nil {
				_ = client.Conn.Close()
				return
			}
			continue
		}
		if err := s.writeRoleBroadcast(client, payload); err != nil {
			_ = client.Conn.Close()
			return
		}
	}
}
