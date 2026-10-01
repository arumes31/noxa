package server

import (
	"context"
	"fmt"

	"noxa/internal/authorization"
	"noxa/internal/netproto"
)

const eventMemberVoiceChanged = "member_voice_changed"

func (s *TCPServer) handleMemberVoiceSet(ctx context.Context, client *Client, f *netproto.Frame) error {
	var msg netproto.MemberVoiceSet
	if err := netproto.Decode(f, &msg); err != nil || (msg.Muted == nil && msg.Deafened == nil) || msg.ChannelID <= 0 || msg.ClientID == "" {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "invalid member voice change")
	}
	if s.deps == nil || s.deps.Authority == nil || s.deps.State == nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "role moderation unavailable")
	}
	return s.roleAction(ctx, client, msg.ChannelID, authorization.ViewChannel, func(ctx context.Context) error {
		s.roleMetadataMu.Lock()
		defer s.roleMetadataMu.Unlock()
		if client.rulesBlocked() {
			return authorization.ErrRoleForbidden
		}
		e := ctx.Value(roleLeaseKey{}).(roleLease).evaluator
		result, err := s.setRoleMemberVoice(ctx, e, client.userID(), client.uniqueID(), msg)
		if err != nil {
			return err
		}
		return s.writeMessage(client, netproto.MsgMemberVoiceState, result)
	})
}

// Caller retains the current policy and metadata barriers through this effect.
// Target movement/media serialize through roleActionMu; both flags are checked
// before either is changed. Actor identity never comes from the request.
func (s *TCPServer) setRoleMemberVoice(ctx context.Context, e *authorization.RoleEvaluator, actorID int64, actorUniqueID string, msg netproto.MemberVoiceSet) (netproto.MemberVoiceState, error) {
	if (msg.Muted == nil && msg.Deafened == nil) || msg.ChannelID <= 0 || msg.ClientID == "" {
		return netproto.MemberVoiceState{}, authorization.ErrRoleInvalid
	}
	if s.deps.State == nil {
		return netproto.MemberVoiceState{}, authorization.ErrAuthorizationUnavailable
	}
	if !e.Evaluate(actorID, msg.ChannelID, authorization.ViewChannel).Allowed {
		return netproto.MemberVoiceState{}, authorization.ErrRoleForbidden
	}
	target, ok := s.clientByID(msg.ClientID)
	if !ok || !target.isAuthed() {
		return netproto.MemberVoiceState{}, authorization.ErrRoleForbidden
	}
	target.roleActionMu.Lock()
	defer target.roleActionMu.Unlock()
	before, ok := s.deps.State.GetClient(target.ID)
	if !ok || before.ChannelID != msg.ChannelID || !e.CanManageMember(actorID, target.userID()) {
		return netproto.MemberVoiceState{}, authorization.ErrRoleForbidden
	}
	if before.Status == "invisible" && !e.Evaluate(actorID, 0, authorization.ViewConnectionInfo).Allowed {
		return netproto.MemberVoiceState{}, authorization.ErrRoleForbidden
	}
	if msg.Muted != nil && !e.Evaluate(actorID, before.ChannelID, authorization.MuteMembers).Allowed {
		return netproto.MemberVoiceState{}, authorization.ErrRoleForbidden
	}
	if msg.Deafened != nil && !e.Evaluate(actorID, before.ChannelID, authorization.DeafenMembers).Allowed {
		return netproto.MemberVoiceState{}, authorization.ErrRoleForbidden
	}
	if err := ctx.Err(); err != nil {
		return netproto.MemberVoiceState{}, err
	}
	after, err := s.deps.State.SetServerVoiceState(target.ID, msg.Muted, msg.Deafened)
	if err != nil {
		return netproto.MemberVoiceState{}, err
	}
	result := netproto.MemberVoiceState{Revision: after.VoiceRevision, ClientID: target.ID, ChannelID: after.ChannelID, Muted: after.ServerMuted, Deafened: after.ServerDeafened}
	s.auditInChannels(ctx, actorUniqueID, "member_voice_changed", target.uniqueID(), fmt.Sprintf("channel=%d muted=%t->%t deafened=%t->%t", after.ChannelID, before.ServerMuted, after.ServerMuted, before.ServerDeafened, after.ServerDeafened), after.ChannelID)
	s.broadcastEvent(eventMemberVoiceChanged, result)
	return result, nil
}
