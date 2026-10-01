package query

import (
	"context"
	"time"

	"noxa/internal/auth"
	"noxa/internal/netproto"
)

// RoleAuditBackend supplies native scoped audit pages through protected delivery.
type RoleAuditBackend interface {
	WithIntegrationAudit(context.Context, auth.IntegrationPrincipal, netproto.AuditLog, func(context.Context, netproto.AuditLogResponse) error) error
}

func (s *Server) executeRoleAudit(ctx context.Context, sess *session, backend RoleIntegrationBackend, cmd command) bool {
	b, ok := backend.(RoleAuditBackend)
	if !ok {
		return s.write(sess, errorLine(errInsufficientPermissions, "audit inspection unavailable"))
	}
	var request netproto.AuditLog
	if len(cmd.positional) != 0 || len(cmd.args) != 1 || decodeRoleRequest(cmd.args["data"], &request) != nil || request.BeforeID < 0 || request.Limit < 0 || request.Limit > 200 {
		return s.write(sess, errorLine(errInvalidParameter, "auditquery requires data=<escaped JSON> with nonnegative before_id and limit from 0 to 200"))
	}
	return s.executeIntegrationJSON(ctx, sess, 10*time.Second, func(ctx context.Context, deliver func(context.Context, any) error) error {
		return b.WithIntegrationAudit(ctx, sess.principal, request, func(ctx context.Context, response netproto.AuditLogResponse) error { return deliver(ctx, response) })
	})
}
