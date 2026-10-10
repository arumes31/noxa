// handlers_channels.go implements channel lifecycle and membership changes.
package server

import (
	"context"
	"errors"
	"io/fs"
	"strconv"
	"sync"
	"time"

	"go.uber.org/zap"

	"noxa/internal/auth"
	"noxa/internal/authorization"
	"noxa/internal/channels"
	"noxa/internal/netproto"
	"noxa/internal/recorder"
	"noxa/internal/state"
)

const maxConcurrentDeletedRecorderStops = 8

// ApplyChannelDeletion publishes every side effect shared by explicit and
// automatic channel-subtree deletion. ChannelManager releases its lifecycle
// lock before invoking this method as the temporary-cleanup sink.
func (s *TCPServer) ApplyChannelDeletion(result channels.DeleteResult, reason string) {
	if s == nil || s.deps == nil || len(result.ChannelIDs) == 0 {
		return
	}
	// Revoke every subtree capability before any potentially blocking voice,
	// recorder, filesystem, or notification work. This closes the authorization
	// boundary before the committed deletion becomes externally visible.
	if s.deps.FileTransfer != nil {
		for _, channelID := range result.ChannelIDs {
			if err := s.deps.FileTransfer.TombstoneChannelData(channelID); err != nil {
				s.logger.Warn("tombstoning deleted channel files failed",
					zap.Int64("channel_id", channelID),
					zap.Error(err),
				)
			}
		}
	}
	s.broadcastEvent(eventChannelDeleted, channelEvent{
		ChannelID:  result.RootID,
		ChannelIDs: result.ChannelIDs,
		Reason:     reason,
	})
	if s.deps.Voice != nil {
		for _, member := range result.Members {
			s.deps.Voice.LeaveChannel(member.ClientID, member.ChannelID)
		}
	}
	// State-facing consequences must not sit behind filesystem cleanup. A file
	// mutation can legitimately hold its lifecycle read lock while finishing a
	// database/blob move; clients, metrics, and recorders still need to observe
	// the committed deletion immediately.
	s.pushSubscriptionStateTo(context.Background(), result.SubscriberIDs)
	s.stopDeletedChannelRecordings(result.ChannelIDs)
	cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cleanupCancel()
	for _, channelID := range result.ChannelIDs {
		if s.deps.FileTransfer != nil {
			if err := s.deps.FileTransfer.DeleteChannelData(cleanupCtx, channelID); err != nil {
				s.logger.Warn("removing deleted channel files failed",
					zap.Int64("channel_id", channelID),
					zap.Error(err),
				)
			}
		}
		if _, err := s.assets().removeImage("icons", strconv.FormatInt(channelID, 10)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			s.logger.Warn("removing deleted channel icon failed",
				zap.Int64("channel_id", channelID),
				zap.Error(err),
			)
		}
	}
}

func (s *TCPServer) stopDeletedChannelRecordings(channelIDs []int64) {
	if s.deps.Recorder == nil || len(channelIDs) == 0 {
		return
	}
	workers := min(maxConcurrentDeletedRecorderStops, len(channelIDs))
	jobs := make(chan int64)
	var wg sync.WaitGroup
	wg.Add(workers)
	for range workers {
		go func() {
			defer wg.Done()
			for channelID := range jobs {
				if err := s.deps.Recorder.Stop(channelID); err != nil && !errors.Is(err, recorder.ErrNotRecording) {
					s.logger.Warn("stopping deleted channel recording failed",
						zap.Int64("channel_id", channelID),
						zap.Error(err),
					)
				}
			}
		}()
	}
	for _, channelID := range channelIDs {
		jobs <- channelID
	}
	close(jobs)
	wg.Wait()
}

// BroadcastChannelUpdated announces a channel's current editable fields to
// all clients (used by out-of-band edits, e.g. ServerQuery channeledit).
func (s *TCPServer) BroadcastChannelUpdated(channelID int64) {
	if s.deps == nil || s.deps.State == nil {
		return
	}
	ch, ok := s.deps.State.GetChannel(channelID)
	if !ok {
		return
	}
	s.broadcastEvent(eventChannelUpdated, channelUpdatedEventFor(ch))
}

