package server

import (
	"context"
	"errors"
	"fmt"

	"noxa/internal/authorization"
	"noxa/internal/netproto"
	"noxa/internal/state"
)

// moveRoleMember runs with policy and metadata pinned by its caller. Movement,
// target moderation and media delivery serialize on the target's action lock.
// actorClientID is presentation metadata; only actorID determines authority.
func (s *TCPServer) moveRoleMember(ctx context.Context, e *authorization.RoleEvaluator, actorID int64, actorUniqueID, actorClientID string, msg netproto.MoveClient) error {
	if msg.ClientID == "" || msg.ChannelID <= 0 {
		return authorization.ErrRoleInvalid
	}
	if s.deps.State == nil {
		return authorization.ErrAuthorizationUnavailable
	}
	if !e.Evaluate(actorID, msg.ChannelID, authorization.MoveMembers).Allowed {
		return authorization.ErrRoleForbidden
	}
	target, ok := s.clientByID(msg.ClientID)
	if !ok || !target.isAuthed() {
		return authorization.ErrRoleForbidden
	}
	target.roleActionMu.Lock()
	defer target.roleActionMu.Unlock()
	before, ok := s.deps.State.GetClient(target.ID)
	if !ok || target.rulesBlocked() || !e.CanManageMember(actorID, target.userID()) ||
		!e.Evaluate(actorID, before.ChannelID, authorization.MoveMembers).Allowed ||
		!e.Evaluate(target.userID(), msg.ChannelID, authorization.Connect).Allowed ||
		(before.Status == "invisible" && !e.Evaluate(actorID, 0, authorization.ViewConnectionInfo).Allowed) {
		return authorization.ErrRoleForbidden
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.moveClient(ctx, target.ID, msg.ChannelID, actorClientID); err != nil {
		if errors.Is(err, state.ErrChannelFull) {
			return authorization.ErrRoleConflict
		}
		return err
	}
	s.auditInChannels(ctx, actorUniqueID, "member_moved", target.uniqueID(), fmt.Sprintf("channel=%d->%d", before.ChannelID, msg.ChannelID), before.ChannelID, msg.ChannelID)
	return nil
}
