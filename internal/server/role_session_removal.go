package server

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.uber.org/zap"
	"noxa/internal/authorization"
	"noxa/internal/filetransfer"
	"noxa/internal/state"
)

func (s *TCPServer) withExclusiveRolePolicy(ctx context.Context, effect func(context.Context) error) error {
	if s.deps == nil || s.deps.Authority == nil {
		return authorization.ErrRolesNotConfigured
	}
	if _, nested := ctx.Value(roleLeaseKey{}).(roleLease); nested {
		return authorization.ErrRoleInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return s.deps.Authority.WithExclusivePolicy(ctx, func(e *authorization.RoleEvaluator) error {
		return effect(context.WithValue(ctx, roleLeaseKey{}, roleLease{s.deps.Authority, e}))
	})
}

// kickRoleMember runs under an exclusive policy lease and the metadata lock.
// Pending reports cleanup trouble after the session has already been revoked;
// callers must acknowledge that effect instead of retrying it as a new kick.
func (s *TCPServer) kickRoleMember(ctx context.Context, e *authorization.RoleEvaluator, actorID int64, actorUniqueID, actorClientID, targetID, reason string) (pending bool, err error) {
	if targetID == "" || len(reason) > 4096 {
		return false, authorization.ErrRoleInvalid
	}
	if s.deps.State == nil {
		return false, authorization.ErrAuthorizationUnavailable
	}
	if !e.Evaluate(actorID, 0, authorization.KickMembers).Allowed {
		return false, authorization.ErrRoleForbidden
	}
	target, ok := s.clientByID(targetID)
	if !ok || !target.isAuthed() {
		return false, authorization.ErrRoleForbidden
	}
	target.roleActionMu.Lock()
	defer target.roleActionMu.Unlock()
	before, ok := s.deps.State.GetClient(targetID)
	if !ok || !e.CanManageMember(actorID, target.userID()) || !e.Evaluate(actorID, before.ChannelID, authorization.ViewChannel).Allowed ||
		(before.Status == "invisible" && !e.Evaluate(actorID, 0, authorization.ViewConnectionInfo).Allowed) {
		return false, authorization.ErrRoleForbidden
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	event := kickEvent{ClientID: target.ID, ChannelID: before.ChannelID, ByClientID: actorClientID, Reason: reason, FromServer: true}
	pending = s.removeRoleSession(ctx, target, event)
	auditCtx, cancelAudit := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancelAudit()
	s.auditInChannels(auditCtx, actorUniqueID, "kick", target.uniqueID(), fmt.Sprintf("from_server=true duration_seconds=0 reason=%s", reason))
	return pending, nil
}

// removeRoleSession requires exclusive policy, metadata and target locks. It
// performs teardown after its caller has authorized and committed removal.
func (s *TCPServer) removeRoleSession(ctx context.Context, target *Client, event kickEvent) (pending bool) {
	target.revokeSession()
	s.unregister(target.ID)
	s.disconnectPrivateCall(target.ID)
	if s.deps.FileTransfer != nil {
		s.deps.FileTransfer.RevokeTransfers(func(p filetransfer.Principal, _ int64, _ string) bool { return p.SessionID != target.ID })
	}
	var removalErr error
	if s.deps.Channels != nil {
		_, removalErr = s.deps.Channels.RemoveClient(target.ID)
	} else {
		s.deps.State.RemoveClient(target.ID)
	}
	if errors.Is(removalErr, state.ErrClientNotFound) {
		removalErr = nil
	}
	var voiceErr error
	if s.deps.Voice != nil {
		voiceErr = s.deps.Voice.ClosePeer(target.ID)
	}
	pending = removalErr != nil || voiceErr != nil
	if !pending {
		target.markDisconnectCleaned()
	}
	if s.deps.Broadcast != nil {
		s.deps.Broadcast.Unregister(target.ID)
	}
	s.sendTerminalEventInContext(ctx, target, eventKicked, event)
	_ = target.Conn.Close()
	s.broadcastEvent(eventKicked, event)
	if removalErr == nil && event.ChannelID > 0 {
		s.rotateScopeKey(ctx, event.ChannelID)
	}
	if pending {
		s.logger.Warn("kicked session cleanup pending", zap.String("client_id", target.ID), zap.Error(errors.Join(removalErr, voiceErr)))
	}
	return pending
}
