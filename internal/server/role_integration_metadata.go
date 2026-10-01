package server

import (
	"context"

	"noxa/internal/auth"
	"noxa/internal/authorization"
	"noxa/internal/netproto"
)

// WithIntegrationServerInfo shares native public information, including only
// visible population counts. Delivery must finish inside the callback.
func (s *TCPServer) WithIntegrationServerInfo(ctx context.Context, principal auth.IntegrationPrincipal, deliver func(context.Context, netproto.ServerInfoResponse) error) error {
	if deliver == nil {
		return authorization.ErrRoleInvalid
	}
	return s.withIntegrationPolicy(ctx, principal, func(ctx context.Context, e *authorization.RoleEvaluator) error {
		if s.deps.State == nil || s.deps.State.ChannelCount()+s.deps.State.ClientCount() > maxIntegrationSnapshotItems {
			return authorization.ErrAuthorizationUnavailable
		}
		info, err := s.buildRoleServerInfo(ctx, e, principal.UserID(), principal.UniqueID())
		if err != nil {
			return err
		}
		return deliver(ctx, info)
	})
}

// WithIntegrationClientInfo uses native target visibility and sensitive-field
// gates without treating any native session as the integration's own session.
func (s *TCPServer) WithIntegrationClientInfo(ctx context.Context, principal auth.IntegrationPrincipal, targetID string, deliver func(context.Context, netproto.ClientInfoResponse) error) error {
	if deliver == nil || targetID == "" {
		return authorization.ErrRoleInvalid
	}
	return s.withIntegrationPolicy(ctx, principal, func(ctx context.Context, e *authorization.RoleEvaluator) error {
		info, err := s.buildRoleClientInfo(e, principal.UserID(), principal.UniqueID(), "", targetID)
		if err != nil {
			return err
		}
		return deliver(ctx, info)
	})
}

// WithIntegrationServerConfig returns runtime settings only to ManageServer.
func (s *TCPServer) WithIntegrationServerConfig(ctx context.Context, principal auth.IntegrationPrincipal, deliver func(context.Context, netproto.ServerConfig) error) error {
	if deliver == nil {
		return authorization.ErrRoleInvalid
	}
	return s.withIntegrationPolicy(ctx, principal, func(ctx context.Context, e *authorization.RoleEvaluator) error {
		if !e.Evaluate(principal.UserID(), 0, authorization.ManageServer).Allowed {
			return authorization.ErrRoleForbidden
		}
		return deliver(ctx, s.serverConfig())
	})
}
