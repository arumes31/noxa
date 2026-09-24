package server

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.uber.org/zap"
	"noxa/internal/authorization"
	"noxa/internal/netproto"
	"noxa/internal/recorder"
)

const maxRoleBanSeconds = netproto.MaxBanDurationSeconds

type roleBanResult struct {
	Saved     bool
	Pending   bool
	UniqueID  string
	ExpiresAt time.Time
}

// banRoleMember runs under exclusive policy and metadata locks. A persistence
// error may represent a lost commit acknowledgement: conservatively disconnect
// the authorized target's sessions while reporting that the ban is unconfirmed.
func (s *TCPServer) banRoleMember(ctx context.Context, e *authorization.RoleEvaluator, actorID int64, actorUniqueID, actorClientID, targetID, reason string, seconds int64) (roleBanResult, error) {
	var result roleBanResult
	if targetID == "" || len(reason) > 4096 || seconds < 0 || seconds > maxRoleBanSeconds {
		return result, authorization.ErrRoleInvalid
	}
	if s.deps.State == nil {
		return result, authorization.ErrAuthorizationUnavailable
	}
	if !e.Evaluate(actorID, 0, authorization.BanMembers).Allowed {
		return result, authorization.ErrRoleForbidden
	}
	target, ok := s.clientByID(targetID)
	if !ok || !target.isAuthed() {
		return result, authorization.ErrRoleForbidden
	}
	before, ok := s.deps.State.GetClient(targetID)
	if !ok || !e.CanManageMember(actorID, target.userID()) || !e.Evaluate(actorID, before.ChannelID, authorization.ViewChannel).Allowed ||
		(before.Status == "invisible" && !e.Evaluate(actorID, 0, authorization.ViewConnectionInfo).Allowed) {
		return result, authorization.ErrRoleForbidden
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	result.UniqueID, result.ExpiresAt = target.uniqueID(), banExpiration(seconds)
	var bannedBy any
	if actorID > 0 {
		bannedBy = actorID
	}
	insertErr := s.insertBan(ctx, result.UniqueID, reason, bannedBy, persistentBanExpiration(result.ExpiresAt))
	result.Saved = insertErr == nil
	// First close admission for every matching session and recording. Do this
	// before any collaborator or notification can consume the cleanup deadline.
	var sessions []*Client
	s.mu.RLock()
	for _, c := range s.clients {
		if c.isAuthed() && c.uniqueID() == result.UniqueID {
			sessions = append(sessions, c)
		}
	}
	s.mu.RUnlock()
	for _, c := range sessions {
		c.revokeSession()
	}
	var recordings []int64
	if target.userID() > 0 {
		s.roleRecordingOwners.Range(func(key, value any) bool {
			if value.(int64) == target.userID() {
				channelID := key.(int64)
				s.roleRevokedRecordings.Store(channelID, true)
				recordings = append(recordings, channelID)
			}
			return true
		})
	}
	cleanupCtx, cancelCleanup := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancelCleanup()
	for _, c := range sessions {
		c.roleActionMu.Lock()
		channelID, _, _ := s.deps.State.ClientChannelState(c.ID)
		event := kickEvent{ClientID: c.ID, ChannelID: channelID, ByClientID: actorClientID, Reason: reason, FromServer: true, Ban: result.Saved, ExpiresAt: banExpirationMillis(result.ExpiresAt)}
		result.Pending = s.removeRoleSession(cleanupCtx, c, event) || result.Pending
		c.roleActionMu.Unlock()
	}
	for _, channelID := range recordings {
		stopper, ok := s.deps.Recorder.(interface {
			StopContext(context.Context, int64) error
		})
		if !ok {
			result.Pending = true
			continue
		}
		if err := stopper.StopContext(cleanupCtx, channelID); err != nil && !errors.Is(err, recorder.ErrNotRecording) {
			result.Pending = true
			s.logger.Warn("banned account recording cleanup pending", zap.Int64("channel_id", channelID), zap.Error(err))
			continue
		}
		s.roleRecordingOwners.Delete(channelID)
		s.roleRevokedRecordings.Delete(channelID)
	}
	action := "ban"
	if !result.Saved {
		action = "ban_unconfirmed"
	}
	auditCtx, cancelAudit := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancelAudit()
	s.auditInChannels(auditCtx, actorUniqueID, action, result.UniqueID, fmt.Sprintf("duration_seconds=%d cleanup_pending=%t reason=%s", seconds, result.Pending, reason))
	return result, insertErr
}
