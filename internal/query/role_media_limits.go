package query

import (
	"context"
	"time"

	"noxa/internal/auth"
	"noxa/internal/netproto"
)

type RoleMediaLimitsBackend interface {
	WithIntegrationMediaLimits(context.Context, auth.IntegrationPrincipal, func(context.Context, netproto.MediaLimitsChanged) error) error
	SetIntegrationMediaLimits(context.Context, auth.IntegrationPrincipal, netproto.MediaLimits) (netproto.MediaLimitsSaved, error)
}

func (s *Server) executeRoleMediaLimits(ctx context.Context, sess *session, backend RoleIntegrationBackend, cmd command) bool {
	b, ok := backend.(RoleMediaLimitsBackend)
	if !ok {
		return s.write(sess, errorLine(errInsufficientPermissions, "media limit management unavailable"))
	}
	if cmd.name == "medialimits" {
		if len(cmd.args) != 0 || len(cmd.positional) != 0 {
			return s.write(sess, errorLine(errInvalidParameter, "expected medialimits"))
		}
		return s.executeIntegrationJSON(ctx, sess, 10*time.Second, func(ctx context.Context, deliver func(context.Context, any) error) error {
			return b.WithIntegrationMediaLimits(ctx, sess.principal, func(ctx context.Context, limits netproto.MediaLimitsChanged) error {
				return deliver(ctx, limits)
			})
		})
	}
	var request netproto.MediaLimitsSet
	if len(cmd.args) != 1 || len(cmd.positional) != 0 || decodeRoleRequest(cmd.args["data"], &request) != nil {
		return s.write(sess, errorLine(errInvalidParameter, "expected data=<escaped_complete_media_limits>"))
	}
	limits, valid := request.Limits()
	if !valid {
		return s.write(sess, errorLine(errInvalidParameter, "expected data=<escaped_complete_media_limits>"))
	}
	return s.executeIntegrationJSON(ctx, sess, 10*time.Second, func(ctx context.Context, deliver func(context.Context, any) error) error {
		result, err := b.SetIntegrationMediaLimits(ctx, sess.principal, limits)
		if err != nil {
			return err
		}
		return deliverIntegrationCommit(ctx, deliver, result)
	})
}
