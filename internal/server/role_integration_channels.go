package server

import (
	"context"

	"noxa/internal/auth"
	"noxa/internal/authorization"
	"noxa/internal/netproto"
)

func (s *TCPServer) WithIntegrationChannelState(ctx context.Context, principal auth.IntegrationPrincipal, query netproto.RoleChannelQuery, deliver func(context.Context, netproto.RoleChannelState) error) error {
	if deliver == nil {
		return authorization.ErrRoleInvalid
	}
	return s.withIntegrationPolicy(ctx, principal, func(ctx context.Context, e *authorization.RoleEvaluator) error {
		if s.deps.State == nil || s.deps.State.ChannelCount() > maxIntegrationSnapshotItems {
			return authorization.ErrAuthorizationUnavailable
		}
		p, err := e.BoundedPolicy(ctx, maxIntegrationSnapshotItems)
		if err != nil {
			return err
		}
		response, err := s.buildRoleChannelState(ctx, p, e, principal.UserID(), query)
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		return deliver(ctx, response)
	})
}

func (s *TCPServer) ChangeIntegrationChannel(ctx context.Context, principal auth.IntegrationPrincipal, request netproto.RoleChannelChange) (netproto.RoleChannelResult, error) {
	if !s.RoleIntegrationsEnabled() {
		return netproto.RoleChannelResult{}, authorization.ErrRolesNotConfigured
	}
	a, ok := s.deps.Auth.(integrationAuthenticator)
	if !ok {
		return netproto.RoleChannelResult{}, authorization.ErrAuthorizationUnavailable
	}
	return s.changeRoleChannel(ctx, principal.UserID(), request, func(ctx context.Context) error {
		s.roleMetadataMu.Lock()
		defer s.roleMetadataMu.Unlock()
		return a.ValidateIntegration(ctx, principal)
	})
}
