package query

import (
	"context"
	"time"

	"noxa/internal/auth"
	"noxa/internal/netproto"
)

type RoleMemberRemovalBackend interface {
	KickIntegrationMember(context.Context, auth.IntegrationPrincipal, netproto.MemberKick) (netproto.MemberKickResult, error)
	BanIntegrationMember(context.Context, auth.IntegrationPrincipal, netproto.MemberBan) (netproto.MemberBanResult, error)
}

func (s *Server) executeRoleMemberRemoval(ctx context.Context, sess *session, backend RoleIntegrationBackend, cmd command) bool {
	b, ok := backend.(RoleMemberRemovalBackend)
	if !ok {
		return s.write(sess, errorLine(errInsufficientPermissions, "role member removal unavailable"))
	}
	if cmd.name == "memberkick" {
		var request netproto.MemberKick
		if err := decodeRoleRequest(cmd.args["data"], &request); err != nil || request.ClientID == "" || len(request.Reason) > 4096 {
			return s.write(sess, errorLine(errInvalidParameter, "memberkick requires data=<escaped JSON> with client_id and optional reason"))
		}
		return s.executeIntegrationJSON(ctx, sess, 30*time.Second, func(ctx context.Context, deliver func(context.Context, any) error) error {
			result, err := b.KickIntegrationMember(ctx, sess.principal, request)
			if err != nil {
				return err
			}
			return deliverIntegrationCommit(ctx, deliver, result)
		})
	}
	var request netproto.MemberBan
	if err := decodeRoleRequest(cmd.args["data"], &request); err != nil || request.ClientID == "" || len(request.Reason) > 4096 || request.DurationSeconds < 0 || request.DurationSeconds > netproto.MaxBanDurationSeconds {
		return s.write(sess, errorLine(errInvalidParameter, "memberban requires data=<escaped JSON> with client_id and nonnegative duration_seconds; zero means permanent"))
	}
	return s.executeIntegrationJSON(ctx, sess, 30*time.Second, func(ctx context.Context, deliver func(context.Context, any) error) error {
		result, err := b.BanIntegrationMember(ctx, sess.principal, request)
		if err != nil {
			return err
		}
		return deliverIntegrationCommit(ctx, deliver, result)
	})
}