// channelUpdatedEventFor snapshots a channel's editable fields for the
// channel_updated event.
func channelUpdatedEventFor(ch *state.Channel) channelUpdatedEvent {
	return channelUpdatedEvent{
		ChannelID:       ch.ChannelID,
		Topic:           ch.Topic,
		MaxClients:      ch.MaxClients,
		OpusBitrate:     ch.OpusBitrate,
		OpusFEC:         ch.OpusFEC,
		OpusDTX:         ch.OpusDTX,
		OpusStereo:      ch.OpusStereo,
		SlowModeSeconds: ch.SlowModeSeconds,
		Description:     ch.Description,
		OrderIndex:      ch.OrderIndex,
		ParentID:        ch.ParentID,
	}
}

// handleJoinChannel moves the calling client into the target channel. The
// role evaluator authorizes Connect, and any channel password is checked
// independently.
func (s *TCPServer) handleJoinChannel(ctx context.Context, client *Client, f *netproto.Frame) error {
	var msg netproto.JoinChannel
	if err := netproto.Decode(f, &msg); err != nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "malformed join_channel: "+err.Error())
	}
	if msg.AckRequested && msg.ChannelID < 0 {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "invalid channel")
	}
	if s.deps == nil || s.deps.State == nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "state backend unavailable")
	}
	// Channel zero is the connected lobby. Leaving your own channel requires
	// no moderation or join permission and never closes the server connection.
	if msg.ChannelID == 0 {
		return s.rolePolicyRead(ctx, client, func(ctx context.Context) error {
			s.roleMetadataMu.Lock()
			defer s.roleMetadataMu.Unlock()
			client.roleActionMu.Lock()
			defer client.roleActionMu.Unlock()
			if err := s.leaveOwnChannelInContext(ctx, client); err != nil {
				return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, err.Error())
			}
			return s.acknowledgeJoin(client, msg)
		})
	}
	return s.roleAction(ctx, client, msg.ChannelID, authorization.Connect, func(ctx context.Context) error {
		return s.joinChannelAllowed(ctx, client, msg)
	})
}

func (s *TCPServer) joinChannelAllowed(ctx context.Context, client *Client, msg netproto.JoinChannel) error {
	s.roleMetadataMu.Lock()
	defer s.roleMetadataMu.Unlock()
	client.roleActionMu.Lock()
	defer client.roleActionMu.Unlock()
	// (215) acceptance is a condition of entry, not a notice: an unanswered
	// rules prompt keeps the client in the lobby, where the only thing it can
	// still do is answer.
	if client.rulesBlocked() {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodePermissionDenied, "accept the server rules before joining a channel")
	}
	ch, ok := s.deps.State.GetChannel(msg.ChannelID)
	if !ok {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeNotFound, "channel not found")
	}

	bypassPassword := s.roleAllowed(ctx, client, msg.ChannelID, authorization.BypassChannelPassword)
	if ch.PasswordHash != "" && !bypassPassword {
		if err := auth.VerifyPassword(msg.Password, ch.PasswordHash); err != nil {
			return s.sendErrorFor(client, requestOrigin(ctx), errCodePermissionDenied, "invalid channel password")
		}
	}

	if err := s.moveClient(ctx, client.ID, msg.ChannelID, client.ID); err != nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeNotFound, err.Error())
	}
	return s.acknowledgeJoin(client, msg)
}

func (s *TCPServer) leaveOwnChannelInContext(ctx context.Context, client *Client) error {
	var previousChannelID int64
	if s.deps.Channels != nil {
		var err error
		previousChannelID, err = s.deps.Channels.LeaveClient(client.ID)
		if errors.Is(err, state.ErrNotInChannel) {
			return nil
		}
		if err != nil {
			return err
		}
	} else {
		snapshot, ok := s.deps.State.GetClient(client.ID)
		if !ok {
			return state.ErrClientNotFound
		}
		previousChannelID = snapshot.ChannelID
		if err := s.deps.State.LeaveChannel(client.ID); err != nil && !errors.Is(err, state.ErrNotInChannel) {
			return err
		}
	}
	s.deps.State.SetPrioritySpeaker(client.ID, false)
	client.rememberOwnChannelMove(previousChannelID, 0, false)
	s.deps.State.SetSharing(client.ID, false)
	if previousChannelID != 0 {
		if s.deps.Voice != nil {
			s.deps.Voice.LeaveChannel(client.ID, previousChannelID)
		}
		s.rotateScopeKey(ctx, previousChannelID)
	}
	_ = s.sendSubscriptionState(ctx, client)
	s.broadcastEvent(eventUserMoved, userEvent{
		ClientID: client.ID, FromChannelID: previousChannelID,
		ChannelID: 0, ByClientID: client.ID,
	})
	return nil
}

