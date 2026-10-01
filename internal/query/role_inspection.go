package query

import (
	"context"
	"time"

	"noxa/internal/auth"
	"noxa/internal/authorization"
	"noxa/internal/netproto"
)

// RoleInspectionBackend keeps member search and access explanations behind
// the same bounded-delivery contract as other role-aware integration reads.
type RoleInspectionBackend interface {
	WithIntegrationRoleMembers(context.Context, auth.IntegrationPrincipal, authorization.MemberQuery, func(context.Context, authorization.MemberPage) error) error
	WithIntegrationAccessCheck(context.Context, auth.IntegrationPrincipal, netproto.AccessCheck, func(context.Context, netproto.AccessCheckResult) error) error
}

func (s *Server) executeRoleInspection(ctx context.Context, sess *session, backend RoleIntegrationBackend, cmd command) bool {
	b, ok := backend.(RoleInspectionBackend)
	if !ok {
		return s.write(sess, errorLine(errInsufficientPermissions, "role inspection unavailable"))
	}
	var members authorization.MemberQuery
	var check netproto.AccessCheck
	var err error
	if cmd.name == "rolemembers" {
		err = decodeRoleRequest(cmd.args["data"], &members)
		if members.ExpectedRevision <= 0 || members.ChannelID < 0 || members.AfterID < 0 || len(members.Search) > 100 {
			err = authorization.ErrRoleInvalid
		}
	} else {
		err = decodeRoleRequest(cmd.args["data"], &check)
		if check.ExpectedRevision <= 0 || check.ChannelID < 0 || check.UserID < 0 || check.Capability == "" {
			err = authorization.ErrRoleInvalid
		}
	}
	if err != nil {
		return s.write(sess, errorLine(errInvalidParameter, "expected one data=<escaped JSON> with a positive expected_revision"))
	}
	return s.executeIntegrationJSON(ctx, sess, 10*time.Second, func(ctx context.Context, deliver func(context.Context, any) error) error {
		if cmd.name == "rolemembers" {
			return b.WithIntegrationRoleMembers(ctx, sess.principal, members, func(ctx context.Context, page authorization.MemberPage) error { return deliver(ctx, page) })
		}
		return b.WithIntegrationAccessCheck(ctx, sess.principal, check, func(ctx context.Context, result netproto.AccessCheckResult) error { return deliver(ctx, result) })
	})
}
