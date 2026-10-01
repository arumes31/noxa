package server

import (
	"context"

	"noxa/internal/auth"
	"noxa/internal/authorization"
	"noxa/internal/netproto"
)

// SetIntegrationServerConfig shares native validation and serialized save order.
// The result acknowledges this request's saved values, not a subsequent read.
func (s *TCPServer) SetIntegrationServerConfig(ctx context.Context, principal auth.IntegrationPrincipal, request netproto.ServerConfig) (netproto.ServerConfig, error) {
	if !request.ValidLimits() {
		return netproto.ServerConfig{}, authorization.ErrRoleInvalid
	}
	var result netproto.ServerConfig
	err := s.withIntegrationPolicy(ctx, principal, func(ctx context.Context, e *authorization.RoleEvaluator) error {
		if !e.Evaluate(principal.UserID(), 0, authorization.ManageServer).Allowed {
			return authorization.ErrRoleForbidden
		}
		var err error
		result, err = s.saveServerConfig(ctx, principal.UniqueID(), request)
		return err
	})
	return result, err
}
