package query

import (
	"context"
	"time"

	"noxa/internal/auth"
	"noxa/internal/netproto"
)

type RoleVoiceModerationBackend interface {
	SetIntegrationMemberVoice(context.Context, auth.IntegrationPrincipal, netproto.MemberVoiceSet) (netproto.MemberVoiceState, error)
}

type RoleMemberMoveBackend interface {
	MoveIntegrationMember(context.Context, auth.IntegrationPrincipal, netproto.MoveClient) error
}

type RoleMemberDisconnectBackend interface {
	DisconnectIntegrationMember(context.Context, auth.IntegrationPrincipal, netproto.MemberDisconnect) (netproto.MemberDisconnectResult, error)
}

func (s *Server) executeRoleMemberDisconnect(ctx context.Context, sess *session, backend RoleIntegrationBackend, cmd command) bool {
	b, ok := backend.(RoleMemberDisconnectBackend)
	if !ok {
		return s.write(sess, errorLine(errInsufficientPermissions, "role member disconnection unavailable"))
	}
	var request netproto.MemberDisconnect
	if err := decodeRoleRequest(cmd.args["data"], &request); err != nil || request.ChannelID <= 0 || request.ClientID == "" || len(request.Reason) > 4096 {
		return s.write(sess, errorLine(errInvalidParameter, "memberdisconnect requires data=<escaped JSON> with client_id and current channel_id"))
	}
	return s.executeIntegrationJSON(ctx, sess, 10*time.Second, func(ctx context.Context, deliver func(context.Context, any) error) error {
		result, err := b.DisconnectIntegrationMember(ctx, sess.principal, request)
		if err != nil {
			return err
		}
		return deliverIntegrationCommit(ctx, deliver, result)
	})
}

func (s *Server) executeRoleMemberMove(ctx context.Context, sess *session, backend RoleIntegrationBackend, cmd command) bool {
	b, ok := backend.(RoleMemberMoveBackend)
	if !ok {
		return s.write(sess, errorLine(errInsufficientPermissions, "role member movement unavailable"))
	}
	var request netproto.MoveClient
	if err := decodeRoleRequest(cmd.args["data"], &request); err != nil || request.ChannelID <= 0 || request.ClientID == "" {
		return s.write(sess, errorLine(errInvalidParameter, "membermove requires data=<escaped JSON> with client_id and destination channel_id"))
	}
	return s.executeIntegrationJSON(ctx, sess, 10*time.Second, func(ctx context.Context, deliver func(context.Context, any) error) error {
		if err := b.MoveIntegrationMember(ctx, sess.principal, request); err != nil {
			return err
		}
		return deliverIntegrationCommit(ctx, deliver, request)
	})
}

func (s *Server) executeRoleVoiceModeration(ctx context.Context, sess *session, backend RoleIntegrationBackend, cmd command) bool {
	b, ok := backend.(RoleVoiceModerationBackend)
	if !ok {
		return s.write(sess, errorLine(errInsufficientPermissions, "role voice moderation unavailable"))
	}
	var request netproto.MemberVoiceSet
	if err := decodeRoleRequest(cmd.args["data"], &request); err != nil || request.ChannelID <= 0 || request.ClientID == "" || (request.Muted == nil && request.Deafened == nil) {
		return s.write(sess, errorLine(errInvalidParameter, "membervoice requires data=<escaped JSON> with client_id, channel_id and muted or deafened"))
	}
	return s.executeIntegrationJSON(ctx, sess, 10*time.Second, func(ctx context.Context, deliver func(context.Context, any) error) error {
		result, err := b.SetIntegrationMemberVoice(ctx, sess.principal, request)
		if err != nil {
			return err
		}
		return deliverIntegrationCommit(ctx, deliver, result)
	})
}
