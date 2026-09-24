package server

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strconv"

	"noxa/internal/authorization"
	"noxa/internal/channels"
)

type roleChannelReconciler interface {
	ReconcileRoleChannels(context.Context, authorization.RolePolicy, func(channels.DeleteResult) error) error
}

func (s *TCPServer) reconcileRoleChannels(ctx context.Context, before, after *authorization.RoleEvaluator) error {
	if s.deps == nil || s.deps.State == nil {
		return authorization.ErrAuthorizationUnavailable
	}
	manager, ok := s.deps.Channels.(roleChannelReconciler)
	if !ok {
		// Reduced deployments may omit persistent channels. They must not accept
		// a policy tree change they cannot mirror and reconcile.
		old := before.Policy().Channels
		next := after.Policy().Channels
		parents := make(map[int64]int64, len(old))
		for _, ch := range old {
			parents[ch.ChannelID] = ch.ParentID
		}
		if len(old) != len(next) {
			return authorization.ErrAuthorizationUnavailable
		}
		for _, ch := range next {
			if parent, found := parents[ch.ChannelID]; !found || parent != ch.ParentID {
				return authorization.ErrAuthorizationUnavailable
			}
		}
		return nil
	}
	s.roleMetadataMu.Lock()
	defer s.roleMetadataMu.Unlock()
	return manager.ReconcileRoleChannels(ctx, after.Policy(), s.detachRoleDeletedChannels)
}

// Runs under the metadata and channel-tree barriers, before old membership is
// erased. Never call ApplyChannelDeletion here: its notifications re-enter the
// authority. Snapshot/subscription delivery belongs to reconcileRoleChat.
func (s *TCPServer) detachRoleDeletedChannels(deleted channels.DeleteResult) error {
	var revokeErr error
	if s.deps.FileTransfer != nil {
		for _, id := range deleted.ChannelIDs {
			revokeErr = errors.Join(revokeErr, s.deps.FileTransfer.TombstoneChannelData(id))
		}
	}
	for _, member := range deleted.Members {
		if s.deps.Voice != nil {
			s.deps.Voice.LeaveChannel(member.ClientID, member.ChannelID)
			s.deps.Voice.SetWhisper(member.ClientID, nil, nil, false)
		}
		s.deps.State.SetSpeaking(member.ClientID, false)
		s.deps.State.SetPrioritySpeaker(member.ClientID, false)
		s.deps.State.SetSharing(member.ClientID, false)
	}
	// Keep the old channel in state if cleanup fails, so recovery can retry
	// the same IDs before the tree mirror discards them.
	for _, id := range deleted.ChannelIDs {
		if _, err := s.assets().removeImage("icons", strconv.FormatInt(id, 10)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			revokeErr = errors.Join(revokeErr, fmt.Errorf("removing channel %d icon: %w", id, err))
		}
	}
	return revokeErr
}
