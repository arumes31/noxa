package server

import (
	"context"

	"noxa/internal/auth"
	"noxa/internal/authorization"
	"noxa/internal/netproto"
)

func (s *TCPServer) KickIntegrationMember(ctx context.Context, principal auth.IntegrationPrincipal, request netproto.MemberKick) (netproto.MemberKickResult, error) {
	var result netproto.MemberKickResult
	err := s.withExclusiveIntegrationPolicy(ctx, principal, func(ctx context.Context, e *authorization.RoleEvaluator) error {
		pending, err := s.kickRoleMember(ctx, e, principal.UserID(), principal.UniqueID(), "", request.ClientID, request.Reason)
		if err != nil {
			return err
		}
		result = netproto.MemberKickResult{ClientID: request.ClientID, CleanupPending: pending}
		return nil
	})
	return result, err
}

func (s *TCPServer) BanIntegrationMember(ctx context.Context, principal auth.IntegrationPrincipal, request netproto.MemberBan) (netproto.MemberBanResult, error) {
	var result netproto.MemberBanResult
	err := s.withExclusiveIntegrationPolicy(ctx, principal, func(ctx context.Context, e *authorization.RoleEvaluator) error {
		ban, err := s.banRoleMember(ctx, e, principal.UserID(), principal.UniqueID(), "", request.ClientID, request.Reason, request.DurationSeconds)
		if ban.UniqueID == "" {
			return err
		}
		// Revocation has happened even if INSERT returned an uncertain error.
		// Deliver its explicit outcome instead of encouraging a blind retry.
		result = netproto.MemberBanResult{UniqueID: ban.UniqueID, Persistence: netproto.BanUnconfirmed, CleanupPending: ban.Pending}
		if ban.Saved {
			result.Persistence = netproto.BanSaved
			result.ExpiresAt = banExpirationMillis(ban.ExpiresAt)
		}
		return nil
	})
	return result, err
}

func (s *TCPServer) DisconnectIntegrationMember(ctx context.Context, principal auth.IntegrationPrincipal, request netproto.MemberDisconnect) (netproto.MemberDisconnectResult, error) {
	if request.ChannelID <= 0 {
		return netproto.MemberDisconnectResult{}, authorization.ErrRoleInvalid
	}
	var result netproto.MemberDisconnectResult
	err := s.withIntegrationPolicy(ctx, principal, func(ctx context.Context, e *authorization.RoleEvaluator) error {
		var err error
		result, err = s.disconnectRoleMember(ctx, e, principal.UserID(), principal.UniqueID(), "", request.ClientID, request.ChannelID, request.Reason)
		return err
	})
	return result, err
}

// MoveIntegrationMember uses the current source/destination policy and native
// membership lifecycle. The integration has no synthetic privileged client.
func (s *TCPServer) MoveIntegrationMember(ctx context.Context, principal auth.IntegrationPrincipal, request netproto.MoveClient) error {
	return s.withIntegrationPolicy(ctx, principal, func(ctx context.Context, e *authorization.RoleEvaluator) error {
		return s.moveRoleMember(ctx, e, principal.UserID(), principal.UniqueID(), "", request)
	})
}

// SetIntegrationMemberVoice changes session moderation flags under the same
// current policy, admission, membership and target locks as native moderation.
func (s *TCPServer) SetIntegrationMemberVoice(ctx context.Context, principal auth.IntegrationPrincipal, request netproto.MemberVoiceSet) (netproto.MemberVoiceState, error) {
	var result netproto.MemberVoiceState
	err := s.withIntegrationPolicy(ctx, principal, func(ctx context.Context, e *authorization.RoleEvaluator) error {
		var err error
		result, err = s.setRoleMemberVoice(ctx, e, principal.UserID(), principal.UniqueID(), request)
		return err
	})
	return result, err
}
