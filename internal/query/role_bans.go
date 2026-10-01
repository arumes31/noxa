package query

import (
	"context"
	"time"

	"noxa/internal/auth"
	"noxa/internal/netproto"
)

// RoleBanInspectionBackend supplies bounded ban recovery reads.
type RoleBanInspectionBackend interface {
	WithIntegrationBans(context.Context, auth.IntegrationPrincipal, netproto.BanQuery, func(context.Context, netproto.BanPage) error) error
}

func (s *Server) executeRoleBanQuery(ctx context.Context, sess *session, backend RoleIntegrationBackend, cmd command) bool {
	b, ok := backend.(RoleBanInspectionBackend)
	if !ok {
		return s.write(sess, errorLine(errInsufficientPermissions, "ban inspection unavailable"))
	}
	var request netproto.BanQuery
	if err := decodeRoleRequest(cmd.args["data"], &request); err != nil || request.BeforeID < 0 || request.Limit < 0 || request.Limit > 100 {
		return s.write(sess, errorLine(errInvalidParameter, "banquery requires data=<escaped JSON> with nonnegative before_id and limit from 0 to 100"))
	}
	return s.executeIntegrationJSON(ctx, sess, 10*time.Second, func(ctx context.Context, deliver func(context.Context, any) error) error {
		return b.WithIntegrationBans(ctx, sess.principal, request, func(ctx context.Context, page netproto.BanPage) error { return deliver(ctx, page) })
	})
}
