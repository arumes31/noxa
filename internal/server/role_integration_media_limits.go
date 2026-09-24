package server

import (
	"context"

	"noxa/internal/auth"
	"noxa/internal/authorization"
	"noxa/internal/netproto"
)

// WithIntegrationMediaLimits pins ManageServer through delivery of one
// coherent limits/revision snapshot.
func (s *TCPServer) WithIntegrationMediaLimits(ctx context.Context, principal auth.IntegrationPrincipal, deliver func(context.Context, netproto.MediaLimitsChanged) error) error {
	if deliver == nil {
		return authorization.ErrRoleInvalid
	}
	return s.withIntegrationPolicy(ctx, principal, func(ctx context.Context, e *authorization.RoleEvaluator) error {
		if !e.Evaluate(principal.UserID(), 0, authorization.ManageServer).Allowed {
			return authorization.ErrRoleForbidden
		}
		return deliver(ctx, s.mediaLimitsSnapshot())
	})
}

// SetIntegrationMediaLimits shares the native coordinated save and returns
// this request's committed values/revision without a subsequent state read.
func (s *TCPServer) SetIntegrationMediaLimits(ctx context.Context, principal auth.IntegrationPrincipal, limits netproto.MediaLimits) (netproto.MediaLimitsSaved, error) {
	if !limits.Valid() {
		return netproto.MediaLimitsSaved{}, authorization.ErrRoleInvalid
	}
	var result netproto.MediaLimitsChanged
	err := s.withIntegrationPolicy(ctx, principal, func(ctx context.Context, e *authorization.RoleEvaluator) error {
		if !e.Evaluate(principal.UserID(), 0, authorization.ManageServer).Allowed {
			return authorization.ErrRoleForbidden
		}
		var err error
		result, err = s.saveMediaLimits(ctx, principal.UniqueID(), limits)
		return err
	})
	return netproto.MediaLimitsSaved(result), err
}
