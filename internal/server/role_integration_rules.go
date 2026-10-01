package server

import (
	"context"

	"noxa/internal/auth"
	"noxa/internal/authorization"
	"noxa/internal/netproto"
)

// WithIntegrationRules includes aggregate acceptance metadata, so it requires
// ManageServer. Ordinary native clients still receive the published text at join.
func (s *TCPServer) WithIntegrationRules(ctx context.Context, principal auth.IntegrationPrincipal, deliver func(context.Context, netproto.RulesInspection) error) error {
	if deliver == nil {
		return authorization.ErrRoleInvalid
	}
	return s.withIntegrationPolicy(ctx, principal, func(ctx context.Context, e *authorization.RoleEvaluator) error {
		if !e.Evaluate(principal.UserID(), 0, authorization.ManageServer).Allowed {
			return authorization.ErrRoleForbidden
		}
		backend, ok := s.deps.Rules.(interface {
			Inspect(context.Context) (string, string, int, error)
		})
		if !ok {
			return authorization.ErrAuthorizationUnavailable
		}
		text, hash, accepted, err := backend.Inspect(ctx)
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		return deliver(ctx, netproto.RulesInspection{Text: text, Hash: hash, AcceptedClients: accepted})
	})
}
