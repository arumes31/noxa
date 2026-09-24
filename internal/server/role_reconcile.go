package server

import (
	"context"
	"errors"

	"noxa/internal/authorization"
)

// reconcileRolePolicy joins the live revocation barriers. Authority calls it
// under its exclusive gate. Attempt every subsystem even if one fails: a
// failed recording stop must not leave old file links or chat keys valid.
// Startup/cutover wiring remains separate from this runtime hook.
func (s *TCPServer) reconcileRolePolicy(ctx context.Context, before, after *authorization.RoleEvaluator) error {
	s.reconcileRoleFiles(after)
	channelErr := s.reconcileRoleChannels(ctx, before, after)
	mediaErr := s.reconcileRoleMedia(ctx, before, after)
	chatErr := s.reconcileRoleChat(ctx, before, after)
	if err := errors.Join(channelErr, mediaErr, chatErr); err != nil {
		return err
	}
	if s.deps.Voice != nil {
		for _, member := range s.deps.State.ListClients() {
			if member.ChannelID > 0 {
				s.deps.Voice.RefreshSubscriber(member.ClientID)
			}
		}
	}
	return nil
}

// ReconcileRolePolicy is the production Authority callback. Startup wires it
// before opening listeners, then performs one Reload to reconcile persisted
// channels and install every live revocation barrier.
func (s *TCPServer) ReconcileRolePolicy(ctx context.Context, before, after *authorization.RoleEvaluator) error {
	return s.reconcileRolePolicy(ctx, before, after)
}
