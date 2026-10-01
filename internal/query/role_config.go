package query

import (
	"context"
	"time"

	"noxa/internal/auth"
	"noxa/internal/netproto"
)

type RoleConfigBackend interface {
	SetIntegrationServerConfig(context.Context, auth.IntegrationPrincipal, netproto.ServerConfig) (netproto.ServerConfig, error)
}

func (s *Server) executeRoleConfig(ctx context.Context, sess *session, backend RoleIntegrationBackend, cmd command) bool {
	b, ok := backend.(RoleConfigBackend)
	if !ok {
		return s.write(sess, errorLine(errInsufficientPermissions, "server configuration unavailable"))
	}
	var request netproto.ServerConfig
	if len(cmd.args) != 1 || len(cmd.positional) != 0 || decodeRoleRequest(cmd.args["data"], &request) != nil || !request.ValidLimits() {
		return s.write(sess, errorLine(errInvalidParameter, "expected data=<escaped complete server configuration>"))
	}
	return s.executeIntegrationJSON(ctx, sess, 10*time.Second, func(ctx context.Context, deliver func(context.Context, any) error) error {
		result, err := b.SetIntegrationServerConfig(ctx, sess.principal, request)
		if err != nil {
			return err
		}
		return deliverIntegrationCommit(ctx, deliver, result)
	})
}
