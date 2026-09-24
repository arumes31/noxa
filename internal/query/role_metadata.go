package query

import (
	"context"
	"time"

	"noxa/internal/auth"
	"noxa/internal/netproto"
)

// RoleMetadataBackend keeps metadata and its field-level permissions pinned
// through bounded delivery, without calling legacy unfiltered read methods.
type RoleMetadataBackend interface {
	WithIntegrationServerInfo(context.Context, auth.IntegrationPrincipal, func(context.Context, netproto.ServerInfoResponse) error) error
	WithIntegrationClientInfo(context.Context, auth.IntegrationPrincipal, string, func(context.Context, netproto.ClientInfoResponse) error) error
	WithIntegrationServerConfig(context.Context, auth.IntegrationPrincipal, func(context.Context, netproto.ServerConfig) error) error
}

func (s *Server) executeRoleMetadata(ctx context.Context, sess *session, backend RoleIntegrationBackend, cmd command) bool {
	b, ok := backend.(RoleMetadataBackend)
	if !ok {
		return s.write(sess, errorLine(errInsufficientPermissions, "metadata inspection unavailable"))
	}
	if len(cmd.positional) != 0 || (cmd.name == "clientinfo" && (len(cmd.args) != 1 || cmd.args["clid"] == "")) || (cmd.name != "clientinfo" && len(cmd.args) != 0) {
		return s.write(sess, errorLine(errInvalidParameter, "expected serverinfo, serverconfig or clientinfo clid=<client_id>"))
	}
	return s.executeIntegrationJSON(ctx, sess, 10*time.Second, func(ctx context.Context, deliver func(context.Context, any) error) error {
		switch cmd.name {
		case "serverinfo":
			return b.WithIntegrationServerInfo(ctx, sess.principal, func(ctx context.Context, result netproto.ServerInfoResponse) error { return deliver(ctx, result) })
		case "clientinfo":
			return b.WithIntegrationClientInfo(ctx, sess.principal, cmd.args["clid"], func(ctx context.Context, result netproto.ClientInfoResponse) error { return deliver(ctx, result) })
		default:
			return b.WithIntegrationServerConfig(ctx, sess.principal, func(ctx context.Context, result netproto.ServerConfig) error { return deliver(ctx, result) })
		}
	})
}
