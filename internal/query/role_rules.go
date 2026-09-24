package query

import (
	"context"
	"time"

	"noxa/internal/auth"
	"noxa/internal/netproto"
)

type RoleRulesBackend interface {
	WithIntegrationRules(context.Context, auth.IntegrationPrincipal, func(context.Context, netproto.RulesInspection) error) error
}

func (s *Server) executeRoleRules(ctx context.Context, sess *session, backend RoleIntegrationBackend, cmd command) bool {
	b, ok := backend.(RoleRulesBackend)
	if !ok {
		return s.write(sess, errorLine(errInsufficientPermissions, "rules inspection unavailable"))
	}
	if len(cmd.args) != 0 || len(cmd.positional) != 0 {
		return s.write(sess, errorLine(errInvalidParameter, "expected rulesquery without arguments"))
	}
	return s.executeIntegrationJSON(ctx, sess, 10*time.Second, func(ctx context.Context, deliver func(context.Context, any) error) error {
		return b.WithIntegrationRules(ctx, sess.principal, func(ctx context.Context, result netproto.RulesInspection) error { return deliver(ctx, result) })
	})
}