// handleMoveClient uses current role authority and hierarchy.
func (s *TCPServer) handleMoveClient(ctx context.Context, client *Client, f *netproto.Frame) error {
	var msg netproto.MoveClient
	if err := netproto.Decode(f, &msg); err != nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "malformed move_client: "+err.Error())
	}
	err := s.withRolePolicy(ctx, func(ctx context.Context) error {
		s.roleMetadataMu.Lock()
		defer s.roleMetadataMu.Unlock()
		if client.sessionRevoked() || client.rulesBlocked() {
			return authorization.ErrRoleForbidden
		}
		e := ctx.Value(roleLeaseKey{}).(roleLease).evaluator
		return s.moveRoleMember(ctx, e, client.userID(), client.uniqueID(), client.ID, msg)
	})
	if err != nil {
		return s.roleError(ctx, client, err)
	}
	return s.acknowledgeMove(client, msg)
}

// moveClient moves a client into a channel in the state manager, performs the
// temp-channel cleanup bookkeeping for the source and target channels, keeps
// the voice router's membership in sync, and announces the move.
func (s *TCPServer) moveClient(ctx context.Context, clientID string, channelID int64, movedBy string) error {
	return s.moveClientWithCause(ctx, clientID, channelID, movedBy, movedBy != "" && movedBy != clientID)
}

func (s *TCPServer) moveClientWithCause(ctx context.Context, clientID string, channelID int64, movedBy string, forced bool) error {
	afterMove := func(previousChannelID int64) {
		if previousChannelID != channelID {
			if client, ok := s.clientByID(clientID); ok {
				client.rememberOwnChannelMove(previousChannelID, channelID, forced)
			}
			// Moving invalidates publications even when the destination permits sharing.
			s.deps.State.SetSharing(clientID, false)
		}
		// The destination can revoke active controls even without a policy
		// edit. The caller retains its policy and membership action locks.
		if lease, ok := ctx.Value(roleLeaseKey{}).(roleLease); ok {
			if member, present := s.deps.State.GetClient(clientID); present {
				if !lease.evaluator.Evaluate(member.UserID, channelID, authorization.PrioritySpeaker).Allowed {
					s.deps.State.SetPrioritySpeaker(clientID, false)
				}
				if !lease.evaluator.Evaluate(member.UserID, channelID, authorization.ShareScreen).Allowed {
					s.deps.State.SetSharing(clientID, false)
				}
			}
		}
		if s.deps.Voice != nil {
			if lease, ok := ctx.Value(roleLeaseKey{}).(roleLease); ok {
				s.refreshRolePublishers(lease.evaluator)
			}
			if previousChannelID != 0 && previousChannelID != channelID {
				s.deps.Voice.LeaveChannel(clientID, previousChannelID)
			}
			s.deps.Voice.JoinChannel(clientID, channelID)
		}
		// Chat keys (4b): the client gets the new channel's key; the channel it
		// left rotates so ex-members cannot read new messages.
		if previousChannelID != 0 && previousChannelID != channelID {
			s.rotateScopeKey(ctx, previousChannelID)
		}
		if client, ok := s.clientByID(clientID); ok {
			// The move is already committed when this lifecycle callback runs.
			// A cancelled request or key backend failure must not undo it.
			if err := s.deliverScopeKey(ctx, client, channelID); err != nil {
				s.logger.Warn("delivering channel key after move failed",
					zap.String("client_id", client.ID),
					zap.Int64("channel_id", channelID),
					zap.Error(err),
				)
			}
			// (312) the channel a client stands in is implicitly subscribed, so a
			// move changes the authoritative set even though nothing was asked.
			_ = s.sendSubscriptionState(ctx, client)
		}
		s.broadcastEvent(eventUserMoved, userEvent{
			ClientID:      clientID,
			FromChannelID: previousChannelID,
			ChannelID:     channelID,
			ByClientID:    movedBy,
		})
	}
	if s.deps.Channels != nil {
		backend, ok := s.deps.Channels.(interface {
			MoveClientWithinCapacity(string, int64, func(int64)) (int64, error)
		})
		if !ok {
			return authorization.ErrAuthorizationUnavailable
		}
		_, err := backend.MoveClientWithinCapacity(clientID, channelID, afterMove)
		return err
	}
	var oldChannelID int64
	if sc, ok := s.deps.State.GetClient(clientID); ok {
		oldChannelID = sc.ChannelID
	}
	if err := s.deps.State.MoveClientWithinCapacity(clientID, channelID); err != nil {
		return err
	}
	afterMove(oldChannelID)
	return nil
}
