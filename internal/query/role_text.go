package query

import (
	"context"
	"time"

	"noxa/internal/auth"
	"noxa/internal/netproto"
)

type RoleTextBackend interface {
	SetIntegrationServerText(context.Context, auth.IntegrationPrincipal, netproto.ServerTextSet) (netproto.ServerTextResult, error)
}

func (s *Server) executeRoleText(ctx context.Context, sess *session, backend RoleIntegrationBackend, cmd command) bool {
	b, ok := backend.(RoleTextBackend)
	if !ok {
		return s.write(sess, errorLine(errInsufficientPermissions, "server text management unavailable"))
	}
	var request netproto.ServerTextSet
	if len(cmd.args) != 1 || len(cmd.positional) != 0 || decodeRoleRequest(cmd.args["data"], &request) != nil || !request.Valid() {
		return s.write(sess, errorLine(errInvalidParameter, "expected data=<escaped JSON with supported key and value>"))
	}
	return s.executeIntegrationJSON(ctx, sess, 10*time.Second, func(ctx context.Context, deliver func(context.Context, any) error) error {
		result, err := b.SetIntegrationServerText(ctx, sess.principal, request)
		if err != nil {
			return err
		}
		return deliverIntegrationCommit(ctx, deliver, result)
	})
}
