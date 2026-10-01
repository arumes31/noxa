package query

import (
	"context"
	"time"

	"noxa/internal/auth"
	"noxa/internal/netproto"
)

// RoleComplaintBackend shares native complaint moderation without legacy admin authority.
type RoleComplaintBackend interface {
	WithIntegrationComplaints(context.Context, auth.IntegrationPrincipal, netproto.ComplaintQuery, func(context.Context, netproto.ComplaintPage) error) error
	ClearIntegrationComplaints(context.Context, auth.IntegrationPrincipal, netproto.ComplaintClear) (netproto.ComplaintClearResult, error)
}

func (s *Server) executeRoleComplaints(ctx context.Context, sess *session, backend RoleIntegrationBackend, cmd command) bool {
	b, ok := backend.(RoleComplaintBackend)
	if !ok {
		return s.write(sess, errorLine(errInsufficientPermissions, "complaint management unavailable"))
	}
	if len(cmd.args) != 1 || len(cmd.positional) != 0 {
		return s.write(sess, errorLine(errInvalidParameter, "expected one data=<escaped JSON>"))
	}
	if cmd.name == "complaintquery" {
		var request netproto.ComplaintQuery
		if decodeRoleRequest(cmd.args["data"], &request) != nil || request.AfterID < 0 || request.Limit < 0 || request.Limit > 100 {
			return s.write(sess, errorLine(errInvalidParameter, "invalid complaint page"))
		}
		return s.executeIntegrationJSON(ctx, sess, 10*time.Second, func(ctx context.Context, deliver func(context.Context, any) error) error {
			return b.WithIntegrationComplaints(ctx, sess.principal, request, func(ctx context.Context, page netproto.ComplaintPage) error { return deliver(ctx, page) })
		})
	}
	var request netproto.ComplaintClear
	if decodeRoleRequest(cmd.args["data"], &request) != nil || request.TargetUniqueID == "" {
		return s.write(sess, errorLine(errInvalidParameter, "target_unique_id required"))
	}
	return s.executeIntegrationJSON(ctx, sess, 10*time.Second, func(ctx context.Context, deliver func(context.Context, any) error) error {
		result, err := b.ClearIntegrationComplaints(ctx, sess.principal, request)
		if err != nil {
			return err
		}
		return deliverIntegrationCommit(ctx, deliver, result)
	})
}
