package server

import (
	"context"
	"fmt"
	"time"

	"noxa/internal/authorization"
	"noxa/internal/netproto"
)

// disconnectRoleMember retains the caller's policy/metadata lease through the
// target's membership change. A zero expectedChannel is reserved for the native
// legacy-shaped request, which resolves its scope under the target lock.
func (s *TCPServer) disconnectRoleMember(ctx context.Context, e *authorization.RoleEvaluator, actorID int64, actorUniqueID, actorClientID, targetID string, expectedChannel int64, reason string) (netproto.MemberDisconnectResult, error) {
	if targetID == "" || expectedChannel < 0 || len(reason) > 4096 {
		return netproto.MemberDisconnectResult{}, authorization.ErrRoleInvalid
	}
	if s.deps.State == nil {
		return netproto.MemberDisconnectResult{}, authorization.ErrAuthorizationUnavailable
	}
	target, ok := s.clientByID(targetID)
	if !ok || !target.isAuthed() {
		return netproto.MemberDisconnectResult{}, authorization.ErrRoleForbidden
	}
	target.roleActionMu.Lock()
	defer target.roleActionMu.Unlock()
	before, ok := s.deps.State.GetClient(targetID)
	if !ok || before.ChannelID <= 0 || (expectedChannel != 0 && before.ChannelID != expectedChannel) ||
		!e.CanManageMember(actorID, target.userID()) || !e.Evaluate(actorID, before.ChannelID, authorization.DisconnectMembers).Allowed ||
		(before.Status == "invisible" && !e.Evaluate(actorID, 0, authorization.ViewConnectionInfo).Allowed) {
		return netproto.MemberDisconnectResult{}, authorization.ErrRoleForbidden
	}
	if err := ctx.Err(); err != nil {
		return netproto.MemberDisconnectResult{}, err
	}
	if err := s.leaveOwnChannelInContext(ctx, target); err != nil {
		return netproto.MemberDisconnectResult{}, err
	}
	s.broadcastEvent(eventKicked, kickEvent{ClientID: targetID, ChannelID: before.ChannelID, ByClientID: actorClientID, Reason: reason})
	// Membership already changed. Notification cancellation must not suppress
	// its audit, but a failing audit backend still gets a bounded attempt.
	auditCtx, cancelAudit := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancelAudit()
	s.auditInChannels(auditCtx, actorUniqueID, "kick", target.uniqueID(), fmt.Sprintf("from_server=false duration_seconds=0 reason=%s", reason), before.ChannelID)
	return netproto.MemberDisconnectResult{ClientID: targetID, ChannelID: before.ChannelID}, nil
}
