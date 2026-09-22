package query

import (
	"context"
	"time"

	"noxa/internal/auth"
	"noxa/internal/netproto"
)

// RoleChannelBackend uses the native revision-checked resource lifecycle.
type RoleChannelBackend interface {
	WithIntegrationChannelState(context.Context, auth.IntegrationPrincipal, netproto.RoleChannelQuery, func(context.Context, netproto.RoleChannelState) error) error
	ChangeIntegrationChannel(context.Context, auth.IntegrationPrincipal, netproto.RoleChannelChange) (netproto.RoleChannelResult, error)
}

func (s *Server) executeRoleChannelCommand(ctx context.Context, sess *session, backend RoleIntegrationBackend, cmd command) bool {
	b, ok := backend.(RoleChannelBackend)
	if !ok {
		return s.write(sess, errorLine(errInsufficientPermissions, "role channel management unavailable"))
	}
	if cmd.name == "channelquery" {
		var request netproto.RoleChannelQuery
		if err := decodeRoleRequest(cmd.args["data"], &request); err != nil || request.Kind == "" || request.ChannelID < 0 {
			return s.write(sess, errorLine(errInvalidParameter, "channelquery requires data=<escaped JSON> with kind and optional channel_id"))
		}
		return s.executeIntegrationJSON(ctx, sess, 10*time.Second, func(ctx context.Context, deliver func(context.Context, any) error) error {
			return b.WithIntegrationChannelState(ctx, sess.principal, request, func(ctx context.Context, result netproto.RoleChannelState) error { return deliver(ctx, result) })
		})
	}
	var request netproto.RoleChannelChange
	if err := decodeRoleRequest(cmd.args["data"], &request); err != nil || request.ExpectedRevision <= 0 {
		return s.write(sess, errorLine(errInvalidParameter, "channelchange requires data=<escaped JSON> with expected_revision"))
	}
	return s.executeIntegrationJSON(ctx, sess, 30*time.Second, func(ctx context.Context, deliver func(context.Context, any) error) error {
		result, err := b.ChangeIntegrationChannel(ctx, sess.principal, request)
		if err != nil {
			return err
		}
		return deliverIntegrationCommit(ctx, deliver, result)
	})
}
